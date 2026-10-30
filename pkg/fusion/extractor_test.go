package fusion

import (
	"testing"
)

func TestExtractModel(t *testing.T) {
	if !fusionServerUp() {
		t.Skip("Fusion MCP not reachable at 127.0.0.1:27182; skipping live test")
	}
	client := NewClient("")
	extractor := NewExtractor(client)

	model, err := extractor.ExtractModel()
	if err != nil {
		t.Fatalf("ExtractModel failed: %v", err)
	}

	t.Logf("Extracted model: %s with %d timeline steps", model.Name, len(model.Timeline))
	for _, step := range model.Timeline {
		t.Logf("  [%d] %s (%s)", step.Index, step.Name, step.Type)
	}
}
