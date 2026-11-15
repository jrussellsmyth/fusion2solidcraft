package solidcraft

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"reflect"
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

	t.Run("SplitArcsAtCircles_Obsolete", func(t *testing.T) {
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
		t.Logf("Unsplit arc+circle on SolidCraft 0.42+: %d profiles, %.3f mm2", unsplit.ProfileCount, unsplit.TotalAreaMM2)
		if unsplit.ProfileCount == 0 {
			t.Errorf("expected unsplit arc+circle to form closed profiles without SplitArcsAtCircles")
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
		t.Logf("Unbridged 0.10mm gap profiles=%d area=%.3f (BridgeOpenCollinearGaps still required)", unbridged.ProfileCount, unbridged.TotalAreaMM2)
		if unbridged.ProfileCount != 0 {
			t.Errorf("expected 0 profiles for unbridged 0.10mm collinear gap, got %d", unbridged.ProfileCount)
		}

		if err := client.NewDesign(); err != nil {
			t.Fatalf("NewDesign: %v", err)
		}
		skBridged := sk
		skBridged.Curves = cadir.BridgeOpenCollinearGaps(sk.Curves)
		if _, err := client.Batch(buildSketchCmds(skBridged)); err != nil {
			t.Fatalf("Batch (bridged) failed: %v", err)
		}
		bridged := inspectLastSketch(t, client)
		t.Logf("Bridged 0.10mm gap profiles=%d area=%.3f", bridged.ProfileCount, bridged.TotalAreaMM2)
		if bridged.ProfileCount != 1 || math.Abs(bridged.TotalAreaMM2-200.0) > 0.1 {
			t.Errorf("expected bridged 20x10 rectangle to yield 1 profile (200.0 mm2), got %+v", bridged)
		}
	})
}

func TestLiveModelIRSketchesWithoutPreconditioning(t *testing.T) {
	data, err := os.ReadFile("../../model_ir.json")
	if err != nil {
		t.Skip("model_ir.json not present; skipping real-IR sketch A/B test")
	}
	var model cadir.Model
	if err := json.Unmarshal(data, &model); err != nil {
		t.Fatalf("unmarshal model_ir.json: %v", err)
	}

	client := startLiveClientOrSkip(t)

	for _, step := range model.Timeline {
		if step.Type != "sketch" || step.Sketch == nil {
			continue
		}
		sk := *step.Sketch
		afterBridge := cadir.BridgeOpenCollinearGaps(sk.Curves)

		changedBridge := !reflect.DeepEqual(sk.Curves, afterBridge)
		isRegressionSketch := sk.Name == "Socket" || sk.Name == "connectors" || sk.Name == "bottom_covers"
		if !changedBridge && !isRegressionSketch {
			continue
		}

		t.Run(sk.Name, func(t *testing.T) {
			t.Logf("Step %d (%s) modified by BridgeOpenCollinearGaps=%v",
				step.Index, sk.Name, changedBridge)

			// 1. Run WITH remaining preconditioner (BridgeOpenCollinearGaps via compileAndExecuteSketch)
			if err := client.NewDesign(); err != nil {
				t.Fatalf("NewDesign: %v", err)
			}
			comp := NewCompiler(client)
			if err := comp.compileAndExecuteSketch(sk); err != nil {
				t.Fatalf("compileAndExecuteSketch failed: %v", err)
			}
			baseline := inspectLastSketch(t, client)

			// 2. Run WITHOUT BridgeOpenCollinearGaps (raw curves, NormalizeArc only)
			if err := client.NewDesign(); err != nil {
				t.Fatalf("NewDesign: %v", err)
			}
			if _, err := client.Batch(buildSketchCmds(sk)); err != nil {
				t.Fatalf("raw Batch failed: %v", err)
			}
			raw := inspectLastSketch(t, client)

			t.Logf("  compiled=(%d profiles, %.3f mm2) vs raw=(%d profiles, %.3f mm2)",
				baseline.ProfileCount, baseline.TotalAreaMM2, raw.ProfileCount, raw.TotalAreaMM2)
			if !changedBridge && (baseline.ProfileCount != raw.ProfileCount || math.Abs(baseline.TotalAreaMM2-raw.TotalAreaMM2) > 0.5) {
				t.Errorf("sketch %s differs without BridgeOpenCollinearGaps: compiled=%+v raw=%+v", sk.Name, baseline, raw)
			}

			if sk.Name == "Socket" || sk.Name == "bottom_covers" {
				if _, err := client.Execute("solid.extrude", SolidExtrudeParams{
					Name:      "ExtrudeRawTangent",
					Distance:  10,
					Operation: "new",
					Profiles:  "all",
					Sketch:    sk.Name,
				}); err != nil {
					t.Errorf("raw extrude failed for %s: %v", sk.Name, err)
				} else {
					stats, mErr := client.Measure()
					if mErr != nil {
						t.Errorf("Measure after raw extrude failed for %s: %v", sk.Name, mErr)
					} else {
						t.Logf("  raw extrude (Profiles=all) succeeded! Volume=%.2f mm3", stats.VolumeMM3)
					}
				}
			}
		})
	}
}

func TestLiveModelIRSteps0To2(t *testing.T) {
	data, err := os.ReadFile("../../model_ir.json")
	if err != nil {
		t.Skip("model_ir.json not present; skipping Step 0-2 regression test")
	}
	var model cadir.Model
	if err := json.Unmarshal(data, &model); err != nil {
		t.Fatalf("unmarshal model_ir.json: %v", err)
	}

	client := startLiveClientOrSkip(t)
	comp := NewCompiler(client)
	results, err := comp.CompileModelWithOptions(&model, CompileOptions{
		Verify:           true,
		UntilStep:        2,
		StepFrom:         -1,
		StopOnDivergence: true,
	})
	if err != nil {
		t.Fatalf("CompileModelWithOptions(UntilStep=2) failed: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 verified checkpoints for Steps 0-2, got %d", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("checkpoint %s failed: %s", r.StepName, r.Message)
		}
	}
}
