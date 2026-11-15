# Sketch Preconditioning Reduction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Empirically determine which of the three sketch preconditioning workarounds (`SeparateIsolatedTangentCircles`, `SplitArcsAtCircles`, `BridgeOpenCollinearGaps`) are still required on SolidCraft 0.42.0 without running a full 96-step model conversion, and remove those that are no longer needed.

**Architecture:** Build a fast, isolated live-MCP test harness in `pkg/solidcraft` that launches `solidcraft-cli mcp --headless` and tests each preconditioner in a fresh 1-sketch (or 1-sketch + 1-extrude) design—first on minimal synthetic geometry, then on only the affected sketches extracted from `model_ir.json`. Use the resulting pass/fail matrix to delete obsoleted preconditioners from `pkg/solidcraft/compiler.go` and `pkg/cadir/precondition.go`.

**Tech Stack:** Go 1.x, `solidcraft-cli` (0.42.0+) MCP server (`mcp --headless`), `pkg/cadir`, `pkg/solidcraft`.

## Global Constraints

- Never push directly to `main`; develop on a feature branch in a `.worktrees` worktree.
- Never place design-specific literals (e.g., `"Socket"`, `"connectors"`, `"bottom_covers"`, `"Sketch1"`) in non-test Go files under `pkg/` (enforced by `TestNoDesignSpecificLiterals` in `pkg/solidcraft/compiler_test.go`).
- Preserve `cadir.NormalizeArc` and `cadir.PlaneBasis` in `pkg/cadir/precondition.go` (they are format/coordinate conversions, not kernel workarounds).
- Live SolidCraft tests must skip cleanly (`t.Skip`) when `solidcraft-cli` is not on `$PATH`.

---

### Task 1: Isolated Live SolidCraft Probe for the 3 Preconditioners

**Files:**
- Create: `pkg/solidcraft/precondition_live_test.go`

**Interfaces:**
- Consumes: `solidcraft.NewClient`, `Client.Start`, `Client.Close`, `Client.NewDesign`, `Client.Batch`, `Client.Execute`, `Client.CallTool`, `Client.Measure` (`pkg/solidcraft/client.go`).
- Produces: `TestLiveSketchPreconditioning` and helper `execSketchAndInspect` in `pkg/solidcraft/precondition_live_test.go`.

- [ ] **Step 1: Write the isolated live probe test**

Create `pkg/solidcraft/precondition_live_test.go`:

```go
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
	client := NewClient()
	if err := client.Start(); err != nil {
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
	cmds := []Command{
		{ID: "sketch.create", Params: SketchCreateParams{Name: s.Name, Plane: s.Plane}},
	}
	for _, c := range s.Curves {
		switch c.Type {
		case cadir.CurveTypeCircle:
			cmds = append(cmds, Command{
				ID:     "sketch.circle.center",
				Params: SketchCircleCenterParams{Center: []float64{c.Center.X, c.Center.Y}, Radius: c.Radius, Construction: c.Construction},
			})
		case cadir.CurveTypeLine:
			cmds = append(cmds, Command{
				ID:     "sketch.line",
				Params: SketchLineParams{Points: [][]float64{{c.Start.X, c.Start.Y}, {c.End.X, c.End.Y}}, Construction: c.Construction},
			})
		case cadir.CurveTypeArc:
			st, en := cadir.NormalizeArc(c.Center, c.Start, c.End, c.MidPoint)
			cmds = append(cmds, Command{
				ID:     "sketch.arc.center_point",
				Params: SketchArcCenterPointParams{Center: []float64{c.Center.X, c.Center.Y}, Start: []float64{st.X, st.Y}, End: []float64{en.X, en.Y}, Construction: c.Construction},
			})
		}
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
```

- [ ] **Step 2: Run the isolated probe test**

Run: `go test -v ./pkg/solidcraft -run TestLiveSketchPreconditioning`
Expected: Outputs exact profile counts, areas, and extrude success/failure for each of the 3 preconditioners on SolidCraft 0.42.0.

- [ ] **Step 3: Commit the probe test**

```bash
git add pkg/solidcraft/precondition_live_test.go
git commit -m "test(solidcraft): add isolated live probe for sketch preconditioners"
```

---

### Task 2: Real-IR Affected-Sketch A/B Test (`model_ir.json`)

**Files:**
- Modify: `pkg/solidcraft/precondition_live_test.go`

**Interfaces:**
- Consumes: `cadir.Model`, `cadir.SeparateIsolatedTangentCircles`, `cadir.SplitArcsAtCircles`, `cadir.BridgeOpenCollinearGaps`, `Compiler.compileAndExecuteSketch` (`pkg/solidcraft/compiler.go`).
- Produces: `TestLiveModelIRSketchesWithoutPreconditioning` in `pkg/solidcraft/precondition_live_test.go`.

- [ ] **Step 1: Add the affected-sketch A/B test to `pkg/solidcraft/precondition_live_test.go`**

Append to `pkg/solidcraft/precondition_live_test.go` (add `"os"` and `"reflect"` to imports):

```go
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
		afterTangent := cadir.SeparateIsolatedTangentCircles(sk.Curves)
		afterSplit := cadir.SplitArcsAtCircles(afterTangent)
		afterBridge := cadir.BridgeOpenCollinearGaps(afterSplit)

		changedTangent := !reflect.DeepEqual(sk.Curves, afterTangent)
		changedSplit := !reflect.DeepEqual(afterTangent, afterSplit)
		changedBridge := !reflect.DeepEqual(afterSplit, afterBridge)
		if !changedTangent && !changedSplit && !changedBridge {
			continue
		}

		t.Run(sk.Name, func(t *testing.T) {
			t.Logf("Step %d (%s) modified by: tangent=%v split=%v bridge=%v",
				step.Index, sk.Name, changedTangent, changedSplit, changedBridge)

			// 1. Run WITH all preconditioners (baseline)
			if err := client.NewDesign(); err != nil {
				t.Fatalf("NewDesign: %v", err)
			}
			comp := NewCompiler(client)
			if err := comp.compileAndExecuteSketch(sk); err != nil {
				t.Fatalf("baseline compileAndExecuteSketch failed: %v", err)
			}
			baseline := inspectLastSketch(t, client)

			// 2. Run WITHOUT any of the 3 preconditioners (raw curves, NormalizeArc only)
			if err := client.NewDesign(); err != nil {
				t.Fatalf("NewDesign: %v", err)
			}
			if _, err := client.Batch(buildSketchCmds(sk)); err != nil {
				t.Fatalf("raw Batch failed: %v", err)
			}
			raw := inspectLastSketch(t, client)

			t.Logf("  baseline=(%d profiles, %.3f mm2) vs raw=(%d profiles, %.3f mm2)",
				baseline.ProfileCount, baseline.TotalAreaMM2, raw.ProfileCount, raw.TotalAreaMM2)
			if baseline.ProfileCount != raw.ProfileCount || math.Abs(baseline.TotalAreaMM2-raw.TotalAreaMM2) > 0.5 {
				t.Errorf("sketch %s differs without preconditioners: baseline=%+v raw=%+v", sk.Name, baseline, raw)
			}
		})
	}
}
```

Note: `buildSketchCmds` in Task 1 should also emit splines (copied from `compileAndExecuteSketch` lines 543–584 of `pkg/solidcraft/compiler.go`) so real IR sketches with splines (e.g. `BodyOutline`, `connectors`, `bottom_covers`, `foam_template`) execute identically. Update `buildSketchCmds` to include the spline loop and `sketch.loose_ends.tie` when `hasSplines` is true.

- [ ] **Step 2: Run the real-IR affected-sketch A/B test**

Run: `go test -v ./pkg/solidcraft -run TestLiveModelIRSketchesWithoutPreconditioning`
Expected: Runs only the sketches in `model_ir.json` that are actually touched by the 3 preconditioners (typically in seconds) and reports exact profile count and area parity for each.

- [ ] **Step 3: Commit the A/B test**

```bash
git add pkg/solidcraft/precondition_live_test.go
git commit -m "test(solidcraft): add A/B test for model_ir sketches without preconditioning"
```

---

### Task 3: Remove Obsoleted Preconditioners & Update Audit Doc

**Files:**
- Modify: `pkg/solidcraft/compiler.go:469-478`
- Modify: `pkg/cadir/precondition.go`
- Modify: `pkg/cadir/precondition_test.go`
- Modify: `docs/workarounds-audit-v0.42.md`

**Interfaces:**
- Consumes: Empirical pass/fail results from Task 1 and Task 2.
- Produces: Cleaned `compileAndExecuteSketch` in `pkg/solidcraft/compiler.go` and cleaned `pkg/cadir/precondition.go`.

- [ ] **Step 1: Remove each preconditioner that passed both Task 1 and Task 2**

In `pkg/solidcraft/compiler.go` (`compileAndExecuteSketch`, lines 469–477), remove the call(s) to every preconditioner whose Task 1 probe AND Task 2 real-IR A/B test passed without regression. For any preconditioner that still failed in Task 1 or Task 2, keep its call in `compileAndExecuteSketch` and document the exact failure in `docs/workarounds-audit-v0.42.md`.

For example, if `SplitArcsAtCircles` is proven unnecessary, remove:
```go
	curves = cadir.SplitArcsAtCircles(curves)
```
and delete `CircleArcIntersections`, `SplitArcsAtCircles`, and their tests (`TestCircleArcIntersections`, `TestSplitArcsAtCircles_*`) from `pkg/cadir/precondition.go` and `pkg/cadir/precondition_test.go`. Apply the exact same deletion rule to `SeparateIsolatedTangentCircles` and `BridgeOpenCollinearGaps` if their Task 1 + Task 2 checks pass.

- [ ] **Step 2: Run full unit tests and live preconditioner tests**

Run: `go test -v ./pkg/cadir/... ./pkg/solidcraft/...`
Expected: `PASS` across all packages, including `TestNoDesignSpecificLiterals` and `TestLiveModelIRSketchesWithoutPreconditioning`.

- [ ] **Step 3: Update `docs/workarounds-audit-v0.42.md` with determination results**

Record the exact SolidCraft `0.42.0` test outcomes (profile counts, areas, extrude results) for each of the 3 sketch preconditioners in `docs/workarounds-audit-v0.42.md`.

- [ ] **Step 4: Commit the cleanup**

```bash
git add pkg/solidcraft/compiler.go pkg/cadir/precondition.go pkg/cadir/precondition_test.go docs/workarounds-audit-v0.42.md
git commit -m "refactor(cadir,compiler): remove sketch preconditioners obsoleted in SolidCraft 0.42"
```

---

## Self-Review

1. **Plan coverage:** Covers all 3 sketch preconditioning workarounds (`SeparateIsolatedTangentCircles`, `SplitArcsAtCircles`, `BridgeOpenCollinearGaps`), tests against actual `solidcraft-cli 0.42.0`, avoids converting the entire 96-step model, and ends with cleanup + verification.
2. **No placeholders:** Every file path, function signature, test body, and `go test` command is concrete.
3. **Type consistency:** `cadir.Sketch`, `cadir.Curve`, `cadir.Point2D`, `SolidExtrudeParams`, `NewCompiler`, and `NewClient` match existing definitions in `pkg/cadir/ast.go`, `pkg/solidcraft/commands.go`, `pkg/solidcraft/compiler.go`, and `pkg/solidcraft/client.go`.
