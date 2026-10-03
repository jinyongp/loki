package tools

import "fmt"

type Phase string

const (
	Prepared  Phase = "prepared"
	Staged    Phase = "staged"
	Committed Phase = "committed"
	Aborted   Phase = "aborted"
)

// Operation persists before changing a module's current generation. Owned
// generations can be recovered without deleting external paths or user data.
type Operation struct {
	Schema    int       `json:"schema"`
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Module    ID        `json:"module"`
	Phase     Phase     `json:"phase"`
	Previous  string    `json:"previous,omitempty"`
	Candidate string    `json:"candidate,omitempty"`
	Artifact  *Artifact `json:"artifact,omitempty"`
}

func (o Operation) Validate() error {
	if o.Schema != 1 || !validID(ID(o.ID)) || !validID(o.Module) {
		return fmt.Errorf("operation requires valid schema, ID and module")
	}
	if o.Action != "install" && o.Action != "remove" {
		return fmt.Errorf("invalid operation action %q", o.Action)
	}
	if o.Phase != Prepared && o.Phase != Staged && o.Phase != Committed && o.Phase != Aborted {
		return fmt.Errorf("invalid operation phase %q", o.Phase)
	}
	if o.Previous != "" && !digestPattern.MatchString(o.Previous) {
		return fmt.Errorf("invalid previous generation")
	}
	if o.Action == "install" && !digestPattern.MatchString(o.Candidate) {
		return fmt.Errorf("installation requires a candidate digest")
	}
	if o.Action == "remove" && o.Candidate != "" {
		return fmt.Errorf("removal cannot have a candidate")
	}
	if o.Artifact != nil {
		if err := o.Artifact.Validate(); err != nil {
			return err
		}
		if o.Artifact.Module != o.Module || (o.Action == "remove" && o.Artifact.SHA256 != o.Previous) || (o.Action == "install" && o.Artifact.SHA256 != o.Candidate) {
			return fmt.Errorf("operation artifact differs from its generation")
		}
	}
	return nil
}

func (o Operation) Advance(next Phase) (Operation, error) {
	if err := o.Validate(); err != nil {
		return o, err
	}
	if next == o.Phase {
		return o, nil
	}
	if (o.Phase == Prepared && (next == Staged || next == Aborted)) || (o.Phase == Staged && (next == Committed || next == Aborted)) {
		o.Phase = next
		return o, nil
	}
	return o, fmt.Errorf("invalid operation transition %s -> %s", o.Phase, next)
}
