package browser

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"

	"loki/internal/fault"
)

//go:embed state.js
var stateScript string

func integer(args map[string]any, key string, fallback, min, max int) (int, error) {
	v, ok := args[key]
	if !ok {
		return fallback, nil
	}
	var n int
	switch v := v.(type) {
	case int:
		n = v
	case int64:
		if v < int64(min) || v > int64(max) {
			return 0, fmt.Errorf("%s is out of range", key)
		}
		n = int(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v < float64(min) || v > float64(max) {
			return 0, fmt.Errorf("%s must be an integer in range", key)
		}
		n = int(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil || parsed < int64(min) || parsed > int64(max) {
			return 0, fmt.Errorf("invalid %s", key)
		}
		n = int(parsed)
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s is out of range", key)
	}
	return n, nil
}

const maxBrowserGeneration = uint64(1<<53 - 1)

func generationValue(args map[string]any, key string) (uint64, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, fault.New(fault.CodeInvalidInput, key+" is required for this browser interaction", false, "observe the browser again and supply the returned generation")
	}
	var generation uint64
	switch typed := value.(type) {
	case int:
		if typed < 0 {
			return 0, fault.New(fault.CodeInvalidInput, key+" must be a non-negative integer", false, "use the generation returned by browser_observe")
		}
		generation = uint64(typed)
	case int64:
		if typed < 0 {
			return 0, fault.New(fault.CodeInvalidInput, key+" must be a non-negative integer", false, "use the generation returned by browser_observe")
		}
		generation = uint64(typed)
	case uint64:
		generation = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed < 0 || typed > float64(maxBrowserGeneration) {
			return 0, fault.New(fault.CodeInvalidInput, key+" must be a safe non-negative integer", false, "use the generation returned by browser_observe")
		}
		generation = uint64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 0 {
			return 0, fault.New(fault.CodeInvalidInput, key+" must be a non-negative integer", false, "use the generation returned by browser_observe")
		}
		generation = uint64(parsed)
	default:
		return 0, fault.New(fault.CodeInvalidInput, key+" must be a non-negative integer", false, "use the generation returned by browser_observe")
	}
	if generation > maxBrowserGeneration {
		return 0, fault.New(fault.CodeInvalidInput, key+" exceeds the supported generation range", false, "use the generation returned by browser_observe")
	}
	return generation, nil
}

func (d *Driver) requireInteractionGeneration(args map[string]any, requireState bool) error {
	expected, err := generationValue(args, "expected_browser_generation")
	if err != nil {
		return err
	}
	if expected != d.generation {
		return fault.New(fault.CodeConflict, "browser generation is stale", false, "call browser_observe action=state or action=tabs and retry with the current browser_generation")
	}
	if !requireState {
		return nil
	}
	expectedState, err := generationValue(args, "expected_state_generation")
	if err != nil {
		return err
	}
	if expectedState != d.stateGeneration {
		return fault.New(fault.CodeConflict, "browser state snapshot is stale", false, "call browser_observe action=state and retry with its current state_generation")
	}
	return nil
}

func (d *Driver) finishInteraction(startGeneration uint64, result map[string]any, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	if d.generation == startGeneration {
		d.generation++
	}
	d.stateGeneration++
	result["browser_generation"] = d.generation
	return result, nil
}

func (d *Driver) state(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	if err := d.evaluate(ctx, stateScript, &result); err != nil {
		return nil, err
	}
	tabs, err := d.tabList(ctx)
	if err != nil {
		return nil, err
	}
	d.stateGeneration++
	result["tabs"] = tabs["tabs"]
	result["active_tab_id"] = shortID(d.target)
	result["browser_generation"] = d.generation
	result["state_generation"] = d.stateGeneration
	return result, nil
}
func (d *Driver) element(ctx context.Context, index int, scroll bool) (map[string]any, error) {
	// Integer interpolation and the boolean are trusted; user text never becomes JavaScript.
	script := fmt.Sprintf(`(() => {
 const element = globalThis.__lokiNodes?.[%d];
 if (!element || !element.isConnected) throw new Error('stale element');
 const shouldScroll = %t;
 if (shouldScroll) element.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
 const rect=element.getBoundingClientRect();
 if (!rect.width || !rect.height) throw new Error('element is hidden');
 let x=rect.x+rect.width/2,y=rect.y+rect.height/2,view=element.ownerDocument.defaultView;
 while (view !== window) {
   const frame=view.frameElement;
   if (!frame) throw new Error('frame is unavailable');
   if (shouldScroll) frame.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
   const bounds=frame.getBoundingClientRect(); x+=bounds.x+frame.clientLeft; y+=bounds.y+frame.clientTop;
   view=frame.ownerDocument.defaultView;
 }
 if (x < 0 || y < 0 || x > window.innerWidth || y > window.innerHeight) throw new Error('element is outside viewport');
 return {x,y,href:element.href || null};
})()`, index, scroll)
	var result map[string]any
	err := d.evaluate(ctx, script, &result)
	return result, err
}
func (d *Driver) screenshot(ctx context.Context, full bool) (map[string]any, error) {
	params := map[string]any{"format": "png", "captureBeyondViewport": full, "fromSurface": true}
	if full {
		var metrics struct {
			CSSContentSize struct{ Width, Height float64 }
		}
		if err := d.client.Call(ctx, d.sessions[d.target], "Page.getLayoutMetrics", nil, &metrics); err != nil {
			return nil, err
		}
		width, height := metrics.CSSContentSize.Width, metrics.CSSContentSize.Height
		if width < 1 || height < 1 || width*height > 100000000 {
			return nil, errors.New("screenshot dimensions exceed limit")
		}
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": width, "height": height, "scale": 1}
	}
	var result struct{ Data string }
	if err := d.client.Call(ctx, d.sessions[d.target], "Page.captureScreenshot", params, &result); err != nil {
		return nil, err
	}
	if len(result.Data) > base64.StdEncoding.EncodedLen(10485760) {
		return nil, errors.New("screenshot exceeds 10 MiB")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(result.Data)
	if err != nil || len(data) > 10485760 {
		return nil, errors.New("invalid or oversized screenshot")
	}
	if _, err = png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return nil, errors.New("browser returned an invalid PNG")
	}
	return map[string]any{"mime_type": "image/png", "bytes": len(data), "data_base64": result.Data}, nil
}
