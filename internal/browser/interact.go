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
	"strings"
	"unicode/utf8"
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
func (d *Driver) element(ctx context.Context, index int, typing bool) (map[string]any, error) {
	// Integer interpolation is trusted; user text never becomes JavaScript.
	script := fmt.Sprintf(`(() => {
 const element = globalThis.__lokiNodes?.[%d];
 if (!element || !element.isConnected) throw new Error('stale element');
 element.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
 if (%t) {
   if (element.disabled || element.readOnly) throw new Error('read-only element');
   element.focus();
   if (element.tagName === 'INPUT' || element.tagName === 'TEXTAREA') element.select();
   else if (element.isContentEditable) {
     const selection=element.ownerDocument.getSelection(), range=element.ownerDocument.createRange();
     range.selectNodeContents(element); selection.removeAllRanges(); selection.addRange(range);
   } else throw new Error('element is not editable');
 }
 const rect=element.getBoundingClientRect();
 if (!rect.width || !rect.height) throw new Error('element is hidden');
 let x=rect.x+rect.width/2,y=rect.y+rect.height/2,view=element.ownerDocument.defaultView;
 while (view !== window) {
   const frame=view.frameElement;
   if (!frame) throw new Error('frame is unavailable');
   frame.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
   const bounds=frame.getBoundingClientRect(); x+=bounds.x+frame.clientLeft; y+=bounds.y+frame.clientTop;
   view=frame.ownerDocument.defaultView;
 }
 return {x,y,href:element.href || null};
})()`, index, typing)
	var result map[string]any
	err := d.evaluate(ctx, script, &result)
	return result, err
}
func (d *Driver) mouse(ctx context.Context, x, y float64) error {
	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		args := map[string]any{"type": kind, "x": x, "y": y}
		if kind != "mouseMoved" {
			args["button"] = "left"
			args["clickCount"] = 1
		}
		if err := d.client.Call(ctx, d.sessions[d.target], "Input.dispatchMouseEvent", args, nil); err != nil {
			return err
		}
	}
	return nil
}
func (d *Driver) click(ctx context.Context, args map[string]any) (map[string]any, error) {
	if args["index"] != nil {
		if args["x"] != nil || args["y"] != nil {
			return nil, errors.New("provide an element index or coordinates, not both")
		}
		index, err := integer(args, "index", -1, 0, 1000000)
		if err != nil {
			return nil, err
		}
		element, err := d.element(ctx, index, false)
		if err != nil {
			return nil, err
		}
		if href, ok := element["href"].(string); args["new_tab"] == true && ok && href != "" {
			if _, err = d.navigate(ctx, href, true); err != nil {
				return nil, err
			}
		} else if err = d.mouse(ctx, element["x"].(float64), element["y"].(float64)); err != nil {
			return nil, err
		}
		return map[string]any{"clicked": map[string]any{"index": index}, "new_tab": args["new_tab"] == true}, nil
	}
	if args["x"] == nil || args["y"] == nil {
		return nil, errors.New("provide index or both x and y")
	}
	x, err := integer(args, "x", 0, 0, 16384)
	if err != nil {
		return nil, err
	}
	y, err := integer(args, "y", 0, 0, 16384)
	if err != nil {
		return nil, err
	}
	if err = d.mouse(ctx, float64(x), float64(y)); err != nil {
		return nil, err
	}
	return map[string]any{"clicked": map[string]any{"x": x, "y": y}, "new_tab": false}, nil
}
func (d *Driver) typeText(ctx context.Context, args map[string]any) (map[string]any, error) {
	index, err := integer(args, "index", -1, 0, 1000000)
	if err != nil || index < 0 {
		return nil, errors.New("index must identify an element from browser_state")
	}
	text, ok := args["text"].(string)
	if !ok || utf8.RuneCountInString(text) > 65536 || strings.ContainsRune(text, 0) {
		return nil, errors.New("text must be a string of at most 65536 characters")
	}
	if _, err = d.element(ctx, index, true); err != nil {
		return nil, err
	}
	// insertText alone does not delete the selection when text is empty.
	if text == "" {
		if _, err = d.press(ctx, "Backspace"); err != nil {
			return nil, err
		}
	} else if err = d.client.Call(ctx, d.sessions[d.target], "Input.insertText", map[string]any{"text": text}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"typed": true, "index": index, "characters": utf8.RuneCountInString(text)}, nil
}

var keys = map[string]int{"Enter": 13, "Tab": 9, "Escape": 27, "Backspace": 8, "Delete": 46, "ArrowUp": 38, "ArrowDown": 40, "ArrowLeft": 37, "ArrowRight": 39, "PageUp": 33, "PageDown": 34, "Home": 36, "End": 35, "Space": 32}

func (d *Driver) press(ctx context.Context, key string) (map[string]any, error) {
	code, ok := keys[key]
	if !ok {
		return nil, errors.New("unsupported key")
	}
	for _, kind := range []string{"keyDown", "keyUp"} {
		keyValue := key
		if key == "Space" {
			keyValue = " "
		}
		params := map[string]any{"type": kind, "key": keyValue, "code": key, "windowsVirtualKeyCode": code}
		if err := d.client.Call(ctx, d.sessions[d.target], "Input.dispatchKeyEvent", params, nil); err != nil {
			return nil, err
		}
	}
	return map[string]any{"pressed": key}, nil
}
func (d *Driver) scroll(ctx context.Context, args map[string]any) (map[string]any, error) {
	direction := value(args, "direction", "down")
	if direction != "up" && direction != "down" {
		return nil, errors.New("direction must be up or down")
	}
	amount, err := integer(args, "amount", 500, 1, 10000)
	if err != nil {
		return nil, err
	}
	delta := amount
	if direction == "up" {
		delta = -delta
	}
	if err = d.evaluate(ctx, fmt.Sprintf(`window.scrollBy({top:%d,left:0,behavior:'instant'})`, delta), nil); err != nil {
		return nil, err
	}
	return map[string]any{"direction": direction, "amount": amount}, nil
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
