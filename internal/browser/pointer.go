package browser

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	maxPointerCoordinate = 16384
	maxWheelDelta        = 10000
	maxDragSteps         = 100
	maxDragDurationMS    = 5000
)

type pointerPoint struct {
	X, Y  float64
	Index *int
	Href  string
}

var inputModifierBits = map[string]int{
	"Alt":     1,
	"Control": 2,
	"Meta":    4,
	"Shift":   8,
}

var pointerButtonBits = map[string]int{
	"left":   1,
	"right":  2,
	"middle": 4,
}

func inputModifiers(args map[string]any) (int, []string, error) {
	raw, ok := args["modifiers"]
	if !ok || raw == nil {
		return 0, []string{}, nil
	}
	values := []string{}
	switch typed := raw.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return 0, nil, errors.New("modifiers must contain only supported modifier names")
			}
			values = append(values, value)
		}
	default:
		return 0, nil, errors.New("modifiers must be an array")
	}
	if len(values) > 4 {
		return 0, nil, errors.New("at most four modifiers are supported")
	}
	seen := map[string]bool{}
	for _, value := range values {
		if inputModifierBits[value] == 0 {
			return 0, nil, errors.New("unsupported pointer modifier")
		}
		if seen[value] {
			return 0, nil, errors.New("duplicate pointer modifier")
		}
		seen[value] = true
	}
	mask := 0
	canonical := []string{}
	for _, value := range []string{"Alt", "Control", "Meta", "Shift"} {
		if seen[value] {
			mask |= inputModifierBits[value]
			canonical = append(canonical, value)
		}
	}
	return mask, canonical, nil
}

func pointerButton(args map[string]any) (string, int, error) {
	button := "left"
	if raw, ok := args["button"]; ok && raw != nil {
		var valid bool
		button, valid = raw.(string)
		if !valid {
			return "", 0, errors.New("button must be left, middle, or right")
		}
	}
	bit := pointerButtonBits[button]
	if bit == 0 {
		return "", 0, errors.New("button must be left, middle, or right")
	}
	return button, bit, nil
}

func pointerCoordinates(args map[string]any, xKey, yKey string) (pointerPoint, error) {
	if args[xKey] == nil || args[yKey] == nil {
		return pointerPoint{}, fmt.Errorf("%s and %s are required together", xKey, yKey)
	}
	x, err := integer(args, xKey, 0, 0, maxPointerCoordinate)
	if err != nil {
		return pointerPoint{}, err
	}
	y, err := integer(args, yKey, 0, 0, maxPointerCoordinate)
	if err != nil {
		return pointerPoint{}, err
	}
	return pointerPoint{X: float64(x), Y: float64(y)}, nil
}

func (d *Driver) pointerElement(ctx context.Context, args map[string]any, key string, scroll bool) (pointerPoint, error) {
	index, err := integer(args, key, -1, 0, 1000000)
	if err != nil || index < 0 {
		return pointerPoint{}, fmt.Errorf("%s must identify an element from browser_observe action=state", key)
	}
	element, err := d.element(ctx, index, scroll)
	if err != nil {
		return pointerPoint{}, err
	}
	return pointerPoint{X: element["x"].(float64), Y: element["y"].(float64), Index: &index, Href: stringValue(element["href"])}, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func pointerResult(point pointerPoint) map[string]any {
	if point.Index != nil {
		return map[string]any{"index": *point.Index}
	}
	return map[string]any{"x": point.X, "y": point.Y}
}

func (d *Driver) dispatchPointer(ctx context.Context, eventType string, point pointerPoint, button string, buttons, clickCount, modifiers int, extra map[string]any) error {
	params := map[string]any{
		"type": eventType,
		"x":    point.X,
		"y":    point.Y,
	}
	if modifiers != 0 {
		params["modifiers"] = modifiers
	}
	if buttons != 0 || eventType == "mouseReleased" {
		params["buttons"] = buttons
	}
	if button != "" {
		params["button"] = button
	}
	if clickCount > 0 {
		params["clickCount"] = clickCount
	}
	for key, value := range extra {
		params[key] = value
	}
	return d.client.Call(ctx, d.sessions[d.target], "Input.dispatchMouseEvent", params, nil)
}

func (d *Driver) releasePointerCleanup(point pointerPoint, button string, clickCount, modifiers int) error {
	releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return d.dispatchPointer(releaseCtx, "mouseReleased", point, button, 0, clickCount, modifiers, nil)
}

func (d *Driver) click(ctx context.Context, args map[string]any) (result map[string]any, err error) {
	var point pointerPoint
	if args["index"] != nil {
		if args["x"] != nil || args["y"] != nil {
			return nil, errors.New("provide an element index or coordinates, not both")
		}
		point, err = d.pointerElement(ctx, args, "index", true)
	} else {
		point, err = pointerCoordinates(args, "x", "y")
	}
	if err != nil {
		return nil, err
	}

	button, buttons, err := pointerButton(args)
	if err != nil {
		return nil, err
	}
	clickCount, err := integer(args, "click_count", 1, 1, 3)
	if err != nil {
		return nil, err
	}
	modifierMask, modifiers, err := inputModifiers(args)
	if err != nil {
		return nil, err
	}

	newTab := args["new_tab"] == true
	if newTab {
		if point.Index == nil {
			return nil, errors.New("new_tab requires an element-index click")
		}
		if button != "left" || clickCount != 1 || modifierMask != 0 {
			return nil, errors.New("new_tab cannot be combined with button, click_count, or modifiers")
		}
		if point.Href == "" {
			return nil, errors.New("new_tab requires an element with an href")
		}
		if _, err = d.navigate(ctx, point.Href, true); err != nil {
			return nil, err
		}
	} else {
		if err = d.dispatchPointer(ctx, "mouseMoved", point, "", 0, 0, modifierMask, nil); err != nil {
			return nil, err
		}
		pressed := false
		defer func() {
			if !pressed {
				return
			}
			if releaseErr := d.releasePointerCleanup(point, button, clickCount, modifierMask); releaseErr != nil {
				err = errors.Join(err, fmt.Errorf("release clicked pointer: %w", releaseErr))
			}
		}()
		if err = d.dispatchPointer(ctx, "mousePressed", point, button, buttons, clickCount, modifierMask, nil); err != nil {
			return nil, err
		}
		pressed = true
		if err = d.dispatchPointer(ctx, "mouseReleased", point, button, 0, clickCount, modifierMask, nil); err != nil {
			return nil, err
		}
		pressed = false
	}
	return map[string]any{
		"clicked": pointerResult(point), "button": button, "click_count": clickCount,
		"modifiers": modifiers, "new_tab": newTab,
	}, nil
}

func (d *Driver) hover(ctx context.Context, args map[string]any) (map[string]any, error) {
	var point pointerPoint
	var err error
	if args["index"] != nil {
		point, err = d.pointerElement(ctx, args, "index", true)
	} else {
		point, err = pointerCoordinates(args, "x", "y")
	}
	if err != nil {
		return nil, err
	}
	if err = d.dispatchPointer(ctx, "mouseMoved", point, "", 0, 0, 0, nil); err != nil {
		return nil, err
	}
	return map[string]any{"hovered": pointerResult(point)}, nil
}

func waitPointerStep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *Driver) drag(ctx context.Context, args map[string]any) (result map[string]any, err error) {
	var from, to pointerPoint
	if args["source_index"] != nil {
		from, err = d.pointerElement(ctx, args, "source_index", true)
	} else {
		from, err = pointerCoordinates(args, "from_x", "from_y")
	}
	if err != nil {
		return nil, err
	}
	if args["target_index"] != nil {
		to, err = d.pointerElement(ctx, args, "target_index", false)
	} else {
		to, err = pointerCoordinates(args, "to_x", "to_y")
	}
	if err != nil {
		return nil, err
	}

	steps, err := integer(args, "steps", 12, 1, maxDragSteps)
	if err != nil {
		return nil, err
	}
	durationMS, err := integer(args, "duration_ms", 250, 0, maxDragDurationMS)
	if err != nil {
		return nil, err
	}
	stepDelay := time.Duration(0)
	if durationMS > 0 {
		stepDelay = time.Duration(durationMS) * time.Millisecond / time.Duration(steps)
	}

	current := from
	pressed := false
	defer func() {
		if !pressed {
			return
		}
		if releaseErr := d.releasePointerCleanup(current, "left", 1, 0); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release dragged pointer: %w", releaseErr))
		}
	}()

	if err = d.dispatchPointer(ctx, "mouseMoved", from, "", 0, 0, 0, nil); err != nil {
		return nil, err
	}
	if err = d.dispatchPointer(ctx, "mousePressed", from, "left", pointerButtonBits["left"], 1, 0, nil); err != nil {
		return nil, err
	}
	pressed = true
	for step := 1; step <= steps; step++ {
		ratio := float64(step) / float64(steps)
		current = pointerPoint{
			X: from.X + (to.X-from.X)*ratio,
			Y: from.Y + (to.Y-from.Y)*ratio,
		}
		if err = d.dispatchPointer(ctx, "mouseMoved", current, "left", pointerButtonBits["left"], 0, 0, nil); err != nil {
			return nil, err
		}
		if step < steps {
			if err = waitPointerStep(ctx, stepDelay); err != nil {
				return nil, err
			}
		}
	}
	if err = d.dispatchPointer(ctx, "mouseReleased", to, "left", 0, 1, 0, nil); err != nil {
		return nil, err
	}
	pressed = false

	dragged := map[string]any{
		"from_x": from.X, "from_y": from.Y, "to_x": to.X, "to_y": to.Y,
		"steps": steps, "duration_ms": durationMS,
	}
	if from.Index != nil {
		dragged["source_index"] = *from.Index
	}
	if to.Index != nil {
		dragged["target_index"] = *to.Index
	}
	return map[string]any{"dragged": dragged}, nil
}

func (d *Driver) wheel(ctx context.Context, args map[string]any) (map[string]any, error) {
	deltaX, err := integer(args, "delta_x", 0, -maxWheelDelta, maxWheelDelta)
	if err != nil {
		return nil, err
	}
	deltaY, err := integer(args, "delta_y", 0, -maxWheelDelta, maxWheelDelta)
	if err != nil {
		return nil, err
	}
	if deltaX == 0 && deltaY == 0 {
		return nil, errors.New("wheel requires a non-zero delta_x or delta_y")
	}

	var point pointerPoint
	if args["index"] != nil {
		point, err = d.pointerElement(ctx, args, "index", true)
	} else if args["x"] != nil || args["y"] != nil {
		point, err = pointerCoordinates(args, "x", "y")
	} else {
		var center struct{ X, Y float64 }
		if err = d.evaluate(ctx, "({x:innerWidth/2,y:innerHeight/2})", &center); err == nil {
			point = pointerPoint{X: center.X, Y: center.Y}
		}
	}
	if err != nil {
		return nil, err
	}
	if err = d.dispatchPointer(ctx, "mouseWheel", point, "", 0, 0, 0, map[string]any{
		"deltaX": deltaX, "deltaY": deltaY,
	}); err != nil {
		return nil, err
	}
	wheel := map[string]any{
		"x": point.X, "y": point.Y, "delta_x": deltaX, "delta_y": deltaY,
	}
	if point.Index != nil {
		wheel["index"] = *point.Index
	}
	return map[string]any{"wheel": wheel}, nil
}
