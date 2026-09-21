package browser

import "testing"

func TestBrowserKeyParsing(t *testing.T) {
	for _, test := range []struct {
		input     string
		modifiers int
		key       string
		code      string
		virtual   int
		canonical string
	}{
		{input: "Enter", key: "Enter", code: "Enter", virtual: 13, canonical: "Enter"},
		{input: "a", key: "a", code: "KeyA", virtual: 65, canonical: "A"},
		{input: "A", modifiers: inputModifierBits["Shift"], key: "A", code: "KeyA", virtual: 65, canonical: "A"},
		{input: "7", key: "7", code: "Digit7", virtual: 55, canonical: "7"},
		{input: "F12", key: "F12", code: "F12", virtual: 123, canonical: "F12"},
	} {
		key, canonical, err := parseBrowserKey(test.input, test.modifiers)
		if err != nil || key.Key != test.key || key.Code != test.code || key.Virtual != test.virtual || canonical != test.canonical {
			t.Fatalf("%q => %#v canonical=%q err=%v", test.input, key, canonical, err)
		}
	}
	for _, input := range []string{"", "AA", "/", "Control"} {
		if _, _, err := parseBrowserKey(input, 0); err == nil {
			t.Fatalf("accepted unsupported key %q", input)
		}
	}
}

func TestTextArgumentBounds(t *testing.T) {
	if text, err := textArgument(map[string]any{"text": ""}); err != nil || text != "" {
		t.Fatalf("empty text = %q %v", text, err)
	}
	if _, err := textArgument(map[string]any{"text": "a\x00b"}); err == nil {
		t.Fatal("accepted NUL text")
	}
	if _, err := textArgument(map[string]any{"text": 1}); err == nil {
		t.Fatal("accepted non-string text")
	}
}
