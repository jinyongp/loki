package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/platform/safeio"
	"loki/internal/policy"
)

const (
	MaxBatchOperations    = 50
	batchJournalVersion   = 1
	maxBatchJournalBytes  = 256 << 10
	maxBatchJournalFiles  = 200
	maxBatchJournalTotal  = 16 << 20
	batchStateInProgress  = "in_progress"
	batchStateApplied     = "applied"
	batchStateRolledBack  = "rolled_back"
	batchStateRecovered   = "recovered"
	batchStateRecoveryBad = "recovery_failed"
)

var batchRequestIDPattern = regexp.MustCompile("(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")

type BatchOperation struct {
	Action               string
	Path                 string
	Content              string
	Old                  string
	New                  string
	ExpectedSHA256       string
	ExpectedReplacements int
	Source               string
	Destination          string
	ExpectedDestination  string
}

type batchPlanStep struct {
	journal  batchJournalStep
	content  []byte
	preimage []byte
}

type batchJournalStep struct {
	Action           string
	Path             string
	Source           string
	Destination      string
	BeforeSHA256     string
	AfterSHA256      string
	PreviousRevision string
	Mode             uint32
}

type batchJournalError struct {
	Code       string
	Message    string
	Retryable  bool
	NextAction string
}

type batchJournalRecord struct {
	Version       int
	RequestID     string
	RequestSHA256 string
	OperationID   string
	State         string
	CreatedAt     string
	UpdatedAt     string
	Steps         []batchJournalStep
	Result        map[string]any
	Error         *batchJournalError
}

type batchJournalEnvelope struct {
	Record json.RawMessage
	SHA256 string
}

func batchError(detail fault.Detail) *batchJournalError {
	return &batchJournalError{
		Code: string(detail.Code), Message: detail.Message,
		Retryable: detail.Retryable, NextAction: detail.NextAction,
	}
}

func (f *Files) batchDir() (string, error) {
	dir := filepath.Join(filepath.Dir(f.Config.AuditLog), "workspace-batches")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func (f *Files) batchRecordPath(requestID string) (string, error) {
	dir, err := f.batchDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, strings.ToLower(requestID)+".json"), nil
}

func publishBatchRecord(path string, record batchJournalRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	envelope, err := json.Marshal(batchJournalEnvelope{
		Record: raw, SHA256: Digest(raw),
	})
	if err != nil {
		return err
	}
	if len(envelope) > maxBatchJournalBytes {
		return fault.New(fault.CodeQuotaExceeded, "workspace batch recovery record exceeds its size limit", false, "reduce the batch size")
	}
	return safeio.PublishPrivate(path, envelope, true)
}

func loadBatchRecord(path string) (batchJournalRecord, error) {
	var record batchJournalRecord
	info, err := os.Stat(path)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxBatchJournalBytes {
		return record, errors.New("workspace batch recovery record is invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	var envelope batchJournalEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Record) == 0 || Digest(envelope.Record) != envelope.SHA256 {
		return record, errors.New("workspace batch recovery record integrity check failed")
	}
	if err := json.Unmarshal(envelope.Record, &record); err != nil {
		return record, errors.New("workspace batch recovery record is invalid")
	}
	if record.Version != batchJournalVersion || !batchRequestIDPattern.MatchString(record.RequestID) ||
		record.RequestSHA256 == "" || record.OperationID == "" || len(record.Steps) == 0 {
		return batchJournalRecord{}, errors.New("workspace batch recovery record is invalid")
	}
	return record, nil
}

func (f *Files) batchRecord(requestID string) (batchJournalRecord, string, bool, error) {
	path, err := f.batchRecordPath(requestID)
	if err != nil {
		return batchJournalRecord{}, "", false, err
	}
	record, err := loadBatchRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return batchJournalRecord{}, path, false, nil
	}
	return record, path, err == nil, err
}

func (f *Files) publishBatchTerminal(path string, record batchJournalRecord) error {
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := publishBatchRecord(path, record); err != nil {
		return err
	}
	return f.pruneBatchRecords()
}

func (f *Files) pruneBatchRecords() error {
	dir, err := f.batchDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type candidate struct {
		path string
		when time.Time
		size int64
	}
	terminal := []candidate{}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		record, err := loadBatchRecord(path)
		if err != nil {
			return err
		}
		if record.State == batchStateInProgress {
			continue
		}
		when, _ := time.Parse(time.RFC3339Nano, record.UpdatedAt)
		terminal = append(terminal, candidate{path: path, when: when, size: info.Size()})
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].when.Before(terminal[j].when) })
	for len(terminal) > 0 && (len(terminal) > maxBatchJournalFiles || total > maxBatchJournalTotal) {
		item := terminal[0]
		terminal = terminal[1:]
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		total -= item.size
	}
	if directory, err := os.Open(dir); err == nil {
		defer directory.Close()
		return directory.Sync()
	}
	return nil
}

func (f *Files) normalizeBatch(requestID string, operations []BatchOperation) (string, []BatchOperation, string, error) {
	if !batchRequestIDPattern.MatchString(requestID) {
		return "", nil, "", fault.New(fault.CodeInvalidInput, "workspace batch request_id must be a UUID", false, "generate a new UUID request_id")
	}
	limit := min(MaxBatchOperations, f.Config.MaxPatchFiles)
	if len(operations) < 1 || len(operations) > limit {
		return "", nil, "", fault.New(fault.CodeInvalidInput, fmt.Sprintf("workspace batch must contain between 1 and %d operations", limit), false, "reduce the batch size")
	}
	normalized := make([]BatchOperation, len(operations))
	used := map[string]bool{}
	usePath := func(path string) (string, error) {
		relative, err := policy.Relative(path)
		if err != nil {
			return "", err
		}
		if relative == "." {
			return "", fault.New(fault.CodeInvalidInput, "workspace batch contains duplicate or conflicting paths", false, "use distinct non-overlapping file paths")
		}
		for existing := range used {
			if relative == existing || strings.HasPrefix(relative, existing+"/") || strings.HasPrefix(existing, relative+"/") {
				return "", fault.New(fault.CodeInvalidInput, "workspace batch contains duplicate or conflicting paths", false, "use distinct non-overlapping file paths")
			}
		}
		used[relative] = true
		return relative, nil
	}
	for index, operation := range operations {
		item := operation
		switch operation.Action {
		case "create":
			path, err := usePath(operation.Path)
			if err != nil {
				return "", nil, "", err
			}
			if operation.ExpectedSHA256 != "missing" {
				return "", nil, "", fault.New(fault.CodeInvalidInput, "batch create requires expected_sha256='missing'", false, "confirm the target is absent")
			}
			if len(operation.Content) > f.Config.MaxWriteBytes {
				return "", nil, "", fault.New(fault.CodeQuotaExceeded, "batch create content exceeds the write limit", false, "reduce the file size")
			}
			item.Path = path
		case "replace":
			path, err := usePath(operation.Path)
			if err != nil {
				return "", nil, "", err
			}
			if !revisionPattern.MatchString(operation.ExpectedSHA256) || operation.Old == "" || operation.ExpectedReplacements < 1 {
				return "", nil, "", fault.New(fault.CodeInvalidInput, "batch replace requires a file digest, non-empty old text, and positive expected_replacements", false, "read the file and correct the replace preconditions")
			}
			item.Path = path
		case "move":
			source, err := usePath(operation.Source)
			if err != nil {
				return "", nil, "", err
			}
			destination, err := usePath(operation.Destination)
			if err != nil {
				return "", nil, "", err
			}
			if !revisionPattern.MatchString(operation.ExpectedSHA256) || operation.ExpectedDestination != "missing" {
				return "", nil, "", fault.New(fault.CodeInvalidInput, "batch move requires the observed source digest and expected_destination='missing'", false, "read the source and confirm the destination is absent")
			}
			item.Source, item.Destination = source, destination
		default:
			return "", nil, "", fault.New(fault.CodeInvalidInput, fmt.Sprintf("batch operation %d has unsupported action", index), false, "use create, replace, or move")
		}
		normalized[index] = item
	}
	requestID = strings.ToLower(requestID)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", nil, "", err
	}
	requestHash := Digest(append([]byte(requestID+"\x00"), encoded...))
	return requestID, normalized, requestHash, nil
}

func pathExists(workspace *policy.Workspace, path string) (bool, error) {
	file, err := workspace.Open(path, unix.O_PATH, 0)
	if err == nil {
		file.Close()
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (f *Files) planBatchLocked(operations []BatchOperation) ([]batchPlanStep, error) {
	steps := make([]batchPlanStep, 0, len(operations))
	for _, operation := range operations {
		switch operation.Action {
		case "create":
			if _, err := f.Policy.Resolve(operation.Path, false); err != nil {
				return nil, err
			}
			exists, err := pathExists(f.Policy, operation.Path)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, fault.New(fault.CodeConflict, "batch create target already exists", false, "re-read the target and submit a new batch")
			}
			data := []byte(operation.Content)
			steps = append(steps, batchPlanStep{
				journal: batchJournalStep{Action: "create", Path: operation.Path, AfterSHA256: Digest(data), Mode: 0600},
				content: data,
			})
		case "replace":
			data, info, err := f.read(operation.Path, f.Config.MaxWriteBytes)
			if err != nil {
				return nil, err
			}
			if Digest(data) != operation.ExpectedSHA256 {
				return nil, fault.New(fault.CodeConflict, "batch replace file changed since it was read", false, "read the file again and submit a new batch")
			}
			if !utf8.Valid(data) {
				return nil, fault.New(fault.CodeInvalidInput, "batch replace target is not valid UTF-8 text", false, "use a supported binary-specific tool")
			}
			actual := strings.Count(string(data), operation.Old)
			if actual != operation.ExpectedReplacements {
				return nil, fault.New(fault.CodeConflict, fmtReplacement(operation.ExpectedReplacements, actual), false, "read the file again and confirm the replacement count")
			}
			updated := []byte(strings.ReplaceAll(string(data), operation.Old, operation.New))
			if len(updated) > f.Config.MaxWriteBytes {
				return nil, fault.New(fault.CodeQuotaExceeded, "batch replace result exceeds the write limit", false, "reduce the resulting file size")
			}
			steps = append(steps, batchPlanStep{
				journal: batchJournalStep{
					Action: "replace", Path: operation.Path, BeforeSHA256: Digest(data),
					AfterSHA256: Digest(updated), Mode: uint32(info.Mode().Perm()),
				},
				content: updated, preimage: data,
			})
		case "move":
			data, info, err := f.read(operation.Source, 64<<20)
			if err != nil {
				return nil, err
			}
			if Digest(data) != operation.ExpectedSHA256 {
				return nil, fault.New(fault.CodeConflict, "batch move source changed since it was read", false, "read the source again and submit a new batch")
			}
			if _, err := f.Policy.Resolve(operation.Destination, false); err != nil {
				return nil, err
			}
			exists, err := pathExists(f.Policy, operation.Destination)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, fault.New(fault.CodeConflict, "batch move destination already exists", false, "choose an absent destination and submit a new batch")
			}
			steps = append(steps, batchPlanStep{
				journal: batchJournalStep{
					Action: "move", Source: operation.Source, Destination: operation.Destination,
					BeforeSHA256: Digest(data), AfterSHA256: Digest(data), Mode: uint32(info.Mode().Perm()),
				},
				preimage: data,
			})
		}
	}

	for index := range steps {
		step := &steps[index]
		switch step.journal.Action {
		case "replace":
			revision, err := f.capture(step.journal.Path, "batch_replace", step.preimage, os.FileMode(step.journal.Mode))
			if err != nil {
				return nil, err
			}
			step.journal.PreviousRevision = revision
		case "move":
			revision, err := f.capture(step.journal.Source, "batch_move", step.preimage, os.FileMode(step.journal.Mode))
			if err != nil {
				return nil, err
			}
			step.journal.PreviousRevision = revision
		}
	}
	return steps, nil
}

func (f *Files) callBatchFault(stage string, index int) error {
	if f.batchFault == nil {
		return nil
	}
	return f.batchFault(stage, index)
}

func (f *Files) applyBatchStep(step batchPlanStep) error {
	switch step.journal.Action {
	case "create":
		return f.Policy.AtomicWrite(step.journal.Path, step.content, os.FileMode(step.journal.Mode), false)
	case "replace":
		return f.Policy.AtomicWrite(step.journal.Path, step.content, os.FileMode(step.journal.Mode), true)
	case "move":
		return f.Policy.Move(step.journal.Source, step.journal.Destination)
	default:
		return errors.New("invalid workspace batch plan")
	}
}

func (f *Files) currentDigest(path string) (bool, string, error) {
	data, _, err := f.read(path, 64<<20)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, Digest(data), nil
}

func (f *Files) rollbackBatchLocked(record batchJournalRecord) error {
	for index := len(record.Steps) - 1; index >= 0; index-- {
		step := record.Steps[index]
		switch step.Action {
		case "create":
			exists, digest, err := f.currentDigest(step.Path)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			if digest != step.AfterSHA256 {
				return errors.New("batch create target changed during recovery")
			}
			if err := f.Policy.RemoveFile(step.Path); err != nil {
				return err
			}
		case "replace":
			exists, digest, err := f.currentDigest(step.Path)
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("batch replace target disappeared during recovery")
			}
			if digest == step.BeforeSHA256 {
				continue
			}
			if digest != step.AfterSHA256 {
				return errors.New("batch replace target changed during recovery")
			}
			metadata, previous, err := f.loadRevision(step.PreviousRevision, step.Path)
			if err != nil {
				return err
			}
			if metadata.SHA256 != step.BeforeSHA256 {
				return errors.New("batch replace recovery revision does not match preimage")
			}
			if err := f.Policy.AtomicWrite(step.Path, previous, os.FileMode(metadata.Mode), true); err != nil {
				return err
			}
		case "move":
			sourceExists, sourceDigest, err := f.currentDigest(step.Source)
			if err != nil {
				return err
			}
			destinationExists, destinationDigest, err := f.currentDigest(step.Destination)
			if err != nil {
				return err
			}
			if sourceExists && sourceDigest == step.BeforeSHA256 && !destinationExists {
				continue
			}
			if !sourceExists && destinationExists && destinationDigest == step.AfterSHA256 {
				if err := f.Policy.Move(step.Destination, step.Source); err != nil {
					return err
				}
				continue
			}
			return errors.New("batch move state is ambiguous during recovery")
		default:
			return errors.New("workspace batch recovery contains an invalid action")
		}
	}
	return nil
}

func (f *Files) replayBatchRecord(record batchJournalRecord) (map[string]any, error) {
	if record.State == batchStateApplied && record.Result != nil {
		return record.Result, nil
	}
	if record.Error != nil {
		return nil, fault.New(fault.Code(record.Error.Code), record.Error.Message, record.Error.Retryable, record.Error.NextAction)
	}
	return nil, fault.New(fault.CodeOutcomeUnknown, "workspace batch has no terminal result", false, "inspect workspace state before attempting another mutation")
}

func (f *Files) reconcileBatchesLocked() error {
	dir, err := f.batchDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		record, err := loadBatchRecord(path)
		if err != nil {
			return fault.New(fault.CodeOutcomeUnknown, "workspace batch recovery state is unreadable; no workspace mutation was attempted", false, "run diagnostics and repair the runtime recovery state")
		}
		if record.State != batchStateInProgress {
			continue
		}
		if err := f.rollbackBatchLocked(record); err != nil {
			record.State = batchStateRecoveryBad
			record.Error = batchError(fault.Detail{
				Code: fault.CodeOutcomeUnknown, Message: "workspace batch recovery could not prove or restore the pre-batch state",
				NextAction: "inspect every affected path before another mutation",
			})
			if publishErr := f.publishBatchTerminal(path, record); publishErr != nil {
				return fault.New(fault.CodeOutcomeUnknown, "workspace batch recovery failed and its terminal state could not be published", false, "run diagnostics and inspect every affected path")
			}
			return fault.New(fault.CodeOutcomeUnknown, record.Error.Message, false, record.Error.NextAction)
		}
		record.State = batchStateRecovered
		record.Error = batchError(fault.Detail{
			Code: fault.CodeFailed, Message: "incomplete workspace batch was rolled back during recovery",
			NextAction: "re-read affected files and submit a new request_id if the batch is still desired",
		})
		if err := f.publishBatchTerminal(path, record); err != nil {
			return fault.New(fault.CodeOutcomeUnknown, "workspace batch was recovered but its terminal state could not be published", false, "run diagnostics before another mutation")
		}
	}
	return nil
}

func (f *Files) Batch(ctx context.Context, requestID string, operations []BatchOperation) (map[string]any, error) {
	requestID, operations, requestHash, err := f.normalizeBatch(requestID, operations)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.reconcileBatchesLocked(); err != nil {
		return nil, err
	}
	existing, path, exists, err := f.batchRecord(requestID)
	if err != nil {
		return nil, err
	}
	if exists {
		if existing.RequestSHA256 != requestHash {
			return nil, fault.New(fault.CodeConflict, "workspace batch request_id was already used for different operations", false, "generate a new request_id")
		}
		return f.replayBatchRecord(existing)
	}

	steps, err := f.planBatchLocked(operations)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	record := batchJournalRecord{
		Version: batchJournalVersion, RequestID: requestID, RequestSHA256: requestHash,
		OperationID: Digest([]byte("workspace-batch\x00" + requestID)), State: batchStateInProgress,
		CreatedAt: now, UpdatedAt: now, Steps: make([]batchJournalStep, len(steps)),
	}
	for index := range steps {
		record.Steps[index] = steps[index].journal
	}
	if err := publishBatchRecord(path, record); err != nil {
		return nil, err
	}

	fail := func(cause error) (map[string]any, error) {
		if rollbackErr := f.rollbackBatchLocked(record); rollbackErr != nil {
			record.State = batchStateRecoveryBad
			record.Error = batchError(fault.Detail{
				Code: fault.CodeOutcomeUnknown, Message: "workspace batch failed and rollback could not prove the pre-batch state",
				NextAction: "inspect every affected path before another mutation",
			})
			_ = f.publishBatchTerminal(path, record)
			return nil, fault.New(fault.CodeOutcomeUnknown, record.Error.Message, false, record.Error.NextAction)
		}
		record.State = batchStateRolledBack
		detail := fault.Describe(cause)
		message := "workspace batch failed and was rolled back"
		if detail.Code == fault.CodeInvalidInput || detail.Code == fault.CodeConflict || detail.Code == fault.CodeQuotaExceeded {
			message += ": " + detail.Message
		}
		record.Error = batchError(fault.Detail{
			Code: fault.CodeFailed, Message: message,
			NextAction: "re-read affected files and submit a new request_id if the batch is still desired",
		})
		if publishErr := f.publishBatchTerminal(path, record); publishErr != nil {
			return nil, fault.New(fault.CodeOutcomeUnknown, "workspace batch was rolled back but its terminal state could not be published", false, "run diagnostics before another mutation")
		}
		return nil, fault.New(fault.CodeFailed, record.Error.Message, false, record.Error.NextAction)
	}

	files := make([]map[string]any, 0, len(steps))
	for index, step := range steps {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := f.callBatchFault("before_apply", index); err != nil {
			return fail(err)
		}
		if err := f.applyBatchStep(step); err != nil {
			return fail(err)
		}
		if err := f.callBatchFault("after_apply", index); err != nil {
			return fail(err)
		}
		item := map[string]any{"action": step.journal.Action, "sha256": step.journal.AfterSHA256}
		switch step.journal.Action {
		case "create", "replace":
			item["path"] = step.journal.Path
		case "move":
			item["source"] = step.journal.Source
			item["destination"] = step.journal.Destination
		}
		if step.journal.PreviousRevision != "" {
			item["previous_revision"] = step.journal.PreviousRevision
		}
		files = append(files, item)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	result := map[string]any{
		"request_id": requestID, "operation_id": record.OperationID,
		"state": batchStateApplied, "files": files,
	}
	record.State = batchStateApplied
	record.Result = result
	if err := f.publishBatchTerminal(path, record); err != nil {
		return fail(err)
	}
	return result, nil
}
