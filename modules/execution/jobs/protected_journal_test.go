package jobs

import (
	"loki/internal/platform/privateio"
	"strings"
	"testing"
	"time"
)

func TestProtectedBindingAndMaskedPrefixSurviveJournalReopen(t *testing.T) {
	dir := privateJournalDir(t)
	journal := testJournal(t, dir)
	now := time.Now().UTC()
	id, err := JobIDForRequestID(testRequestID())
	if err != nil {
		t.Fatal(err)
	}
	deployment := "deployment-" + strings.Repeat("b", 32)
	if _, _, err := journal.AdmitRequest(id, "oci:"+strings.Repeat("c", 64)+":"+strings.Repeat("d", 64), testRequestID(), strings.Repeat("e", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.BindProtectedJob(id, deployment, "fixture", "common", now); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.BindInstance(id, testInstanceRef(), now); err != nil {
		t.Fatal(err)
	}
	prepared := privateio.PreparedJob{Stage: "prepared", ExecutionID: strings.Repeat("f", 32), InputID: strings.Repeat("0", 32), RunnerID: testRequestID(), RunnerInstanceID: testRequestID(), Revision: 1, PrepareRequestID: privateio.OperationID(deployment, id, "prepare"), ApproveRequestID: privateio.OperationID(deployment, id, "approve"), DeliverRequestID: privateio.OperationID(deployment, id, "deliver")}
	if _, err := journal.BindProtectedInput(id, prepared, now); err != nil {
		t.Fatal(err)
	}
	prepared.Stage = "approved"
	prepared.Revision = 2
	if _, err := journal.BindProtectedInput(id, prepared, now); err != nil {
		t.Fatal(err)
	}
	changed := prepared
	changed.InputID = strings.Repeat("1", 32)
	if _, err := journal.BindProtectedInput(id, changed, now); err == nil {
		t.Fatal("frozen input replacement accepted")
	}
	if err := journal.AppendMaskedOutput(id, []byte("safe [REDACTED] prefix"), now); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := testJournal(t, dir)
	record, ok, err := reopened.Get(id)
	if err != nil || !ok || record.Protected == nil || record.Protected.Prepared == nil || record.Protected.Prepared.Revision != 2 || string(record.Protected.Output) != "safe [REDACTED] prefix" || record.Protected.Complete {
		t.Fatal("durable protected binding or prefix lost")
	}
	record.Protected.Output[0] = 'X'
	again, _, _ := reopened.Get(id)
	if string(again.Protected.Output) != "safe [REDACTED] prefix" {
		t.Fatal("caller mutated canonical output")
	}
}
