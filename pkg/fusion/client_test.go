package fusion

import (
	"net"
	"testing"
	"time"
)

// fusionServerUp reports whether the Fusion MCP endpoint is accepting TCP
// connections, so live tests can skip cleanly on machines without Fusion.
func fusionServerUp() bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:27182", 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestExecuteScript(t *testing.T) {
	if !fusionServerUp() {
		t.Skip("Fusion MCP not reachable at 127.0.0.1:27182; skipping live test")
	}
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
