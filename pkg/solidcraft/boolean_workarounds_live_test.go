package solidcraft

import (
	"math"
	"testing"
)

func TestLiveBooleanWorkarounds(t *testing.T) {
	client := startLiveClientOrSkip(t)

	t.Run("TouchingCurvedFaceCut_NoRaise", func(t *testing.T) {
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		if _, err := client.Execute("solid.box", map[string]interface{}{
			"name":   "BasePlate",
			"corner": []float64{0, 0, 0},
			"length": 50,
			"width":  30,
			"height": 8,
		}); err != nil {
			t.Fatalf("solid.box: %v", err)
		}
		for _, hole := range []struct {
			name string
			x    float64
		}{
			{"Hole1", 15},
			{"Hole2", 35},
		} {
			if _, err := client.Execute("solid.cylinder", map[string]interface{}{
				"name":      hole.name,
				"operation": "cut",
				"center":    []float64{hole.x, 15, 0},
				"radius":    2.5,
				"height":    8,
				"targets":   []string{"Body1"},
			}); err != nil {
				t.Fatalf("solid.cylinder %s: %v", hole.name, err)
			}
		}
		preStats, err := client.Measure()
		if err != nil {
			t.Fatalf("pre Measure: %v", err)
		}

		// Sketch on z=0 plane cutting +1 mm into the bottom (same geometry as repro_kernel_bug.py)
		if _, err := client.Batch([]Command{
			{ID: "sketch.create", Params: SketchCreateParams{Name: "ChannelSketch", Plane: "XY"}},
			{ID: "sketch.arc.center_point", Params: SketchArcCenterPointParams{Center: []float64{15, 15}, Start: []float64{15, 19}, End: []float64{15, 11}}},
			{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{15, 11}, {35, 11}}}},
			{ID: "sketch.arc.center_point", Params: SketchArcCenterPointParams{Center: []float64{35, 15}, Start: []float64{35, 11}, End: []float64{35, 19}}},
			{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{35, 19}, {15, 19}}}},
			{ID: "sketch.finish"},
		}); err != nil {
			t.Fatalf("sketch Batch: %v", err)
		}

		if _, err := client.Execute("solid.extrude", SolidExtrudeParams{
			Name:      "ChannelCut",
			Distance:  1.0,
			Operation: "cut",
			Sketch:    "ChannelSketch",
			Targets:   []string{"Body1"},
		}); err != nil {
			t.Fatalf("ChannelCut WITHOUT raised-tool retry failed: %v", err)
		}

		postStats, err := client.Measure()
		if err != nil {
			t.Fatalf("post Measure: %v", err)
		}
		removed := preStats.VolumeMM3 - postStats.VolumeMM3
		t.Logf("ChannelCut succeeded! pre=%.2f post=%.2f removed=%.2f mm3", preStats.VolumeMM3, postStats.VolumeMM3, removed)
		if removed < 150.0 {
			t.Errorf("ChannelCut removed %.2f mm3, expected >= 150.0 mm3 (silent no-op or wrong cut)", removed)
		}
	})

	t.Run("CoincidentEdgeTouchJoin", func(t *testing.T) {
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		if _, err := client.Execute("solid.box", map[string]interface{}{
			"name": "BoxA", "corner": []float64{0, 0, 0}, "length": 10, "width": 10, "height": 10,
		}); err != nil {
			t.Fatalf("BoxA: %v", err)
		}
		if _, err := client.Execute("solid.box", map[string]interface{}{
			"name": "BoxB", "corner": []float64{10, 10, 0}, "length": 10, "width": 10, "height": 10,
		}); err != nil {
			t.Fatalf("BoxB: %v", err)
		}
		// Two boxes touching only along vertical edge (10,10,0)-(10,10,10)
		_, err := client.Execute("solid.combine", SolidCombineParams{
			Target:    "Body1",
			Tools:     []string{"Body2"},
			Operation: "join",
			KeepTools: false,
		})
		t.Logf("Edge-touching combine result: err=%v", err)
	})

	t.Run("MultiToolCombine", func(t *testing.T) {
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		if _, err := client.Execute("solid.box", map[string]interface{}{
			"name": "Base", "corner": []float64{0, 0, 0}, "length": 50, "width": 20, "height": 10,
		}); err != nil {
			t.Fatalf("Base: %v", err)
		}
		for i, x := range []float64{5, 20, 35} {
			if _, err := client.Execute("solid.box", map[string]interface{}{
				"name": "Rib", "corner": []float64{x, 0, 10}, "length": 5, "width": 20, "height": 5,
			}); err != nil {
				t.Fatalf("Rib %d: %v", i, err)
			}
		}
		if _, err := client.Execute("solid.combine", SolidCombineParams{
			Target:    "Body1",
			Tools:     []string{"Body2", "Body3", "Body4"},
			Operation: "join",
			KeepTools: false,
		}); err != nil {
			t.Fatalf("MultiToolCombine failed: %v", err)
		}
		stats, err := client.Measure()
		if err != nil {
			t.Fatalf("Measure: %v", err)
		}
		want := 50*20*10.0 + 3*(5*20*5.0)
		t.Logf("MultiToolCombine volume=%.2f mm3 (expected %.2f mm3)", stats.VolumeMM3, want)
		if math.Abs(stats.VolumeMM3-want) > 1.0 {
			t.Errorf("volume=%.2f, want %.2f", stats.VolumeMM3, want)
		}
	})
}
