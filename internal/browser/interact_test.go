package browser

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/http"
	"testing"
)

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
<button name="click" onclick="document.title='clicked'">Click</button><a name="link" href="/next">Next</a>
<input name="hidden" type="hidden"><button name="disabled" disabled>No</button>
<div id="host"></div><iframe src="/frame"></iframe><div style="height:2200px">Tall</div>
<script>
globalThis.__lokiNodes=[document.body];
host.attachShadow({mode:'open'}).innerHTML='<button name="shadow" onclick="document.title=\'shadow clicked\'">Shadow</button>';
</script>`)
	}))
	callBrowser(t, d, "start", nil)
	callBrowser(t, d, "navigate", map[string]any{"url": address})
	state := callBrowser(t, d, "state", nil)
	for _, raw := range state["interactive_elements"].([]any) {
		e := raw.(map[string]any)
		if e["name"] == "hidden" || e["name"] == "disabled" {
			t.Fatal("noninteractive element", e)
		}
	}
	for _, name := range []string{"text", "area", "editable"} {
		index := elementIndex(t, state, name)
		result := callBrowser(t, d, "type", map[string]any{"index": index, "text": "안녕 '); throw 1; // 😀"})
		if result["typed"] != true {
			t.Fatal(result)
		}
		var contents string
		if err := d.evaluate(t.Context(), fmt.Sprintf(`(() => {const e=globalThis.__lokiNodes[%d];return e.value ?? e.textContent})()`, index), &contents); err != nil || contents != "안녕 '); throw 1; // 😀" {
			t.Fatal(contents, err)
		}
		callBrowser(t, d, "type", map[string]any{"index": index, "text": ""})
		if err := d.evaluate(t.Context(), fmt.Sprintf(`(() => {const e=globalThis.__lokiNodes[%d];return e.value ?? e.textContent})()`, index), &contents); err != nil || contents != "" {
			t.Fatal(contents, err)
		}
	}
	for _, name := range []string{"click", "shadow", "framed"} {
		callBrowser(t, d, "click", map[string]any{"index": elementIndex(t, state, name)})
		page, err := d.page(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"click": "clicked", "shadow": "shadow clicked", "framed": "frame clicked"}[name]
		if page["title"] != want {
			t.Fatal(name, page)
		}
	}
	callBrowser(t, d, "scroll", map[string]any{"direction": "down", "amount": 500})
	if state = callBrowser(t, d, "state", nil); state["pixels_above"].(float64) <= 0 {
		t.Fatal(state)
	}
	callBrowser(t, d, "press", map[string]any{"key": "Home"})
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
	link := elementIndex(t, state, "link")
	callBrowser(t, d, "click", map[string]any{"index": link, "new_tab": true})
	if tabs := callBrowser(t, d, "list_tabs", nil)["tabs"].([]map[string]any); len(tabs) != 2 {
		t.Fatal(tabs)
	}
	if _, err := d.Call(t.Context(), "click", map[string]any{"index": link}); err == nil {
		t.Fatal("accepted index from previous document")
	}
	for _, args := range []map[string]any{{"index": -1}, {"index": 1.5}, {"index": 0, "x": 0}, {"x": -1, "y": 0}} {
		if _, err := d.Call(t.Context(), "click", args); err == nil {
			t.Fatal(args)
		}
	}
}
