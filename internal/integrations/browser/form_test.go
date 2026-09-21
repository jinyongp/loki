package browser

import "testing"

func TestOptionSelectorPreflight(t *testing.T) {
	selectors, err := parseOptionSelectors(map[string]any{
		"options": []any{
			map[string]any{"value": "one"},
			map[string]any{"label": "Two"},
			map[string]any{"index": 2},
		},
	})
	if err != nil || len(selectors) != 3 ||
		selectors[0].Kind != "value" || selectors[1].Kind != "label" ||
		selectors[2].Kind != "index" || selectors[2].Index != 2 {
		t.Fatalf("selectors = %#v err=%v", selectors, err)
	}
	for _, options := range []any{
		[]any{},
		[]any{map[string]any{}},
		[]any{map[string]any{"value": "one", "label": "One"}},
		[]any{map[string]any{"index": -1}},
		[]any{"one"},
	} {
		if _, err := parseOptionSelectors(map[string]any{"options": options}); err == nil {
			t.Fatalf("accepted invalid selectors %#v", options)
		}
	}
}
