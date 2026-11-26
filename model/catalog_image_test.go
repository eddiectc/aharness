package model

import (
	"testing"
)

func TestCatalogImageInput(t *testing.T) {
	body := `{"p":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/go/openai/v1","models":{
		"vision":{"name":"V","tool_call":true,"attachment":true,"limit":{"context":1000},"modalities":{"input":["text","image"],"output":["text"]}},
		"typed":{"name":"T","tool_call":true,"limit":{"context":1000},"modalities":{"input":["text","image/png"],"output":["text"]}},
		"textonly":{"name":"X","tool_call":true,"attachment":false,"limit":{"context":1000},"modalities":{"input":["text"],"output":["text"]}}
	}}}`
	parsed, err := parseCatalog([]byte(body), "p", "test")
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	cases := map[string]bool{"vision": true, "typed": true, "textonly": false}
	for id, want := range cases {
		entry, ok := parsed.Models[id]
		if !ok {
			t.Fatalf("model %q missing from the catalog", id)
		}
		if entry.ImageInput != want {
			t.Errorf("%s ImageInput = %v, want %v", id, entry.ImageInput, want)
		}
		if entry.Unavailable != "" {
			t.Errorf("%s is unavailable: %s", id, entry.Unavailable)
		}
	}
}
