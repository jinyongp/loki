package redact

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestRedactionSlicesAndPartialWrites(t *testing.T) {
	f, err := New([]string{"synthetic-secret", "secret", "", "synthetic-secret", "한글-token"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input    string
		from, to int
		want     string
	}{
		{"before synthetic-secret after", 0, 29, "before [REDACTED] after"},
		{"before synthetic-secret after", 10, 14, "[REDACTED]"},
		{"before synthe", 0, 13, "before [REDACTED]"},
		{"before synthetic-secret after", 23, 29, " after"},
		{"한글-token", 2, 5, "[REDACTED]"},
		{"secretsecret!", 0, 13, "[REDACTED]!"},
		{"public!", 0, 7, "public!"},
	} {
		if got := string(f.Slice([]byte(tc.input), tc.from, tc.to)); got != tc.want {
			t.Errorf("slice %d:%d: got %q want %q", tc.from, tc.to, got, tc.want)
		}
	}
	for _, value := range []any{f, *f} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if text := fmt.Sprintf(format, value); strings.Contains(text, "synthetic-secret") {
				t.Fatal("filter diagnostic leaked value")
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private filter serialized")
		}
	}
	if _, err := New([]string{strings.Repeat("x", 1048577)}); err == nil {
		t.Fatal("unbounded secret accepted")
	}
}

func TestOverlapsAndTailContext(t *testing.T) {
	f, _ := New([]string{"ababa"})
	if got := string(f.Slice([]byte("abababa!"), 0, 8)); got != "[REDACTED]!" {
		t.Fatal(got)
	}
	input := []byte("before private-synthetic after!")
	f, _ = New([]string{"private-synthetic"})
	visible := 10
	retained := input[len(input)-visible-f.Context():]
	if got := string(f.Slice(retained, f.Context(), len(retained))); got != "[REDACTED] after!" {
		t.Fatalf("tail context: %q", got)
	}
}

func TestPrefixSuffixMatchesNaiveOracle(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		pattern := make([]byte, 1+random.IntN(40))
		text := make([]byte, random.IntN(len(pattern)))
		for i := range pattern {
			pattern[i] = 'a' + byte(random.IntN(3))
		}
		for i := range text {
			text[i] = 'a' + byte(random.IntN(3))
		}
		want := 0
		for length := 1; length <= len(text); length++ {
			if string(text[len(text)-length:]) == string(pattern[:length]) {
				want = length
			}
		}
		if got := matches(pattern, text, nil); got != want {
			t.Fatalf("prefix length got %d want %d", got, want)
		}
	}
}

func TestAllPageCutsStayMasked(t *testing.T) {
	value := "fixture-secret-value"
	f, _ := New([]string{value})
	for written := 1; written <= len(value); written++ {
		data := []byte("public:" + value[:written])
		for start := 7; start < len(data); start++ {
			for end := start + 1; end <= len(data); end++ {
				if got := string(f.Slice(data, start, end)); got != marker {
					t.Fatalf("secret fragment escaped at %d/%d:%d", written, start, end)
				}
			}
		}
	}
}

func TestFilterMatchesNaiveWindowOracle(t *testing.T) {
	random := rand.New(rand.NewPCG(3, 4))
	for range 3000 {
		makeText := func(length int) string {
			b := make([]byte, length)
			for i := range b {
				b[i] = 'a' + byte(random.IntN(3))
			}
			return string(b)
		}
		data := makeText(1 + random.IntN(40))
		values := []string{makeText(1 + random.IntN(8)), makeText(1 + random.IntN(8))}
		mask := make([]bool, len(data))
		for _, value := range values {
			for start := range len(data) {
				length := min(len(value), len(data)-start)
				if data[start:start+length] == value[:length] {
					for i := start; i < start+length; i++ {
						mask[i] = true
					}
				}
			}
		}
		from := random.IntN(len(data))
		to := from + random.IntN(len(data)-from+1)
		var expected strings.Builder
		for i := from; i < to; {
			if !mask[i] {
				expected.WriteByte(data[i])
				i++
				continue
			}
			expected.WriteString(marker)
			for i < to && mask[i] {
				i++
			}
		}
		filter, err := New(values)
		if err != nil {
			t.Fatal(err)
		}
		if actual := string(filter.Slice([]byte(data), from, to)); actual != expected.String() {
			t.Fatalf("window oracle mismatch: got %q want %q", actual, expected.String())
		}
	}
}
