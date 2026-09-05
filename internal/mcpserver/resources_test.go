package mcpserver

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConfiguredWidgetOrigins(t *testing.T) {
	for _, origins := range []ResourceOrigins{{}, {ArtifactBaseURL: "https://images.example.test/artifacts", PreviewDomain: "preview.example.test"}} {
		server, err := NewConfigured(testHandlers(t), origins)
		if err != nil {
			t.Fatal(err)
		}
		a, b := mcp.NewInMemoryTransports()
		ss, err := server.Connect(t.Context(), a, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ss.Close()
		client, err := mcp.NewClient(&mcp.Implementation{Name: "resource-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		list, err := client.ListResources(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, resource := range list.Resources {
			encoded, _ := json.Marshal(resource.Meta)
			if bytes.Contains(encoded, []byte("streamliner.im")) {
				t.Fatal("captured deployment origin retained")
			}
			want, _ := json.Marshal(origins.metadata(resource.URI))
			if !bytes.Equal(encoded, want) {
				t.Fatal(string(encoded), string(want))
			}
			read, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: resource.URI})
			if err != nil {
				t.Fatal(err)
			}
			for _, content := range read.Contents {
				encoded, _ = json.Marshal(content.Meta)
				if !bytes.Equal(encoded, want) {
					t.Fatal(string(encoded))
				}
				if content.Text == "" {
					t.Fatal("widget HTML missing")
				}
			}
		}
	}
}
