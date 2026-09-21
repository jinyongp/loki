package browser

import (
	"context"
	"testing"
	"time"
)

func TestPointerOptionParsing(t *testing.T) {
	mask, modifiers, err := inputModifiers(map[string]any{"modifiers": []any{"Shift", "Control"}})
	if err != nil || mask != inputModifierBits["Shift"]|inputModifierBits["Control"] {
		t.Fatalf("modifiers = %d %#v %v", mask, modifiers, err)
	}
	if len(modifiers) != 2 || modifiers[0] != "Control" || modifiers[1] != "Shift" {
		t.Fatalf("canonical modifiers = %#v", modifiers)
	}
	for _, args := range []map[string]any{
		{"modifiers": []any{"Control", "Control"}},
		{"modifiers": []any{"Unsupported"}},
		{"modifiers": "Control"},
	} {
		if _, _, err := inputModifiers(args); err == nil {
			t.Fatalf("accepted modifiers %#v", args)
		}
	}

	for _, tc := range []struct {
		args map[string]any
		want string
		bit  int
	}{
		{args: map[string]any{}, want: "left", bit: 1},
		{args: map[string]any{"button": "middle"}, want: "middle", bit: 4},
		{args: map[string]any{"button": "right"}, want: "right", bit: 2},
	} {
		button, bit, err := pointerButton(tc.args)
		if err != nil || button != tc.want || bit != tc.bit {
			t.Fatalf("button %#v = %q %d %v", tc.args, button, bit, err)
		}
	}
	if _, _, err := pointerButton(map[string]any{"button": "back"}); err == nil {
		t.Fatal("accepted unsupported pointer button")
	}

	point, err := pointerCoordinates(map[string]any{"x": 10, "y": 20}, "x", "y")
	if err != nil || point.X != 10 || point.Y != 20 {
		t.Fatalf("coordinates = %#v %v", point, err)
	}
	for _, args := range []map[string]any{
		{"x": 10},
		{"x": -1, "y": 0},
		{"x": 0, "y": maxPointerCoordinate + 1},
	} {
		if _, err := pointerCoordinates(args, "x", "y"); err == nil {
			t.Fatalf("accepted coordinates %#v", args)
		}
	}
}

func TestPointerStepWaitRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitPointerStep(ctx, time.Second); err == nil {
		t.Fatal("canceled pointer wait succeeded")
	}
	if err := waitPointerStep(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
}
