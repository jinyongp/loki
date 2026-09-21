package browser

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"unicode/utf8"

	"loki/internal/fault"
	"loki/internal/integrations/browser/internal/cdp"
)

type pendingDialog struct {
	SessionID     string
	Type          string
	Message       string
	DefaultPrompt string
}

type dialogTracker struct {
	mu         sync.Mutex
	generation uint64
	pending    *pendingDialog
}

func boundedDialogText(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func (d *dialogTracker) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.generation = 0
	d.pending = nil
}

func (d *dialogTracker) Event(event cdp.Event) {
	switch event.Method {
	case "Page.javascriptDialogOpening":
		var payload struct {
			Type          string `json:"type"`
			Message       string `json:"message"`
			DefaultPrompt string `json:"defaultPrompt"`
		}
		if json.Unmarshal(event.Params, &payload) != nil {
			return
		}
		switch payload.Type {
		case "alert", "confirm", "prompt", "beforeunload":
		default:
			return
		}
		d.mu.Lock()
		d.generation++
		d.pending = &pendingDialog{
			SessionID:     event.SessionID,
			Type:          payload.Type,
			Message:       boundedDialogText(payload.Message, 4096),
			DefaultPrompt: boundedDialogText(payload.DefaultPrompt, 4096),
		}
		d.mu.Unlock()
	case "Page.javascriptDialogClosed":
		d.mu.Lock()
		if d.pending != nil && (event.SessionID == "" || d.pending.SessionID == event.SessionID) {
			d.pending = nil
			d.generation++
		}
		d.mu.Unlock()
	}
}

func (d *dialogTracker) Snapshot(sessionID string) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := map[string]any{
		"pending":           false,
		"dialog_generation": d.generation,
	}
	if d.pending == nil || d.pending.SessionID != sessionID {
		return result
	}
	result["pending"] = true
	result["type"] = d.pending.Type
	result["message"] = d.pending.Message
	result["accepts_prompt"] = d.pending.Type == "prompt"
	if d.pending.Type == "prompt" {
		result["default_prompt"] = d.pending.DefaultPrompt
	}
	return result
}

func (d *dialogTracker) Pending(expected uint64, sessionID string) (*pendingDialog, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if expected != d.generation {
		return nil, fault.New(fault.CodeConflict, "browser dialog generation is stale", false, "call browser_observe action=dialog and retry with the current dialog_generation")
	}
	if d.pending == nil || d.pending.SessionID != sessionID {
		return nil, fault.New(fault.CodeConflict, "browser dialog is no longer pending", false, "call browser_observe action=dialog before retrying")
	}
	copy := *d.pending
	return &copy, nil
}

func (d *dialogTracker) Complete(expected uint64, sessionID string) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.generation == expected && d.pending != nil && d.pending.SessionID == sessionID {
		d.pending = nil
		d.generation++
	}
	return d.generation
}

func (d *Driver) observeDialog() map[string]any {
	return d.dialogs.Snapshot(d.sessions[d.target])
}

func (d *Driver) handleDialog(ctx context.Context, args map[string]any) (map[string]any, error) {
	expected, err := generationValue(args, "expected_dialog_generation")
	if err != nil {
		return nil, err
	}
	sessionID := d.sessions[d.target]
	pending, err := d.dialogs.Pending(expected, sessionID)
	if err != nil {
		return nil, err
	}
	accept, ok := args["accept"].(bool)
	if !ok {
		return nil, fault.New(fault.CodeInvalidInput, "accept is required for dialog handling", false, "set accept=true or accept=false")
	}
	promptText := pending.DefaultPrompt
	if raw, exists := args["prompt_text"]; exists {
		value, valid := raw.(string)
		if !valid || utf8.RuneCountInString(value) > 4096 {
			return nil, fault.New(fault.CodeInvalidInput, "prompt_text must be a string of at most 4096 characters", false, "use bounded prompt text")
		}
		if pending.Type != "prompt" {
			return nil, fault.New(fault.CodeInvalidInput, "prompt_text is valid only for prompt dialogs", false, "omit prompt_text for this dialog type")
		}
		if !accept {
			return nil, fault.New(fault.CodeInvalidInput, "prompt_text has no effect when dismissing a dialog", false, "omit prompt_text or accept the prompt")
		}
		promptText = value
	}
	params := map[string]any{"accept": accept}
	if pending.Type == "prompt" {
		params["promptText"] = promptText
	}
	if err = d.client.Call(ctx, sessionID, "Page.handleJavaScriptDialog", params, nil); err != nil {
		return nil, err
	}
	generation := d.dialogs.Complete(expected, sessionID)
	if generation == expected {
		return nil, errors.New("browser dialog state did not advance")
	}
	return map[string]any{
		"dialog_handled":    true,
		"accepted":          accept,
		"type":              pending.Type,
		"dialog_generation": generation,
	}, nil
}
