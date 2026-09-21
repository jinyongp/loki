package browser

import (
	"encoding/json"
	"strings"
	"testing"

	"loki/internal/fault"
	"loki/internal/integrations/browser/internal/cdp"
)

func TestDialogTrackerLifecycle(t *testing.T) {
	var tracker dialogTracker
	message := strings.Repeat("x", 5000)
	payload, err := json.Marshal(map[string]any{
		"type": "prompt", "message": message, "defaultPrompt": "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	tracker.Event(cdp.Event{Method: "Page.javascriptDialogOpening", Params: payload, SessionID: "session"})
	snapshot := tracker.Snapshot("session")
	if snapshot["pending"] != true || snapshot["type"] != "prompt" || snapshot["accepts_prompt"] != true ||
		snapshot["dialog_generation"] != uint64(1) || len([]rune(snapshot["message"].(string))) != 4096 {
		t.Fatalf("dialog snapshot = %#v", snapshot)
	}
	pending, err := tracker.Pending(1, "session")
	if err != nil || pending.Type != "prompt" {
		t.Fatalf("pending dialog = %#v %v", pending, err)
	}
	if _, err = tracker.Pending(0, "session"); err == nil || fault.Describe(err).Code != fault.CodeConflict {
		t.Fatalf("stale dialog error = %#v", fault.Describe(err))
	}

	tracker.Event(cdp.Event{Method: "Page.javascriptDialogClosed", Params: json.RawMessage([]byte("{}")), SessionID: "session"})
	snapshot = tracker.Snapshot("session")
	if snapshot["pending"] != false || snapshot["dialog_generation"] != uint64(2) {
		t.Fatalf("closed dialog snapshot = %#v", snapshot)
	}
	if _, err = tracker.Pending(2, "session"); err == nil || fault.Describe(err).Code != fault.CodeConflict {
		t.Fatalf("closed dialog error = %#v", fault.Describe(err))
	}
}

func TestDialogTrackerIgnoresUnknownAndOtherSession(t *testing.T) {
	var tracker dialogTracker
	tracker.Event(cdp.Event{
		Method:    "Page.javascriptDialogOpening",
		Params:    json.RawMessage([]byte("{\"type\":\"unknown\",\"message\":\"x\"}")),
		SessionID: "one",
	})
	if snapshot := tracker.Snapshot("one"); snapshot["dialog_generation"] != uint64(0) || snapshot["pending"] != false {
		t.Fatalf("unknown dialog changed state: %#v", snapshot)
	}
	tracker.Event(cdp.Event{
		Method:    "Page.javascriptDialogOpening",
		Params:    json.RawMessage([]byte("{\"type\":\"confirm\",\"message\":\"x\"}")),
		SessionID: "one",
	})
	if snapshot := tracker.Snapshot("two"); snapshot["pending"] != false || snapshot["dialog_generation"] != uint64(1) {
		t.Fatalf("other session sees dialog: %#v", snapshot)
	}
	if generation := tracker.Complete(1, "two"); generation != 1 {
		t.Fatalf("other session completed dialog: %d", generation)
	}
	if snapshot := tracker.Snapshot("one"); snapshot["pending"] != true {
		t.Fatalf("dialog was lost: %#v", snapshot)
	}
}
