package solidcraft

import (
	"encoding/json"
	"math"
	"testing"
)

func TestLiveCoplanarFaceExtrude(t *testing.T) {
	client := startLiveClientOrSkip(t)

	// Build a 40x20x5 box whose top z=5 face is split into two coplanar faces
	// by a through-slot across the middle (x=0..40, y=8..12, z=3..5).
	buildSplitTopBody := func(t *testing.T) {
		t.Helper()
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		rectLines := func(x0, y0, x1, y1 float64) []Command {
			return []Command{
				{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{x0, y0}, {x1, y0}}}},
				{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{x1, y0}, {x1, y1}}}},
				{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{x1, y1}, {x0, y1}}}},
				{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{x0, y1}, {x0, y0}}}},
			}
		}
		cmds := []Command{{ID: "sketch.create", Params: SketchCreateParams{Name: "Base", Plane: "XY"}}}
		cmds = append(cmds, rectLines(0, 0, 40, 20)...)
		cmds = append(cmds,
			Command{ID: "solid.extrude", Params: SolidExtrudeParams{Sketch: "Base", Profiles: "all", Distance: 5, Operation: "new", BodyName: "SplitBox"}},
			Command{ID: "sketch.create", Params: SketchCreateParams{Name: "Slot", Plane: map[string]interface{}{
				"origin": []float64{0, 0, 5},
				"x_dir":  []float64{1, 0, 0},
				"y_dir":  []float64{0, 1, 0},
			}}},
		)
		cmds = append(cmds, rectLines(-1, 8, 41, 12)...)
		cmds = append(cmds, Command{ID: "solid.extrude", Params: SolidExtrudeParams{Sketch: "Slot", Profiles: "all", Distance: -2, Operation: "cut", Body: "SplitBox"}})
		if _, err := client.Batch(cmds); err != nil {
			t.Fatalf("buildSplitTopBody: %v", err)
		}
	}

	totalVolume := func(t *testing.T) float64 {
		t.Helper()
		inspOut, err := client.CallTool("inspect_design", map[string]interface{}{})
		if err != nil {
			t.Fatalf("inspect_design: %v", err)
		}
		var d struct {
			Bodies []struct {
				Volume float64 `json:"volume_mm3"`
			} `json:"bodies"`
		}
		if err := json.Unmarshal([]byte(inspOut), &d); err != nil {
			t.Fatalf("unmarshal inspect_design: %v", err)
		}
		var sum float64
		for _, b := range d.Bodies {
			sum += b.Volume
		}
		return sum
	}

	// Single face extrude at (10, 4, 5) only extrudes the y=0..8 half (+960 mm3),
	// whereas Faces extrudes both disconnected coplanar faces (+1920 mm3).
	t.Run("SingleFace_vs_BothCoplanarFaces", func(t *testing.T) {
		buildSplitTopBody(t)
		baseVol := totalVolume(t)
		if _, err := client.Execute("solid.extrude", SolidExtrudeParams{
			Body:      "SplitBox",
			Face:      []float64{10, 4, 5},
			Distance:  3,
			Operation: "join",
		}); err != nil {
			t.Fatalf("single face extrude: %v", err)
		}
		singleDelta := totalVolume(t) - baseVol

		buildSplitTopBody(t)
		if _, err := client.Execute("solid.extrude", SolidExtrudeParams{
			FaceBody:  "SplitBox",
			Faces:     [][]float64{{10, 4, 5}, {10, 16, 5}},
			Distance:  3,
			Operation: "join",
		}); err != nil {
			t.Fatalf("multi face extrude: %v", err)
		}
		multiDelta := totalVolume(t) - baseVol

		t.Logf("baseVol=%.2f singleDelta=%.2f multiDelta=%.2f", baseVol, singleDelta, multiDelta)
		if math.Abs(singleDelta-960.0) > 1.0 {
			t.Fatalf("expected single face delta ~960 mm3, got %.2f", singleDelta)
		}
		if math.Abs(multiDelta-1920.0) > 1.0 {
			t.Fatalf("expected multi face delta ~1920 mm3, got %.2f", multiDelta)
		}
	})
}
