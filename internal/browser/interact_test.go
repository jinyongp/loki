package browser

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/http"
	"testing"

	"loki/internal/fault"
)

func TestInteractionGenerationGuards(t *testing.T) {
	d := &Driver{generation: 7, stateGeneration: 3}

	if err := d.requireInteractionGeneration(map[string]any{
		"expected_browser_generation": 7,
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := d.requireInteractionGeneration(map[string]any{
		"expected_browser_generation": 7,
		"expected_state_generation":   3,
	}, true); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name         string
		args         map[string]any
		requireState bool
		code         fault.Code
	}{
		{
			name: "missing browser generation",
			args: map[string]any{},
			code: fault.CodeInvalidInput,
		},
		{
			name: "stale browser generation",
			args: map[string]any{"expected_browser_generation": 6},
			code: fault.CodeConflict,
		},
		{
			name:         "missing state generation",
			args:         map[string]any{"expected_browser_generation": 7},
			requireState: true,
			code:         fault.CodeInvalidInput,
		},
		{
			name: "stale state generation",
			args: map[string]any{
				"expected_browser_generation": 7,
				"expected_state_generation":   2,
			},
			requireState: true,
			code:         fault.CodeConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := d.requireInteractionGeneration(test.args, test.requireState)
			if err == nil {
				t.Fatal("expected generation error")
			}
			if detail := fault.Describe(err); detail.Code != test.code {
				t.Fatalf("generation error = %#v", detail)
			}
		})
	}
}

func TestFinishInteractionAdvancesAtMostOnce(t *testing.T) {
	d := &Driver{generation: 10, stateGeneration: 4}
	result, err := d.finishInteraction(10, map[string]any{"ok": true}, nil)
	if err != nil || d.generation != 11 || d.stateGeneration != 5 || result["browser_generation"] != uint64(11) {
		t.Fatalf("ordinary interaction = %#v generation=%d state=%d err=%v", result, d.generation, d.stateGeneration, err)
	}
	d.generation = 12
	result, err = d.finishInteraction(11, map[string]any{"ok": true}, nil)
	if err != nil || d.generation != 12 || d.stateGeneration != 6 || result["browser_generation"] != uint64(12) {
		t.Fatalf("pre-advanced interaction = %#v generation=%d state=%d err=%v", result, d.generation, d.stateGeneration, err)
	}
}

func elementIndex(t *testing.T, state map[string]any, name string) int {
	t.Helper()
	for _, raw := range state["interactive_elements"].([]any) {
		element := raw.(map[string]any)
		if element["name"] == name {
			return int(element["index"].(float64))
		}
	}
	t.Fatalf("element %q missing: %v", name, state["interactive_elements"])
	return -1
}
func TestChromiumInteractions(t *testing.T) {
	d, address := chromeDriver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/frame" {
			fmt.Fprint(w, `<button name="framed" onclick="top.document.title='frame clicked'">Frame</button>`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Fixture</title>
<input name="text" value="old"><textarea name="area">old</textarea><div name="editable" contenteditable="true">old</div>
<button name="click" onclick="document.title='clicked'">Click</button><button name="hover" onmouseenter="document.title='hovered'">Hover</button>
<button name="pointer" id="pointer">Pointer</button><button name="drag-source" id="drag-source">Drag</button><button name="drag-target" id="drag-target">Drop</button>
<div id="scroller" style="height:80px;width:240px;overflow:auto"><button name="wheel-target">Wheel</button><div style="height:1000px">Tall nested</div></div>
<a name="link" href="/next">Next</a><input name="hidden" type="hidden"><button name="disabled" disabled>No</button>
<div id="host"></div><iframe src="/frame"></iframe><div style="height:2200px">Tall</div>
<script>
globalThis.__lokiNodes=[document.body];
host.attachShadow({mode:'open'}).innerHTML='<button name="shadow" onclick="document.title=\'shadow clicked\'">Shadow</button>';
pointer.addEventListener('mousedown', event => {
  if (event.button === 2) document.title='right';
  else if (event.button === 1) document.title='middle';
  else if (event.ctrlKey && event.shiftKey) document.title='modified';
});
pointer.addEventListener('dblclick', () => document.title='double');
let dragging=false;
document.getElementById('drag-source').addEventListener('mousedown', () => dragging=true);
document.getElementById('drag-target').addEventListener('mousemove', event => {
  if (dragging && event.buttons) document.title='dragging';
});
document.getElementById('drag-target').addEventListener('mouseup', () => {
  if (dragging) document.title='dragged';
  dragging=false;
});
</script>`)
	}))
	callBrowser(t, d, "start", nil)
	callBrowser(t, d, "navigate", map[string]any{"url": address})

	var state map[string]any
	var browserGeneration, stateGeneration uint64
	observe := func() map[string]any {
		t.Helper()
		state = callBrowser(t, d, "state", nil)
		browserGeneration = state["browser_generation"].(uint64)
		stateGeneration = state["state_generation"].(uint64)
		return state
	}
	interact := func(operation string, args map[string]any, requiresState bool) map[string]any {
		t.Helper()
		if args == nil {
			args = map[string]any{}
		}
		args["expected_browser_generation"] = browserGeneration
		if requiresState {
			args["expected_state_generation"] = stateGeneration
		}
		result := callBrowser(t, d, operation, args)
		browserGeneration = result["browser_generation"].(uint64)
		return result
	}
	observe()
	for _, raw := range state["interactive_elements"].([]any) {
		e := raw.(map[string]any)
		if e["name"] == "hidden" || e["name"] == "disabled" {
			t.Fatal("noninteractive element", e)
		}
	}

	pointerCases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"click_count": 2}, "double"},
		{map[string]any{"button": "right"}, "right"},
		{map[string]any{"button": "middle"}, "middle"},
		{map[string]any{"modifiers": []any{"Control", "Shift"}}, "modified"},
	}
	for _, tc := range pointerCases {
		observe()
		tc.args["index"] = elementIndex(t, state, "pointer")
		interact("click", tc.args, true)
		page, err := d.page(t.Context())
		if err != nil || page["title"] != tc.want {
			t.Fatalf("pointer click %#v => %#v %v", tc.args, page, err)
		}
	}

	observe()
	interact("hover", map[string]any{"index": elementIndex(t, state, "hover")}, true)
	if page, err := d.page(t.Context()); err != nil || page["title"] != "hovered" {
		t.Fatalf("hover => %#v %v", page, err)
	}

	observe()
	interact("drag", map[string]any{
		"source_index": elementIndex(t, state, "drag-source"), "target_index": elementIndex(t, state, "drag-target"),
		"steps": 6, "duration_ms": 0,
	}, true)
	if page, err := d.page(t.Context()); err != nil || page["title"] != "dragged" {
		t.Fatalf("drag => %#v %v", page, err)
	}

	observe()
	interact("wheel", map[string]any{
		"index": elementIndex(t, state, "wheel-target"), "delta_x": 0, "delta_y": 500,
	}, true)
	var nestedScroll float64
	if err := d.evaluate(t.Context(), "document.getElementById('scroller').scrollTop", &nestedScroll); err != nil || nestedScroll <= 0 {
		t.Fatalf("wheel nested scroll = %v %v", nestedScroll, err)
	}

	for _, name := range []string{"text", "area", "editable"} {
		observe()
		index := elementIndex(t, state, name)
		result := interact("type", map[string]any{"index": index, "text": "안녕 '); throw 1; // 😀"}, true)
		if result["typed"] != true {
			t.Fatal(result)
		}
		var contents string
		if err := d.evaluate(t.Context(), fmt.Sprintf(`(() => {const e=globalThis.__lokiNodes[%d];return e.value ?? e.textContent})()`, index), &contents); err != nil || contents != "안녕 '); throw 1; // 😀" {
			t.Fatal(contents, err)
		}
		observe()
		index = elementIndex(t, state, name)
		interact("type", map[string]any{"index": index, "text": ""}, true)
		if err := d.evaluate(t.Context(), fmt.Sprintf(`(() => {const e=globalThis.__lokiNodes[%d];return e.value ?? e.textContent})()`, index), &contents); err != nil || contents != "" {
			t.Fatal(contents, err)
		}
	}

	for _, name := range []string{"click", "shadow", "framed"} {
		observe()
		interact("click", map[string]any{"index": elementIndex(t, state, name)}, true)
		page, err := d.page(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"click": "clicked", "shadow": "shadow clicked", "framed": "frame clicked"}[name]
		if page["title"] != want {
			t.Fatal(name, page)
		}
	}

	observe()
	interact("press", map[string]any{"key": "Home"}, false)
	for _, full := range []bool{false, true} {
		shot := callBrowser(t, d, "screenshot", map[string]any{"full_page": full})
		data, err := base64.StdEncoding.DecodeString(shot["data_base64"].(string))
		if err != nil {
			t.Fatal(err)
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if config.Width < 1 || config.Height < 1 || full && config.Height < 2200 {
			t.Fatal(config)
		}
	}

	observe()
	link := elementIndex(t, state, "link")
	oldBrowserGeneration, oldStateGeneration := browserGeneration, stateGeneration
	interact("click", map[string]any{"index": link, "new_tab": true}, true)
	if tabs := callBrowser(t, d, "list_tabs", nil)["tabs"].([]map[string]any); len(tabs) != 2 {
		t.Fatal(tabs)
	}
	if _, err := d.Call(t.Context(), "click", map[string]any{
		"index": link, "expected_browser_generation": oldBrowserGeneration, "expected_state_generation": oldStateGeneration,
	}); err == nil {
		t.Fatal("accepted index from previous browser generation")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("stale index error = %#v", detail)
	}

	observe()
	for _, args := range []map[string]any{
		{"index": -1, "expected_browser_generation": browserGeneration, "expected_state_generation": stateGeneration},
		{"index": 1.5, "expected_browser_generation": browserGeneration, "expected_state_generation": stateGeneration},
		{"index": 0, "x": 0, "expected_browser_generation": browserGeneration, "expected_state_generation": stateGeneration},
		{"x": -1, "y": 0, "expected_browser_generation": browserGeneration},
	} {
		if _, err := d.Call(t.Context(), "click", args); err == nil {
			t.Fatal(args)
		}
	}
}
