package solidcraft

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

func TestLiveFilletWorkarounds(t *testing.T) {
	client := startLiveClientOrSkip(t)

	// Build a body from a multi-point degree-5 B-spline + line closed profile
	// so its side faces have interior-knot-split B-spline spans (< 15 mm).
	buildSplinePlate := func(t *testing.T) [][]float64 {
		t.Helper()
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		if _, err := client.Batch([]Command{
			{ID: "sketch.create", Params: SketchCreateParams{Name: "SplinePlate", Plane: "XY"}},
			{
				ID: "sketch.spline.control_point_5",
				Params: SketchSplineControlPointParams{
					Points: [][]float64{
						{0, 0}, {10, 8}, {22, 12}, {35, 10}, {48, 4}, {60, 0},
					},
				},
			},
			{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{60, 0}, {60, -15}}}},
			{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{60, -15}, {0, -15}}}},
			{ID: "sketch.line", Params: SketchLineParams{Points: [][]float64{{0, -15}, {0, 0}}}},
			{ID: "sketch.loose_ends.tie", Params: map[string]interface{}{}},
			{ID: "sketch.finish"},
		}); err != nil {
			t.Fatalf("sketch Batch: %v", err)
		}
		if _, err := client.Execute("solid.extrude", SolidExtrudeParams{
			Name:      "ExtrudeSplinePlate",
			Distance:  8.0,
			Operation: "new",
			Profiles:  "all",
			Sketch:    "SplinePlate",
		}); err != nil {
			t.Fatalf("solid.extrude: %v", err)
		}
		topoOut, err := client.CallTool("body_topology", map[string]interface{}{"body": "Body1"})
		if err != nil {
			t.Fatalf("body_topology: %v", err)
		}
		var topo struct {
			Edges []struct {
				Mid []float64 `json:"mid"`
			} `json:"edges"`
		}
		if err := json.Unmarshal([]byte(topoOut), &topo); err != nil {
			t.Fatalf("unmarshal body_topology: %v", err)
		}
		var topMids [][]float64
		for _, e := range topo.Edges {
			if len(e.Mid) == 3 && e.Mid[2] > 7.9 {
				topMids = append(topMids, e.Mid)
			}
		}
		if len(topMids) == 0 {
			t.Fatalf("no top edges found at z=8")
		}
		return topMids
	}

	t.Run("AllTopEdges_NoShortSpanFilter_NonChained", func(t *testing.T) {
		mids := buildSplinePlate(t)
		noChain := false
		if _, err := client.Execute("solid.fillet", SolidFilletParams{
			Radius:       2.0,
			Edges:        mids,
			TangentChain: &noChain,
		}); err != nil {
			t.Fatalf("non-chained fillet on %d top edges failed: %v", len(mids), err)
		}
		t.Logf("Non-chained fillet succeeded on all %d top edges", len(mids))
	})

	t.Run("AllTopEdges_Chained", func(t *testing.T) {
		mids := buildSplinePlate(t)
		chain := true
		if _, err := client.Execute("solid.fillet", SolidFilletParams{
			Radius:       2.0,
			Edges:        mids,
			TangentChain: &chain,
		}); err != nil {
			t.Fatalf("chained fillet on %d top edges failed: %v", len(mids), err)
		}
		t.Logf("Chained fillet succeeded on all %d top edges", len(mids))
	})
}

func TestLiveFillet5FromCheckpoint(t *testing.T) {
	if os.Getenv("FUSION2SOLIDCRAFT_RUN_FILLET5") == "" {
		t.Skip("set FUSION2SOLIDCRAFT_RUN_FILLET5=1 to run slow Step 93 Fillet5 checkpoint test")
	}
	const ckpt = "/tmp/pre_fillet5.sc"
	if _, err := os.Stat(ckpt); err != nil {
		t.Skip("/tmp/pre_fillet5.sc not present; skipping Fillet5 checkpoint test")
	}
	irData, err := os.ReadFile("../../model_ir.json")
	if err != nil {
		t.Skip("model_ir.json not present")
	}
	var model cadir.Model
	if err := json.Unmarshal(irData, &model); err != nil {
		t.Fatalf("unmarshal model_ir.json: %v", err)
	}
	var f cadir.FilletFeature
	for _, s := range model.Timeline {
		if s.Index == 93 && s.Fillet != nil {
			f = *s.Fillet
			break
		}
	}
	if f.Name == "" {
		t.Fatal("step 93 Fillet5 not found")
	}

	client := startLiveClientOrSkip(t)

	// Helper to open checkpoint and extract matched edges
	loadAndMatch := func(t *testing.T) ([][]float64, [][]float64) {
		t.Helper()
		if _, err := client.CallTool("open", map[string]interface{}{"path": ckpt}); err != nil {
			t.Fatalf("open %s: %v", ckpt, err)
		}
		type edgePoint struct {
			Mid   []float64 `json:"mid"`
			Start []float64 `json:"start"`
			End   []float64 `json:"end"`
		}
		var allEdges []edgePoint
		for _, bName := range f.TargetBodies {
			topoOut, err := client.CallTool("body_topology", map[string]interface{}{"body": bName})
			if err != nil {
				continue
			}
			var topo struct {
				Edges []edgePoint `json:"edges"`
			}
			if json.Unmarshal([]byte(topoOut), &topo) == nil {
				allEdges = append(allEdges, topo.Edges...)
			}
		}
		var matched []edgePoint
		seenMid := make(map[[3]int]bool)
		for _, target := range f.TargetEdges {
			bestDist := 1e9
			var bestEdge *edgePoint
			for i := range allEdges {
				e := &allEdges[i]
				if len(e.Mid) == 3 {
					d := math.Sqrt((e.Mid[0]-target.X)*(e.Mid[0]-target.X) + (e.Mid[1]-target.Y)*(e.Mid[1]-target.Y) + (e.Mid[2]-target.Z)*(e.Mid[2]-target.Z))
					if d < bestDist {
						bestDist = d
						bestEdge = e
					}
				}
			}
			if bestDist < 2.5 && bestEdge != nil {
				key := [3]int{int(math.Round(bestEdge.Mid[0] * 1000)), int(math.Round(bestEdge.Mid[1] * 1000)), int(math.Round(bestEdge.Mid[2] * 1000))}
				if !seenMid[key] {
					seenMid[key] = true
					matched = append(matched, *bestEdge)
				}
			}
		}
		var allMids, filteredMids [][]float64
		ptKey := func(pt []float64) [3]int {
			if len(pt) < 3 {
				return [3]int{}
			}
			return [3]int{int(math.Round(pt[0] * 20)), int(math.Round(pt[1] * 20)), int(math.Round(pt[2] * 20))}
		}
		vc := make(map[[3]int]int)
		for _, e := range matched {
			vc[ptKey(e.Start)]++
			vc[ptKey(e.End)]++
		}
		for _, e := range matched {
			allMids = append(allMids, e.Mid)
			isolated := vc[ptKey(e.Start)] <= 1 && vc[ptKey(e.End)] <= 1
			chord := 0.0
			if len(e.Start) == 3 && len(e.End) == 3 {
				chord = math.Sqrt((e.End[0]-e.Start[0])*(e.End[0]-e.Start[0]) + (e.End[1]-e.Start[1])*(e.End[1]-e.Start[1]) + (e.End[2]-e.Start[2])*(e.End[2]-e.Start[2]))
			}
			if !isolated || chord >= 15.0 {
				filteredMids = append(filteredMids, e.Mid)
			}
		}
		return allMids, filteredMids
	}

	for _, tc := range []struct {
		name     string
		filtered bool
		chain    bool
	}{
		{"Unfiltered_NonChained", false, false},
		{"Unfiltered_Chained", false, true},
		{"Filtered_NonChained", true, false},
		{"Filtered_Chained", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allMids, filteredMids := loadAndMatch(t)
			edges := allMids
			if tc.filtered {
				edges = filteredMids
			}
			ch := tc.chain
			resp, err := client.Execute("solid.fillet", SolidFilletParams{
				Body:         "Xiao_RP2040_Fifi_Right_Body_",
				Radius:       f.RadiusMM,
				Edges:        edges,
				TangentChain: &ch,
			})
			t.Logf("%s: edges=%d err=%v resp=%s", tc.name, len(edges), err, resp)
		})
	}
}
