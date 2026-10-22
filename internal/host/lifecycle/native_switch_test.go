package lifecycle

import (
	"strings"
	"testing"
	"time"
)

func TestNativeSwitchRetainsOriginalIntentAcrossPublicationAndReleaseLoss(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	open := func() *OperationJournal {
		j, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	j := open()
	now := time.Now()
	intent := NativeSwitchIntent{SourceRelease: "old", TargetRelease: "new", SourceReceipt: "sha256:" + strings.Repeat("a", 64), TargetReceipt: "sha256:" + strings.Repeat("b", 64), Interrupt: true}
	first, err := j.BeginNativeSwitch(intent, now)
	if err != nil || !first.Valid() || first.RecoveryBackupID != "" {
		t.Fatalf("begin: %+v %v", first, err)
	}
	intent.SourceRelease, intent.SourceReceipt = "new", intent.TargetReceipt
	j = open()
	retry, err := j.BeginNativeSwitch(intent, now)
	if err != nil || retry.ID != first.ID || retry.NativeSwitch.SourceRelease != "old" {
		t.Fatalf("published selection retry: %+v %v", retry, err)
	}
	changed := intent
	changed.Interrupt = false
	if _, err := j.BeginNativeSwitch(changed, now); err == nil {
		t.Fatal("changed interruption choice admitted")
	}
	changed = intent
	changed.SourceReceipt = "sha256:" + strings.Repeat("c", 64)
	if _, err := j.BeginNativeSwitch(changed, now); err == nil {
		t.Fatal("changed release receipt admitted")
	}
	if err := j.AcknowledgeNativeRelease(first.ID); err == nil {
		t.Fatal("release acknowledged before health checkpoint")
	}
	if _, err := j.CompleteNativeSwitch(first.ID, false, now); err != nil {
		t.Fatal(err)
	}
	j = open()
	retry, err = j.BeginNativeSwitch(intent, now)
	if err != nil || retry.ID != first.ID || !retry.State.Terminal() {
		t.Fatalf("lost release response: %+v %v", retry, err)
	}
	if err := j.AcknowledgeNativeRelease(first.ID); err != nil {
		t.Fatal(err)
	}
	next, err := open().BeginNativeSwitch(intent, now)
	if err != nil || next.ID == first.ID {
		t.Fatalf("next independent operation: %+v %v", next, err)
	}
}
