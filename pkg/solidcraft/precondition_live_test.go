package solidcraft

import (
	"encoding/json"
	"math"
	"os/exec"
	"testing"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

type inspectSketchSummary struct {
	ProfileCount int
	TotalAreaMM2 float64
}

func startLiveClientOrSkip(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("solidcraft-cli"); err != nil {
		t.Skip("solidcraft-cli not found on PATH; skipping live SolidCraft test")
	}
	client, err := Start()
	if err != nil {
		t.Fatalf("failed to start solidcraft-cli mcp: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func inspectLastSketch(t *testing.T, client *Client) inspectSketchSummary {
	t.Helper()
	out, err := client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		t.Fatalf("inspect_design failed: %v", err)
	}
	var design struct {
		Sketches []struct {
			Profiles []struct {
				AreaMM2 float64 `json:"area_mm2"`
			} `json:"profiles"`
		} `json:"sketches"`
	}
	if err := json.Unmarshal([]byte(out), &design); err != nil {
		t.Fatalf("unmarshal inspect_design failed: %v\nraw: %s", err, out)
	}
	if len(design.Sketches) == 0 {
		t.Fatalf("no sketches found in inspect_design")
	}
	last := design.Sketches[len(design.Sketches)-1]
	sum := 0.0
	for _, p := range last.Profiles {
		sum += p.AreaMM2
	}
	return inspectSketchSummary{
		ProfileCount: len(last.Profiles),
		TotalAreaMM2: sum,
	}
}

func buildSketchCmds(s cadir.Sketch) []Command {
	plane := s.Plane
	if planeStr, ok := plane.(string); ok {
		if planeStr != "XY" && planeStr != "XZ" && planeStr != "YZ" {
			plane = "XY"
		}
	} else if plane == nil {
		plane = "XY"
	}

	cmds := []Command{
		{ID: "sketch.create", Params: SketchCreateParams{Name: s.Name, Plane: plane}},
	}

	// Circles
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeCircle && curve.Radius > 0 {
			cmds = append(cmds, Command{
				ID: "sketch.circle.center",
				Params: SketchCircleCenterParams{
					Center:       []float64{curve.Center.X, curve.Center.Y},
					Radius:       curve.Radius,
					Construction: curve.Construction,
				},
			})
		}
	}

	// Lines
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeLine {
			cmds = append(cmds, Command{
				ID: "sketch.line",
				Params: SketchLineParams{
					Points: [][]float64{
						{curve.Start.X, curve.Start.Y},
						{curve.End.X, curve.End.Y},
					},
					Construction: curve.Construction,
				},
			})
		}
	}

	// Arcs (CCW-normalized)
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeArc {
			st, en := cadir.NormalizeArc(curve.Center, curve.Start, curve.End, curve.MidPoint)
			cmds = append(cmds, Command{
				ID: "sketch.arc.center_point",
				Params: SketchArcCenterPointParams{
					Center:       []float64{curve.Center.X, curve.Center.Y},
					Start:        []float64{st.X, st.Y},
					End:          []float64{en.X, en.Y},
					Construction: curve.Construction,
				},
			})
		}
	}

	// Splines
	hasSplines := false
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeSpline {
			if len(curve.ControlPoints) >= 2 && (curve.Degree == 5 || curve.Degree == 3) {
				var cpts [][]float64
				for _, pt := range curve.ControlPoints {
					cpts = append(cpts, []float64{pt.X, pt.Y})
				}
				cmdID := "sketch.spline.control_point"
				if curve.Degree == 5 {
					cmdID = "sketch.spline.control_point_5"
				}
				cmds = append(cmds, Command{
					ID: cmdID,
					Params: SketchSplineControlPointParams{
						Points:       cpts,
						Construction: curve.Construction,
					},
				})
				hasSplines = true
				continue
			}
			var pts [][]float64
			for _, pt := range curve.Points {
				pts = append(pts, []float64{pt.X, pt.Y})
			}
			if len(pts) >= 2 {
				cmds = append(cmds, Command{
					ID: "sketch.spline.fit_point",
					Params: SketchSplineFitPointParams{
						Points:       pts,
						Construction: curve.Construction,
					},
				})
				hasSplines = true
			}
		}
	}

	if hasSplines {
		cmds = append(cmds, Command{ID: "sketch.loose_ends.tie", Params: map[string]interface{}{}})
	}

	cmds = append(cmds, Command{ID: "sketch.finish"})
	return cmds
}

func TestLiveSketchPreconditioning(t *testing.T) {
	client := startLiveClientOrSkip(t)

	t.Run("SeparateIsolatedTangentCircles", func(t *testing.T) {
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		// 20x10 rectangle with a r=4 circle tangent to bottom line y=0 at (10,0).
		sk := cadir.Sketch{
			Name:  "TangentCircleTest",
			Plane: "XY",
			Curves: []cadir.Curve{
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 0, Y: 0}, End: cadir.Point2D{X: 20, Y: 0}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 20, Y: 0}, End: cadir.Point2D{X: 20, Y: 10}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 20, Y: 10}, End: cadir.Point2D{X: 0, Y: 10}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 0, Y: 10}, End: cadir.Point2D{X: 0, Y: 0}},
				{Type: cadir.CurveTypeCircle, Center: cadir.Point2D{X: 10, Y: 4}, Radius: 4},
			},
		}
		if _, err := client.Batch(buildSketchCmds(sk)); err != nil {
			t.Fatalf("Batch failed: %v", err)
		}
		summary := inspectLastSketch(t, client)
		t.Logf("Un-nudged tangent circle profiles=%d totalArea=%.3f", summary.ProfileCount, summary.TotalAreaMM2)

		// Extrude outer profile (excluding circle) to test if SMLib accepts the pinch point.
		if _, err := client.Execute("solid.extrude", SolidExtrudeParams{
			Name:      "ExtrudeTangent",
			Distance:  10,
			Operation: "new",
			Profiles:  []int{0},
			Sketch:    "TangentCircleTest",
		}); err != nil {
			t.Errorf("Extrude WITHOUT SeparateIsolatedTangentCircles failed: %v", err)
		} else {
			stats, mErr := client.Measure()
			if mErr != nil {
				t.Errorf("Measure failed: %v", mErr)
			} else {
				wantVol := (200.0 - math.Pi*16.0) * 10.0
				t.Logf("Extrude succeeded! Volume=%.2f mm3 (expected outer-minus-hole=%.2f mm3)", stats.VolumeMM3, wantVol)
			}
		}
	})

	t.Run("SplitArcsAtCircles", func(t *testing.T) {
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		// Circle at (0,0) r=5 intersecting an arc centered at (5,0) r=5 from (10,0) to (0,0), closed by line (0,0)->(10,0).
		sk := cadir.Sketch{
			Name:  "SplitArcTest",
			Plane: "XY",
			Curves: []cadir.Curve{
				{Type: cadir.CurveTypeCircle, Center: cadir.Point2D{X: 0, Y: 0}, Radius: 5},
				{Type: cadir.CurveTypeArc, Center: cadir.Point2D{X: 5, Y: 0}, Radius: 5,
					Start: cadir.Point2D{X: 10, Y: 0}, End: cadir.Point2D{X: 0, Y: 0}, MidPoint: cadir.Point2D{X: 5, Y: 5}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 0, Y: 0}, End: cadir.Point2D{X: 10, Y: 0}},
			},
		}
		if _, err := client.Batch(buildSketchCmds(sk)); err != nil {
			t.Fatalf("Batch failed: %v", err)
		}
		unsplit := inspectLastSketch(t, client)

		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		skSplit := sk
		skSplit.Curves = cadir.SplitArcsAtCircles(sk.Curves)
		if _, err := client.Batch(buildSketchCmds(skSplit)); err != nil {
			t.Fatalf("Batch (split) failed: %v", err)
		}
		split := inspectLastSketch(t, client)

		t.Logf("SplitArcsAtCircles comparison: unsplit=(%d profiles, %.3f mm2) vs split=(%d profiles, %.3f mm2)",
			unsplit.ProfileCount, unsplit.TotalAreaMM2, split.ProfileCount, split.TotalAreaMM2)
		if unsplit.ProfileCount != split.ProfileCount || math.Abs(unsplit.TotalAreaMM2-split.TotalAreaMM2) > 0.1 {
			t.Errorf("unsplit differs from split: unsplit=%+v split=%+v", unsplit, split)
		}
	})

	t.Run("BridgeOpenCollinearGaps", func(t *testing.T) {
		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		// 20x10 rectangle whose top edge has a 0.10 mm collinear gap between (10,10) and (10.1,10).
		sk := cadir.Sketch{
			Name:  "CollinearGapTest",
			Plane: "XY",
			Curves: []cadir.Curve{
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 0, Y: 0}, End: cadir.Point2D{X: 20, Y: 0}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 20, Y: 0}, End: cadir.Point2D{X: 20, Y: 10}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 20, Y: 10}, End: cadir.Point2D{X: 10.1, Y: 10}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 10.0, Y: 10}, End: cadir.Point2D{X: 0, Y: 10}},
				{Type: cadir.CurveTypeLine, Start: cadir.Point2D{X: 0, Y: 10}, End: cadir.Point2D{X: 0, Y: 0}},
			},
		}
		if _, err := client.Batch(buildSketchCmds(sk)); err != nil {
			t.Fatalf("Batch failed: %v", err)
		}
		unbridged := inspectLastSketch(t, client)
		t.Logf("Unbridged 0.10mm gap profiles=%d area=%.3f", unbridged.ProfileCount, unbridged.TotalAreaMM2)
		if unbridged.ProfileCount == 0 {
			t.Errorf("0.10mm collinear gap did NOT close without BridgeOpenCollinearGaps")
		}
	})
}
