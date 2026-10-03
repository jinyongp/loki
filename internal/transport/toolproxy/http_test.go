package toolproxy

import (
	"net/http"
	"strings"
	"testing"
)

type recordingHTTPTransport struct{ request *http.Request }

func (r *recordingHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.request = request
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}, nil
}

func TestLocalHTTPBearerCannotFollowUnrelatedDestinations(t *testing.T) {
	base := &recordingHTTPTransport{}
	transport := bearerTransport{base: base, endpoint: "http://127.0.0.1:18765/mcp", token: "private"}
	for _, destination := range []string{"http://example.org/mcp", "http://127.0.0.1:18766/mcp", "http://127.0.0.1:18765/other", "http://127.0.0.1:18765/mcp?forward=1", "http://user@127.0.0.1:18765/mcp"} {
		request, err := http.NewRequest("POST", destination, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(request); err == nil || base.request != nil {
			t.Fatalf("unrelated destination received bearer: %s", destination)
		}
	}
	request, _ := http.NewRequest("POST", transport.endpoint, nil)
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatal(err)
	}
	if base.request.Header.Get("Authorization") != "Bearer private" || request.Header.Get("Authorization") != "" {
		t.Fatal("bearer must enter only the cloned owned request")
	}
}

func TestLocalHTTPTransportRejectsUnsafeConnectionMetadata(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:18765/mcp", "http://localhost:18765/mcp", "http://127.0.0.1:80/mcp", "http://127.0.0.1:18765/mcp#other", "http://127.0.0.1:18765/mcp?token=private"} {
		if _, _, err := LocalHTTPTransport(endpoint, []byte(strings.Repeat("a", 64))); err == nil {
			t.Fatalf("accepted unsafe endpoint: %s", endpoint)
		}
	}
	for _, token := range []string{"private", strings.Repeat("z", 64), strings.Repeat("a", 65)} {
		if _, _, err := LocalHTTPTransport("http://127.0.0.1:18765/mcp", []byte(token)); err == nil {
			t.Fatal("accepted invalid bearer")
		}
	}
}
