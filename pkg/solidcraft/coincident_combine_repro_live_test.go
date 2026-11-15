package solidcraft

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

func TestLiveCoincidentCombineRepro(t *testing.T) {
	client := startLiveClientOrSkip(t)

	irData, err := os.ReadFile("../../model_ir.json")
	if err != nil {
		t.Skip("model_ir.json not present")
	}
	var model cadir.Model
	if err := json.Unmarshal(irData, &model); err != nil {
		t.Fatalf("parse model_ir.json: %v", err)
	}
	// Replay only steps 0..8 (builds one Socket body).
	model.Timeline = model.Timeline[:9]

	if err := client.NewDesign(); err != nil {
		t.Fatalf("NewDesign: %v", err)
	}
	comp := NewCompiler(client)
	if _, err := comp.CompileModelWithOptions(&model, CompileOptions{StartStep: -1, UntilStep: 8, StepFrom: -1}); err != nil {
		t.Fatalf("CompileModelWithOptions(0..8): %v", err)
	}

	// Copy the Socket body in-place and try solid.combine
	if _, err := client.Execute("solid.move", SolidMoveParams{
		Bodies:    []string{"Xiao_RP2040_Fifi_Left_McuCover"},
		Translate: []float64{0, 0, 0},
		Copy:      true,
	}); err != nil {
		t.Fatalf("copy socket: %v", err)
	}

	inspOut, err := client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		t.Fatalf("inspect_design: %v", err)
	}
	var d struct {
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal([]byte(inspOut), &d); err != nil {
		t.Fatalf("unmarshal inspect_design: %v", err)
	}
	if len(d.Bodies) != 2 {
		t.Fatalf("expected 2 bodies, got %d", len(d.Bodies))
	}

	_, combErr := client.Execute("solid.combine", SolidCombineParams{
		Target:    d.Bodies[0].Name,
		Tools:     []string{d.Bodies[1].Name},
		Operation: "join",
		KeepTools: false,
	})
	if combErr == nil || !strings.Contains(combErr.Error(), "another entity is already related") {
		t.Fatalf("expected 'another entity is already related' error, got %v", combErr)
	}
	// Verify selection.delete removes the duplicate body cleanly
	if _, delErr := client.Execute("selection.delete", map[string]interface{}{
		"items": []map[string]interface{}{{"type": "body", "name": d.Bodies[1].Name}},
	}); delErr != nil {
		t.Fatalf("selection.delete: %v", delErr)
	}
}
