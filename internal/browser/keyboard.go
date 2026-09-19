package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type browserKey struct {
	Key, Code string
	Virtual   int
}

var namedBrowserKeys = map[string]browserKey{
	"Enter":      {Key: "Enter", Code: "Enter", Virtual: 13},
	"Tab":        {Key: "Tab", Code: "Tab", Virtual: 9},
	"Escape":     {Key: "Escape", Code: "Escape", Virtual: 27},
	"Backspace":  {Key: "Backspace", Code: "Backspace", Virtual: 8},
	"Delete":     {Key: "Delete", Code: "Delete", Virtual: 46},
	"Insert":     {Key: "Insert", Code: "Insert", Virtual: 45},
	"ArrowUp":    {Key: "ArrowUp", Code: "ArrowUp", Virtual: 38},
	"ArrowDown":  {Key: "ArrowDown", Code: "ArrowDown", Virtual: 40},
	"ArrowLeft":  {Key: "ArrowLeft", Code: "ArrowLeft", Virtual: 37},
	"ArrowRight": {Key: "ArrowRight", Code: "ArrowRight", Virtual: 39},
	"PageUp":     {Key: "PageUp", Code: "PageUp", Virtual: 33},
	"PageDown":   {Key: "PageDown", Code: "PageDown", Virtual: 34},
	"Home":       {Key: "Home", Code: "Home", Virtual: 36},
	"End":        {Key: "End", Code: "End", Virtual: 35},
	"Space":      {Key: " ", Code: "Space", Virtual: 32},
	"F1":         {Key: "F1", Code: "F1", Virtual: 112},
	"F2":         {Key: "F2", Code: "F2", Virtual: 113},
	"F3":         {Key: "F3", Code: "F3", Virtual: 114},
	"F4":         {Key: "F4", Code: "F4", Virtual: 115},
	"F5":         {Key: "F5", Code: "F5", Virtual: 116},
	"F6":         {Key: "F6", Code: "F6", Virtual: 117},
	"F7":         {Key: "F7", Code: "F7", Virtual: 118},
	"F8":         {Key: "F8", Code: "F8", Virtual: 119},
	"F9":         {Key: "F9", Code: "F9", Virtual: 120},
	"F10":        {Key: "F10", Code: "F10", Virtual: 121},
	"F11":        {Key: "F11", Code: "F11", Virtual: 122},
	"F12":        {Key: "F12", Code: "F12", Virtual: 123},
}

func parseBrowserKey(value string, modifiers int) (browserKey, string, error) {
	if key, ok := namedBrowserKeys[value]; ok {
		return key, value, nil
	}
	runes := []rune(value)
	if len(runes) != 1 {
		return browserKey{}, "", errors.New("unsupported browser key")
	}
	r := runes[0]
	if r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	switch {
	case r >= 'A' && r <= 'Z':
		upper := r
		keyValue := strings.ToLower(string(upper))
		if modifiers&inputModifierBits["Shift"] != 0 {
			keyValue = string(upper)
		}
		return browserKey{Key: keyValue, Code: "Key" + string(upper), Virtual: int(upper)}, string(upper), nil
	case r >= '0' && r <= '9':
		return browserKey{Key: string(r), Code: "Digit" + string(r), Virtual: int(r)}, string(r), nil
	}
	return browserKey{}, "", errors.New("unsupported browser key")
}

func (d *Driver) dispatchKeyRelease(key browserKey, modifiers int) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return d.client.Call(ctx, d.sessions[d.target], "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": key.Key, "code": key.Code,
		"windowsVirtualKeyCode": key.Virtual, "nativeVirtualKeyCode": key.Virtual,
		"modifiers": modifiers,
	}, nil)
}

func (d *Driver) dispatchKey(ctx context.Context, key browserKey, modifiers int) (err error) {
	params := map[string]any{
		"type": "keyDown", "key": key.Key, "code": key.Code,
		"windowsVirtualKeyCode": key.Virtual, "nativeVirtualKeyCode": key.Virtual,
		"modifiers": modifiers,
	}
	pressed := false
	defer func() {
		if !pressed {
			return
		}
		if releaseErr := d.dispatchKeyRelease(key, modifiers); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release browser key: %w", releaseErr))
		}
	}()
	if err = d.client.Call(ctx, d.sessions[d.target], "Input.dispatchKeyEvent", params, nil); err != nil {
		return err
	}
	pressed = true
	if err = d.client.Call(ctx, d.sessions[d.target], "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": key.Key, "code": key.Code,
		"windowsVirtualKeyCode": key.Virtual, "nativeVirtualKeyCode": key.Virtual,
		"modifiers": modifiers,
	}, nil); err != nil {
		return err
	}
	pressed = false
	return nil
}

func textArgument(args map[string]any) (string, error) {
	text, ok := args["text"].(string)
	if !ok || utf8.RuneCountInString(text) > 65536 || strings.ContainsRune(text, 0) {
		return "", errors.New("text must be a string of at most 65536 characters")
	}
	return text, nil
}

func (d *Driver) prepareEditable(ctx context.Context, index int, selectAll bool) error {
	if _, err := d.element(ctx, index, true); err != nil {
		return err
	}
	script := fmt.Sprintf(`(() => {
 const element=globalThis.__lokiNodes?.[%d];
 if (!element || !element.isConnected) throw new Error('stale element');
 if (element.disabled || element.readOnly) throw new Error('read-only element');
 const editable=element.tagName==='INPUT' || element.tagName==='TEXTAREA' || element.isContentEditable;
 if (!editable) throw new Error('element is not editable');
 element.focus({preventScroll:true});
 if (%t) {
   if (element.tagName==='INPUT' || element.tagName==='TEXTAREA') element.select();
   else {
     const selection=element.ownerDocument.getSelection(),range=element.ownerDocument.createRange();
     range.selectNodeContents(element);selection.removeAllRanges();selection.addRange(range);
   }
 }
 return true;
})()`, index, selectAll)
	var ok bool
	return d.evaluate(ctx, script, &ok)
}

func (d *Driver) fillText(ctx context.Context, args map[string]any) (map[string]any, error) {
	index, err := integer(args, "index", -1, 0, 1000000)
	if err != nil || index < 0 {
		return nil, errors.New("index must identify an editable element from browser_observe action=state")
	}
	text, err := textArgument(args)
	if err != nil {
		return nil, err
	}
	if err = d.prepareEditable(ctx, index, true); err != nil {
		return nil, err
	}
	if text == "" {
		key, _, _ := parseBrowserKey("Backspace", 0)
		if err = d.dispatchKey(ctx, key, 0); err != nil {
			return nil, err
		}
	} else if err = d.client.Call(ctx, d.sessions[d.target], "Input.insertText", map[string]any{"text": text}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"filled": true, "index": index, "characters": utf8.RuneCountInString(text)}, nil
}

func (d *Driver) typeText(ctx context.Context, args map[string]any) (map[string]any, error) {
	index, err := integer(args, "index", -1, 0, 1000000)
	if err != nil || index < 0 {
		return nil, errors.New("index must identify an editable element from browser_observe action=state")
	}
	text, err := textArgument(args)
	if err != nil {
		return nil, err
	}
	if err = d.prepareEditable(ctx, index, false); err != nil {
		return nil, err
	}
	if text != "" {
		if err = d.client.Call(ctx, d.sessions[d.target], "Input.insertText", map[string]any{"text": text}, nil); err != nil {
			return nil, err
		}
	}
	return map[string]any{"typed": true, "index": index, "characters": utf8.RuneCountInString(text)}, nil
}

func (d *Driver) keyInput(ctx context.Context, args map[string]any, requireModifier bool) (map[string]any, error) {
	value, ok := args["key"].(string)
	if !ok || value == "" {
		return nil, errors.New("key is required")
	}
	modifierMask, modifiers, err := inputModifiers(args)
	if err != nil {
		return nil, err
	}
	if requireModifier && len(modifiers) == 0 {
		return nil, errors.New("shortcut requires at least one modifier")
	}
	key, canonical, err := parseBrowserKey(value, modifierMask)
	if err != nil {
		return nil, err
	}
	if err = d.dispatchKey(ctx, key, modifierMask); err != nil {
		return nil, err
	}
	if requireModifier {
		return map[string]any{"shortcut": map[string]any{"key": canonical, "modifiers": modifiers}}, nil
	}
	return map[string]any{"key": canonical, "modifiers": modifiers}, nil
}
