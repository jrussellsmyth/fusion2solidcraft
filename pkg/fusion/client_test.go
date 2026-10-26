package fusion

import (
	"testing"
)

func TestExecuteScript(t *testing.T) {
	client := NewClient("")
	script := `import adsk.core
def run(_context: str):
    app = adsk.core.Application.get()
    doc = app.activeDocument
    print(f"Doc: {doc.name if doc else 'None'}")
`
	out, err := client.ExecuteScript(script, true)
	if err != nil {
		t.Fatalf("ExecuteScript failed: %v", err)
	}
	t.Logf("Fusion MCP response: %s", out)
}
