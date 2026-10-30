# Design-Agnostic Workarounds Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace all design-specific (name-keyed) workarounds in the Fusion→SolidCraft translator with state-driven reconciliation (Fusion body snapshots) plus universal sketch algorithms, and add a `--skip-steps` diagnostic flag.

**Architecture:** The extractor records the complete Fusion body inventory (name, volume, area, centroid, bbox) after every timeline step. After each executed step the compiler geometrically matches SolidCraft's actual bodies to that snapshot and renames them collision-safely to Fusion's names, making all IR body-name references resolve by construction. Sketch-geometry kernel quirks are compensated by universal algorithms (coordinate snapping, arc splitting at circle intersections, degenerate-profile skipping, size-aware profile matching, claim-based compound matching). Spec: `docs/superpowers/specs/2026-10-29-generic-workarounds-design.md`.

**Tech Stack:** Go 1.x (module `github.com/jrussell/fusion2solidcraft`), Fusion 360 MCP (script execution), SolidCraft MCP (`solidcraft-cli mcp` over stdio JSON-RPC).

## Global Constraints

- Every step and extrude distance is in millimeters; sketch coordinates in millimeters (extractor multiplies Fusion cm by 10).
- No design-specific literals in non-test `pkg/**/*.go` files: `Extrude56`, `Extrude57`, `Extrude59`, `Extrude60`, `Corne36`, `Xiao_`, `McuCover`, `MCULid`, `usbc`, `Socket`, `connectors`, `bottom_covers`, `Sketch1` (enforced by a test in Task 10).
- TDD for all pure logic (anything testable without a live MCP server). MCP-dependent changes are verified by `go build ./...`, a live probe where specified, and the final regression run.
- Commit after every task with a conventional message (`feat:`, `fix:`, `test:`).
- Existing uncommitted changes in `pkg/solidcraft/compiler.go` are prior in-progress work already on disk; build on top of them, do not revert them.
- Work already isolated in git worktree `.worktrees/init-pipeline` (branch `feature/init-pipeline`).

---

### Task 1: IR schema + extractor body snapshots

**Files:**
- Modify: `pkg/cadir/ast.go`
- Modify: `pkg/fusion/extractor.go` (the per-step `target_stats` block, lines ~905-937)

**Interfaces:**
- Consumes: nothing new.
- Produces: `cadir.BodySnapshot` type and `TimelineStep.BodySnapshot []BodySnapshot` field (`json:"body_snapshot,omitempty"`); extractor emits `"body_snapshot"` per timeline step. Later tasks consume `step.BodySnapshot`.

- [ ] **Step 1: Add `BodySnapshot` to `pkg/cadir/ast.go`**

Insert after the `VerificationStats` struct (before `TimelineStep`):

```go
// BodySnapshot records one body's state in the source CAD system immediately
// after a timeline step executes.
type BodySnapshot struct {
	Name      string  `json:"name"`
	VolumeMM3 float64 `json:"volume_mm3"`
	AreaMM2   float64 `json:"area_mm2"`
	Centroid  Point3D `json:"centroid"`
	BBoxMin   Point3D `json:"bbox_min"`
	BBoxMax   Point3D `json:"bbox_max"`
}
```

Add one field to `TimelineStep` (after `TargetStats`):

```go
	BodySnapshot []BodySnapshot `json:"body_snapshot,omitempty"`
```

- [ ] **Step 2: Emit `body_snapshot` in the extractor script**

In `pkg/fusion/extractor.go`, replace the block starting with `try:` that computes `total_vol`/`total_area`/`min_pt`/`max_pt` (the one ending by setting `step_data["target_stats"]`) with:

```python
        try:
            total_vol = 0.0
            total_area = 0.0
            min_pt = [1e9, 1e9, 1e9]
            max_pt = [-1e9, -1e9, -1e9]
            body_count = 0
            body_snap = []
            all_b = list(root.bRepBodies)
            for occ in root.occurrences:
                try:
                    all_b.extend(list(occ.bRepBodies))
                except Exception:
                    pass
            for b in all_b:
                if b.isValid:
                    body_count += 1
                    bb = b.boundingBox
                    vol = b.volume * 1000.0
                    area = b.area * 100.0
                    total_vol += vol
                    total_area += area
                    min_pt[0] = min(min_pt[0], bb.minPoint.x * 10)
                    min_pt[1] = min(min_pt[1], bb.minPoint.y * 10)
                    min_pt[2] = min(min_pt[2], bb.minPoint.z * 10)
                    max_pt[0] = max(max_pt[0], bb.maxPoint.x * 10)
                    max_pt[1] = max(max_pt[1], bb.maxPoint.y * 10)
                    max_pt[2] = max(max_pt[2], bb.maxPoint.z * 10)
                    cx = (bb.minPoint.x + bb.maxPoint.x) / 2.0 * 10
                    cy = (bb.minPoint.y + bb.maxPoint.y) / 2.0 * 10
                    cz = (bb.minPoint.z + bb.maxPoint.z) / 2.0 * 10
                    try:
                        com = b.physicalProperties.centerOfMass
                        cx, cy, cz = com.x * 10, com.y * 10, com.z * 10
                    except Exception:
                        pass
                    body_snap.append({
                        "name": b.name,
                        "volume_mm3": vol,
                        "area_mm2": area,
                        "centroid": {"x": cx, "y": cy, "z": cz},
                        "bbox_min": {"x": bb.minPoint.x * 10, "y": bb.minPoint.y * 10, "z": bb.minPoint.z * 10},
                        "bbox_max": {"x": bb.maxPoint.x * 10, "y": bb.maxPoint.y * 10, "z": bb.maxPoint.z * 10}
                    })
            if body_count > 0:
                step_data["body_snapshot"] = body_snap
                step_data["target_stats"] = {
                    "volume_mm3": total_vol,
                    "area_mm2": total_area,
                    "bbox_min": {"x": min_pt[0], "y": min_pt[1], "z": min_pt[2]},
                    "bbox_max": {"x": max_pt[0], "y": max_pt[1], "z": max_pt[2]}
                }
        except Exception:
            pass
```

- [ ] **Step 3: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add pkg/cadir/ast.go pkg/fusion/extractor.go
git commit -m "feat(extractor): record per-body Fusion state snapshot after each timeline step"
```

---

### Task 2: Geometric body matcher + collision-safe rename planner (TDD)

**Files:**
- Create: `pkg/cadir/snapshot.go`
- Test: `pkg/cadir/snapshot_test.go`

**Interfaces:**
- Consumes: `cadir.BodySnapshot` (Task 1), `cadir.Point3D`, `cadir.Distance3D`.
- Produces: `cadir.MatchBodies(actual, expected []BodySnapshot) map[int]int` (actual index → expected index); `cadir.PlanRenames(current []string, desired map[string]string) [][2]string` (ordered from→to pairs). Task 6 consumes both.

- [ ] **Step 1: Write failing tests in `pkg/cadir/snapshot_test.go`**

```go
package cadir

import (
	"reflect"
	"testing"
)

func snapBody(name string, vol float64, cx, cy, cz float64) BodySnapshot {
	return BodySnapshot{
		Name:      name,
		VolumeMM3: vol,
		BBoxMin:   Point3D{X: cx - 10, Y: cy - 10, Z: cz - 10},
		BBoxMax:   Point3D{X: cx + 10, Y: cy + 10, Z: cz + 10},
	}
}

func TestMatchBodies_PairsByVolumeAndCenter(t *testing.T) {
	actual := []BodySnapshot{
		snapBody("Body1", 1000, 0, 0, 0),
		snapBody("Body2", 500, 100, 0, 0),
	}
	expected := []BodySnapshot{
		snapBody("Lid", 500, 100, 0, 0),
		snapBody("Main", 1000, 0, 0, 0),
	}
	m := MatchBodies(actual, expected)
	if m[0] != 1 || m[1] != 0 {
		t.Fatalf("expected 0->1, 1->0; got %v", m)
	}
}

func TestMatchBodies_RejectsVolumeMismatch(t *testing.T) {
	actual := []BodySnapshot{snapBody("Body1", 2000, 0, 0, 0)}
	expected := []BodySnapshot{snapBody("Main", 1000, 0, 0, 0)}
	if m := MatchBodies(actual, expected); len(m) != 0 {
		t.Fatalf("expected no match for 2x volume difference, got %v", m)
	}
}

func TestMatchBodies_NoDoubleAssignment(t *testing.T) {
	actual := []BodySnapshot{snapBody("A", 1000, 0, 0, 0)}
	expected := []BodySnapshot{
		snapBody("X", 1000, 0, 0, 0),
		snapBody("Y", 1000, 0, 0.5, 0),
	}
	m := MatchBodies(actual, expected)
	if len(m) != 1 {
		t.Fatalf("one actual body must match at most one expected, got %v", m)
	}
}

func TestPlanRenames_NoCollision(t *testing.T) {
	plan := PlanRenames([]string{"A", "B"}, map[string]string{"A": "C"})
	if !reflect.DeepEqual(plan, [][2]string{{"A", "C"}}) {
		t.Fatalf("unexpected plan %v", plan)
	}
}

func TestPlanRenames_ChainOrdersAwayFirst(t *testing.T) {
	// B must be renamed to C first, freeing B for A.
	plan := PlanRenames([]string{"A", "B", "C"}, map[string]string{"A": "B", "B": "C"})
	want := [][2]string{{"B", "C"}, {"A", "B"}}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("expected %v, got %v", want, plan)
	}
}

func TestPlanRenames_SwapUsesTempName(t *testing.T) {
	plan := PlanRenames([]string{"A", "B"}, map[string]string{"A": "B", "B": "A"})
	if len(plan) != 3 {
		t.Fatalf("expected 3 moves for a swap (one via temp), got %v", plan)
	}
	// Validate by simulation: applying the plan never assigns a live name.
	live := map[string]bool{"A": true, "B": true}
	bodies := map[string]string{"A": "A", "B": "B"}
	for _, mv := range plan {
		if live[mv[1]] {
			t.Fatalf("rename %v assigns a still-live name", mv)
		}
		bodies[mv[0]] = mv[1]
		live[mv[0]] = false
		live[mv[1]] = true
	}
	if bodies["A"] != "B" || bodies["B"] != "A" {
		t.Fatalf("swap not completed: %v", bodies)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/cadir/ -run 'TestMatchBodies|TestPlanRenames' -v`
Expected: FAIL — `undefined: MatchBodies` / `undefined: PlanRenames`.

- [ ] **Step 3: Implement `pkg/cadir/snapshot.go`**

```go
package cadir

import (
	"fmt"
	"math"
	"sort"
)

const (
	maxMatchRelVolDiff  = 0.05 // relative volume difference tolerated for a match
	maxMatchCenterDist  = 1.0  // bbox-center distance (mm) tolerated for a match
)

func bboxCenter(b BodySnapshot) Point3D {
	return Point3D{
		X: (b.BBoxMin.X + b.BBoxMax.X) / 2,
		Y: (b.BBoxMin.Y + b.BBoxMax.Y) / 2,
		Z: (b.BBoxMin.Z + b.BBoxMax.Z) / 2,
	}
}

// MatchBodies greedily pairs actual bodies with expected snapshots by geometry:
// relative volume difference <= 5% and bbox-center distance <= 1 mm qualify a
// pair; qualifying pairs are consumed best-score first. Returns actual index
// -> expected index. Bodies left unmatched are simply absent from the result.
func MatchBodies(actual, expected []BodySnapshot) map[int]int {
	type candidate struct {
		ai, ei int
		score  float64
	}
	var cands []candidate
	for ai, a := range actual {
		for ei, ex := range expected {
			rel := math.Abs(a.VolumeMM3-ex.VolumeMM3) / math.Max(math.Abs(ex.VolumeMM3), 1e-9)
			d := Distance3D(bboxCenter(a), bboxCenter(ex))
			if rel > maxMatchRelVolDiff || d > maxMatchCenterDist {
				continue
			}
			cands = append(cands, candidate{ai: ai, ei: ei, score: rel + d})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score < cands[j].score })
	matches := make(map[int]int)
	usedActual := make(map[int]bool, len(actual))
	usedExpected := make(map[int]bool, len(expected))
	for _, c := range cands {
		if usedActual[c.ai] || usedExpected[c.ei] {
			continue
		}
		usedActual[c.ai] = true
		usedExpected[c.ei] = true
		matches[c.ai] = c.ei
	}
	return matches
}

// PlanRenames returns the ordered set of renames (from, to) that transforms
// the current body names into the desired assignment without ever assigning a
// name that is still live. desired maps a current body name to its target
// name; identities and unknown sources are ignored. Cycles are broken with
// temporary names ("__rename_tmp_N").
func PlanRenames(current []string, desired map[string]string) [][2]string {
	live := make(map[string]bool, len(current))
	for _, n := range current {
		live[n] = true
	}
	pending := make(map[string]string, len(desired))
	for from, to := range desired {
		if from != to && live[from] {
			pending[from] = to
		}
	}
	var plan [][2]string
	tmpCount := 0
	for len(pending) > 0 {
		progressed := false
		for from, to := range pending {
			if !live[to] {
				plan = append(plan, [2]string{from, to})
				delete(pending, from)
				live[from] = false
				live[to] = true
				progressed = true
			}
		}
		if !progressed && len(pending) > 0 {
			// Cycle: park one body under a temp name to break it.
			for from, to := range pending {
				tmp := fmt.Sprintf("__rename_tmp_%d", tmpCount)
				tmpCount++
				plan = append(plan, [2]string{from, tmp})
				live[from] = false
				live[tmp] = true
				delete(pending, from)
				pending[tmp] = to
				progressed = true
				break
			}
		}
		if !progressed {
			break // defensive: should not happen
		}
	}
	return plan
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/cadir/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cadir/snapshot.go pkg/cadir/snapshot_test.go
git commit -m "feat(cadir): geometric body matching and collision-safe rename planning"
```

---

### Task 3: Universal arc splitting at circle intersections (TDD)

**Files:**
- Modify: `pkg/cadir/precondition.go` (add function; add `"sort"` import)
- Test: `pkg/cadir/precondition_test.go` (append tests)

**Interfaces:**
- Consumes: `cadir.Curve`, `cadir.NormalizeArc`, `cadir.CircleArcIntersections` (note: `CircleArcIntersections` expects the circle centered at the origin — we translate).
- Produces: `cadir.SplitArcsAtCircles(curves []Curve) []Curve`. Task 4 consumes it.

- [ ] **Step 1: Write failing tests (append to `pkg/cadir/precondition_test.go`)**

```go
func TestSplitArcsAtCircles_SplitsCrossingArc(t *testing.T) {
	// Circle centered at origin, radius 5. Arc centered at (5,0), radius 5,
	// running CCW from (10,0) through (5,5) to (0,0). The circle intersects
	// the arc at (2.5, 4.33) (inside the span) and (2.5,-4.33) (outside).
	curves := []Curve{
		{ID: "c1", Type: CurveTypeCircle, Center: Point2D{X: 0, Y: 0}, Radius: 5},
		{ID: "a1", Type: CurveTypeArc, Center: Point2D{X: 5, Y: 0}, Radius: 5,
			Start: Point2D{X: 10, Y: 0}, End: Point2D{X: 0, Y: 0}, MidPoint: Point2D{X: 5, Y: 5}},
	}
	out := SplitArcsAtCircles(curves)
	if len(out) != 3 { // 1 circle + 2 sub-arcs
		t.Fatalf("expected 3 curves, got %d", len(out))
	}
	sub1, sub2 := out[1], out[2]
	if Distance2D(sub1.Start, Point2D{X: 10, Y: 0}) > 1e-6 {
		t.Fatalf("first sub-arc must start at (10,0), got %+v", sub1.Start)
	}
	if Distance2D(sub1.End, Point2D{X: 2.5, Y: 4.330127}) > 1e-4 {
		t.Fatalf("first sub-arc must end at the intersection, got %+v", sub1.End)
	}
	if Distance2D(sub2.Start, Point2D{X: 2.5, Y: 4.330127}) > 1e-4 ||
		Distance2D(sub2.End, Point2D{X: 0, Y: 0}) > 1e-6 {
		t.Fatalf("second sub-arc endpoints wrong: %+v -> %+v", sub2.Start, sub2.End)
	}
	// Sub-arc midpoints must re-normalize to themselves (CCW preserved).
	ns, ne := NormalizeArc(sub1.Center, sub1.Start, sub1.End, sub1.MidPoint)
	if Distance2D(ns, sub1.Start) > 1e-6 || Distance2D(ne, sub1.End) > 1e-6 {
		t.Fatalf("sub-arc midpoint orientation broken: %+v -> %+v", ns, ne)
	}
}

func TestSplitArcsAtCircles_LeavesDisjointArcAlone(t *testing.T) {
	curves := []Curve{
		{ID: "c1", Type: CurveTypeCircle, Center: Point2D{X: 0, Y: 0}, Radius: 5},
		{ID: "a1", Type: CurveTypeArc, Center: Point2D{X: 50, Y: 0}, Radius: 5,
			Start: Point2D{X: 55, Y: 0}, End: Point2D{X: 45, Y: 0}, MidPoint: Point2D{X: 50, Y: 5}},
	}
	out := SplitArcsAtCircles(curves)
	if len(out) != 2 || out[1].ID != "a1" {
		t.Fatalf("disjoint arc must pass through unchanged, got %d curves", len(out))
	}
}

func TestSplitArcsAtCircles_EndpointTouchDoesNotSplit(t *testing.T) {
	// Circle radius 5 at origin; arc from (5,0) to (0,5) centered at origin:
	// its endpoints lie ON the circle, so no interior split occurs.
	curves := []Curve{
		{ID: "c1", Type: CurveTypeCircle, Center: Point2D{X: 0, Y: 0}, Radius: 5},
		{ID: "a1", Type: CurveTypeArc, Center: Point2D{X: 0, Y: 0}, Radius: 5,
			Start: Point2D{X: 5, Y: 0}, End: Point2D{X: 0, Y: 5}, MidPoint: Point2D{X: 3.5355339, Y: 3.5355339}},
	}
	out := SplitArcsAtCircles(curves)
	if len(out) != 2 {
		t.Fatalf("arc touching circle only at endpoints must not split, got %d curves", len(out))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/cadir/ -run TestSplitArcsAtCircles -v`
Expected: FAIL — `undefined: SplitArcsAtCircles`.

- [ ] **Step 3: Implement in `pkg/cadir/precondition.go`**

Add `"sort"` to the import block, then append:

```go
// SplitArcsAtCircles returns the curve list with every arc split at its
// intersections with any circle in the same sketch. Only intersections lying
// strictly inside the arc's CCW span and away from its endpoints cause a
// split; arcs without such intersections pass through unchanged. Sub-arc
// midpoints are recomputed so NormalizeArc keeps each sub-arc's orientation.
func SplitArcsAtCircles(curves []Curve) []Curve {
	var circles []Curve
	for _, c := range curves {
		if c.Type == CurveTypeCircle {
			circles = append(circles, c)
		}
	}
	if len(circles) == 0 {
		return curves
	}
	out := make([]Curve, 0, len(curves))
	for _, c := range curves {
		if c.Type != CurveTypeArc {
			out = append(out, c)
			continue
		}
		start, end := NormalizeArc(c.Center, c.Start, c.End, c.MidPoint)
		angStart := math.Atan2(start.Y-c.Center.Y, start.X-c.Center.X)
		angEnd := math.Atan2(end.Y-c.Center.Y, end.X-c.Center.X)
		if angStart < 0 {
			angStart += 2 * math.Pi
		}
		if angEnd < 0 {
			angEnd += 2 * math.Pi
		}
		span := angEnd - angStart
		if span < 0 {
			span += 2 * math.Pi
		}
		type splitPoint struct {
			rel float64
			pt  Point2D
		}
		var splits []splitPoint
		for _, circle := range circles {
			// Translate so the circle sits at the origin, as the solver expects.
			shifted := Point2D{X: c.Center.X - circle.Center.X, Y: c.Center.Y - circle.Center.Y}
			for _, p := range CircleArcIntersections(circle.Radius, shifted, c.Radius) {
				world := Point2D{X: p.X + circle.Center.X, Y: p.Y + circle.Center.Y}
				if Distance2D(world, start) < 0.05 || Distance2D(world, end) < 0.05 {
					continue
				}
				ang := math.Atan2(world.Y-c.Center.Y, world.X-c.Center.X)
				if ang < 0 {
					ang += 2 * math.Pi
				}
				rel := ang - angStart
				if rel < 0 {
					rel += 2 * math.Pi
				}
				if rel <= 1e-9 || rel >= span-1e-9 {
					continue
				}
				dup := false
				for _, s := range splits {
					if Distance2D(s.pt, world) < 0.05 {
						dup = true
						break
					}
				}
				if !dup {
					splits = append(splits, splitPoint{rel: rel, pt: world})
				}
			}
		}
		if len(splits) == 0 {
			out = append(out, c)
			continue
		}
		sort.Slice(splits, func(i, j int) bool { return splits[i].rel < splits[j].rel })
		bounds := make([]Point2D, 0, len(splits)+2)
		bounds = append(bounds, start)
		for _, s := range splits {
			bounds = append(bounds, s.pt)
		}
		bounds = append(bounds, end)
		rels := make([]float64, 0, len(bounds))
		rels = append(rels, 0)
		for _, s := range splits {
			rels = append(rels, s.rel)
		}
		rels = append(rels, span)
		for i := 0; i+1 < len(bounds); i++ {
			sub := c
			sub.Start = bounds[i]
			sub.End = bounds[i+1]
			midAng := angStart + (rels[i]+rels[i+1])/2
			sub.MidPoint = Point2D{
				X: c.Center.X + c.Radius*math.Cos(midAng),
				Y: c.Center.Y + c.Radius*math.Sin(midAng),
			}
			out = append(out, sub)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/cadir/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cadir/precondition.go pkg/cadir/precondition_test.go
git commit -m "feat(cadir): universal arc splitting at circle intersections"
```

---

### Task 4: Compiler sketches — universal snapping + arc splitting

**Files:**
- Modify: `pkg/solidcraft/compiler.go` (`compileAndExecuteSketch`, lines ~350-531)

**Interfaces:**
- Consumes: `cadir.SplitArcsAtCircles` (Task 3).
- Produces: `snapCoord(v float64) float64` (unexported, reused nowhere else). Sketch commands are identical in shape; only coordinate treatment changes.

- [ ] **Step 1: Replace `compileAndExecuteSketch`**

Replace the entire function with:

```go
// snapCoord snaps a sketch coordinate to a 1e-4 mm grid so that coincident
// and tangential geometry is exactly coincident for the SolidCraft kernel.
func snapCoord(v float64) float64 {
	return math.Round(v*1e4) / 1e4
}

func (c *Compiler) compileAndExecuteSketch(s cadir.Sketch) error {
	var cmds []Command

	// Universal pre-conditioning: split arcs at circle intersections so SolidCraft
	// region detection sees the same trimmed curve arrangement Fusion used.
	curves := cadir.SplitArcsAtCircles(s.Curves)

	// 1. sketch.create
	plane := s.Plane
	if planeStr, ok := plane.(string); ok {
		if planeStr != "XY" && planeStr != "XZ" && planeStr != "YZ" {
			plane = "XY"
		}
	} else if plane == nil {
		plane = "XY"
	}
	cmds = append(cmds, Command{
		ID: "sketch.create",
		Params: SketchCreateParams{
			Name:  s.Name,
			Plane: plane,
		},
	})

	// 2. Draw circles
	for _, curve := range curves {
		if curve.Type == cadir.CurveTypeCircle && curve.Radius > 0 {
			cmds = append(cmds, Command{
				ID: "sketch.circle.center",
				Params: SketchCircleCenterParams{
					Center:       []float64{snapCoord(curve.Center.X), snapCoord(curve.Center.Y)},
					Radius:       snapCoord(curve.Radius),
					Construction: curve.Construction,
				},
			})
		}
	}

	// 3. Draw lines
	for _, curve := range curves {
		if curve.Type == cadir.CurveTypeLine {
			cmds = append(cmds, Command{
				ID: "sketch.line",
				Params: SketchLineParams{
					Points: [][]float64{
						{snapCoord(curve.Start.X), snapCoord(curve.Start.Y)},
						{snapCoord(curve.End.X), snapCoord(curve.End.Y)},
					},
					Construction: curve.Construction,
				},
			})
		}
	}

	// 4. Draw arcs (CCW-normalized)
	for _, curve := range curves {
		if curve.Type == cadir.CurveTypeArc {
			start, end := cadir.NormalizeArc(curve.Center, curve.Start, curve.End, curve.MidPoint)
			cmds = append(cmds, Command{
				ID: "sketch.arc.center_point",
				Params: SketchArcCenterPointParams{
					Center:       []float64{snapCoord(curve.Center.X), snapCoord(curve.Center.Y)},
					Start:        []float64{snapCoord(start.X), snapCoord(start.Y)},
					End:          []float64{snapCoord(end.X), snapCoord(end.Y)},
					Construction: curve.Construction,
				},
			})
		}
	}

	// 5. Draw splines
	hasSplines := false
	for _, curve := range curves {
		if curve.Type == cadir.CurveTypeSpline {
			if len(curve.ControlPoints) >= 2 && (curve.Degree == 5 || curve.Degree == 3) {
				var cpts [][]float64
				for _, pt := range curve.ControlPoints {
					cpts = append(cpts, []float64{snapCoord(pt.X), snapCoord(pt.Y)})
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
				pts = append(pts, []float64{snapCoord(pt.X), snapCoord(pt.Y)})
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

	// 6. sketch.finish
	cmds = append(cmds, Command{ID: "sketch.finish"})

	_, err := c.client.Batch(cmds)
	if err != nil {
		return err
	}
	inspOut, iErr := c.client.CallTool("inspect_design", map[string]interface{}{})
	if iErr == nil {
		var designData struct {
			Sketches []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"sketches"`
		}
		if json.Unmarshal([]byte(inspOut), &designData) == nil && len(designData.Sketches) > 0 {
			lastSk := designData.Sketches[len(designData.Sketches)-1]
			c.sketchNameToID[s.Name] = lastSk.ID
		}
	}
	return nil
}
```

Note: the old code emitted the circle-radius precondition command and keyed behavior off `circleRadius`; the universal arc splitting supersedes it, so circles are now emitted in their own loop.

- [ ] **Step 2: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/solidcraft/compiler.go
git commit -m "feat(compiler): universal sketch coordinate snapping and arc splitting"
```

---

### Task 5: Compiler extrude matching — universal rules

**Files:**
- Modify: `pkg/solidcraft/compiler.go` (`compileAndExecuteExtrude` profile-matching section, lines ~916-1089; helper added near `compileMultiBodyExtrude`)

**Interfaces:**
- Consumes: `cadir.ExtrudeProfileInfo` (existing).
- Produces: `isNearestFusionProfile(pt []float64, fi int, profiles []cadir.ExtrudeProfileInfo) bool` (unexported).

- [ ] **Step 1: Remove both `connectors` guards**

In `compileAndExecuteExtrude`, both profile-matching loops contain:

```go
					// Skip degenerate profiles in connectors where any curve ID is repeated
					if e.SketchName == "connectors" {
						curveCounts := make(map[string]int)
						degenerate := false
						for _, cid := range sp.Curves {
							curveCounts[cid]++
							if curveCounts[cid] > 1 {
								degenerate = true
								break
							}
						}
						if degenerate {
							continue
						}
					}
```

Replace **both** occurrences (they differ only in indentation — use unique surrounding context for each edit) with:

```go
					// Skip degenerate profiles: a valid region boundary never repeats a curve.
					{
						curveCounts := make(map[string]int)
						degenerate := false
						for _, cid := range sp.Curves {
							curveCounts[cid]++
							if curveCounts[cid] > 1 {
								degenerate = true
								break
							}
						}
						if degenerate {
							continue
						}
					}
```

- [ ] **Step 2: Size-aware centroid tolerance**

Find:

```go
			if (bestDist < 2.0 || (e.SketchName == "Sketch1" && bestDist < 2.5)) && bestIdx >= 0 {
```

Replace with:

```go
			// Tolerance scales with profile size: small profiles (e.g. keyswitch
			// sockets on a fine grid) allow a few mm of centroid discrepancy,
			// large ones stay near the 2 mm floor, capped for safety.
			tol := math.Min(2.0+math.Sqrt(fp.AreaMM2), 6.0)
			if bestDist < tol && bestIdx >= 0 {
```

- [ ] **Step 3: Claim-based compound matching**

Find the compound-profile branch:

```go
			} else if fp.BBoxMin != nil && fp.BBoxMax != nil && fp.AreaMM2 > 300.0 && e.SketchName != "bottom_covers" {
```

Two changes:

a) Change the loop above it from `for _, fp := range e.Profiles {` to `for fi, fp := range e.Profiles {`.

b) Replace the branch condition with:

```go
			} else if fp.BBoxMin != nil && fp.BBoxMax != nil && fp.AreaMM2 > 300.0 {
```

c) Inside that branch's sub-profile loop, immediately after `if len(sp.CentroidSketch) >= 2 {` add:

```go
								// Claim test: a sub-profile belongs to the nearest Fusion
								// profile only; this prevents regions of adjacent compound
								// profiles (e.g. mirrored pairs in one sketch) from being
								// absorbed by the wrong one.
								if !isNearestFusionProfile(sp.CentroidSketch, fi, e.Profiles) {
									continue
								}
```

Add the helper at package level (e.g. just above `compileMultiBodyExtrude`):

```go
// isNearestFusionProfile reports whether the Fusion profile at index fi is the
// closest Fusion profile (by sketch-space centroid) to the given point.
func isNearestFusionProfile(pt []float64, fi int, profiles []cadir.ExtrudeProfileInfo) bool {
	if len(pt) < 2 || fi < 0 || fi >= len(profiles) {
		return false
	}
	best := -1
	bestDist := math.MaxFloat64
	for i, p := range profiles {
		d := math.Hypot(p.Centroid.X-pt[0], p.Centroid.Y-pt[1])
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	return best == fi
}
```

- [ ] **Step 4: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add pkg/solidcraft/compiler.go
git commit -m "feat(compiler): universal profile matching (degenerate skip, size-aware tolerance, claim test)"
```

---

### Task 6: State reconciliation + skip plumbing

**Files:**
- Modify: `pkg/solidcraft/compiler.go` (`CompileOptions`, `CompileModelWithOptions`, new `reconcileBodies`/`inspectBodies`)

**Interfaces:**
- Consumes: `cadir.MatchBodies`, `cadir.PlanRenames` (Task 2); `step.BodySnapshot` (Task 1).
- Produces: `CompileOptions.SkipSteps map[int]bool` (Task 9 wires the CLI); `c.reconcileBodies(expected []cadir.BodySnapshot) error`; `c.inspectBodies() ([]cadir.BodySnapshot, error)`.

- [ ] **Step 1: Probe `inspect_design` body JSON keys (live, one-off)**

Run this probe (creates a 10x10x5 box in a headless SolidCraft session and dumps its design state):

```bash
printf '%s\n' \
'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"1.0"}}}' \
'{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"new_design","arguments":{}}}' \
'{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"execute","arguments":{"command":"sketch.create","params":{"plane":"XY"}}}}' \
'{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"execute","arguments":{"command":"sketch.rectangle.two_point","params":{"p0":[0,0],"p1":[10,10]}}}}' \
'{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"execute","arguments":{"command":"sketch.finish","params":{}}}}' \
'{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"execute","arguments":{"command":"solid.extrude","params":{"distance":5,"operation":"new"}}}}' \
'{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"inspect_design","arguments":{}}}' \
| solidcraft-cli mcp --allow-any-path 2>/dev/null | tail -1 | python3 -m json.tool | head -80
```

Note the exact key names inside `bodies[]` for name, volume, area, center of mass, and bbox. Adjust the struct tags in Step 2 to match. (Existing compiler code already parses `name`, `bbox.min`, `bbox.max`, `face_count`; expect volume/area/COM keys similar to `volume_mm3`/`area_mm2`/`center_of_mass`.)

- [ ] **Step 2: Add `SkipSteps` to `CompileOptions`**

```go
type CompileOptions struct {
	Verify           bool
	StartStep        int
	UntilStep        int
	StopOnDivergence bool
	Interactive      bool
	StepFrom         int
	SkipSteps        map[int]bool
}
```

- [ ] **Step 3: Wire skip + reconciliation into the replay loop**

In `CompileModelWithOptions`, add before the `for _, step := range model.Timeline` loop:

```go
	skipOccurred := false
```

Immediately after the `if opts.UntilStep >= 0 && step.Index > opts.UntilStep { ... }` block, add:

```go
		if opts.SkipSteps[step.Index] {
			fmt.Printf("Skipping step %d (%s) as requested (--skip-steps); body reconciliation disabled for the rest of this run.\n", step.Index, step.Name)
			skipOccurred = true
			continue
		}
```

Immediately after the `switch step.Type { ... }` block and **before** the `if opts.Verify && step.TargetStats != nil ...` block, add:

```go
		if !skipOccurred && len(step.BodySnapshot) > 0 {
			if err := c.reconcileBodies(step.BodySnapshot); err != nil {
				fmt.Printf("Notice: body reconciliation at step %d failed: %v\n", step.Index, err)
			}
		}
```

- [ ] **Step 4: Implement `reconcileBodies` and `inspectBodies`**

Add near the top of the file (after `hydrateStateFromDesign`):

```go
// reconcileBodies aligns SolidCraft body names with Fusion's post-step state:
// bodies are matched geometrically (volume + bbox center) and mismatched names
// are renamed with collision-safe ordering so IR body-name references written
// at this timeline point resolve correctly afterwards.
func (c *Compiler) reconcileBodies(expected []cadir.BodySnapshot) error {
	if len(expected) == 0 {
		return nil
	}
	actual, err := c.inspectBodies()
	if err != nil {
		return err
	}
	if len(actual) == 0 {
		return nil
	}
	matches := cadir.MatchBodies(actual, expected)
	matchedExpected := make(map[int]bool, len(matches))
	desired := make(map[string]string, len(matches))
	for ai, ei := range matches {
		matchedExpected[ei] = true
		if actual[ai].Name != expected[ei].Name {
			desired[actual[ai].Name] = expected[ei].Name
		}
	}
	for ei, ex := range expected {
		if !matchedExpected[ei] {
			fmt.Printf("Reconcile notice: Fusion body %q (%.1f mm3) has no SolidCraft counterpart\n", ex.Name, ex.VolumeMM3)
		}
	}
	if len(desired) == 0 {
		return nil
	}
	names := make([]string, 0, len(actual))
	for _, b := range actual {
		names = append(names, b.Name)
	}
	for _, mv := range cadir.PlanRenames(names, desired) {
		if err := c.renameBody(mv[0], mv[1]); err != nil {
			fmt.Printf("Notice: reconcile rename %s -> %s failed: %v\n", mv[0], mv[1], err)
			continue
		}
		fmt.Printf("Reconcile: renamed body %s -> %s to match Fusion state\n", mv[0], mv[1])
	}
	return nil
}

// inspectBodies reads the current SolidCraft bodies as geometric snapshots.
func (c *Compiler) inspectBodies() ([]cadir.BodySnapshot, error) {
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var design struct {
		Bodies []struct {
			Name         string    `json:"name"`
			VolumeMM3    float64   `json:"volume_mm3"`
			AreaMM2      float64   `json:"area_mm2"`
			CenterOfMass []float64 `json:"center_of_mass"`
			BBox         struct {
				Min []float64 `json:"min"`
				Max []float64 `json:"max"`
			} `json:"bbox"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal([]byte(inspOut), &design); err != nil {
		return nil, err
	}
	out := make([]cadir.BodySnapshot, 0, len(design.Bodies))
	for _, b := range design.Bodies {
		snap := cadir.BodySnapshot{Name: b.Name, VolumeMM3: b.VolumeMM3, AreaMM2: b.AreaMM2}
		if len(b.CenterOfMass) >= 3 {
			snap.Centroid = cadir.Point3D{X: b.CenterOfMass[0], Y: b.CenterOfMass[1], Z: b.CenterOfMass[2]}
		}
		if len(b.BBox.Min) >= 3 && len(b.BBox.Max) >= 3 {
			snap.BBoxMin = cadir.Point3D{X: b.BBox.Min[0], Y: b.BBox.Min[1], Z: b.BBox.Min[2]}
			snap.BBoxMax = cadir.Point3D{X: b.BBox.Max[0], Y: b.BBox.Max[1], Z: b.BBox.Max[2]}
		}
		out = append(out, snap)
	}
	return out, nil
}
```

(Adjust the three JSON tags per the Step 1 probe if they differ.)

- [ ] **Step 5: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add pkg/solidcraft/compiler.go
git commit -m "feat(compiler): per-step body reconciliation against Fusion snapshots; skip plumbing"
```

---

### Task 7: Snapshot-driven participant merging + generic extrude targets

**Files:**
- Modify: `pkg/solidcraft/compiler.go` (`mergeParticipantBodies`, `compileAndExecuteExtrude` target selection and call sites, `compileAndExecuteSketch` plane bookkeeping, `hydrateStateFromDesign`, new `sweptVolumeTargets`, new `cadir.PlaneBasis`)

**Interfaces:**
- Consumes: `step.BodySnapshot` (Task 1).
- Produces: `compileAndExecuteExtrude(e cadir.ExtrudeFeature, snap []cadir.BodySnapshot) error`; `mergeParticipantBodies(e cadir.ExtrudeFeature, snap []cadir.BodySnapshot, desiredBodyName string)`; `cadir.PlaneBasis(plane interface{}) (origin, xDir, yDir [3]float64, ok bool)`; `c.sweptVolumeTargets(e cadir.ExtrudeFeature) []string`.

- [ ] **Step 1: Add `cadir.PlaneBasis` (TDD)**

Append to `pkg/cadir/precondition_test.go`:

```go
func TestPlaneBasis(t *testing.T) {
	o, x, y, ok := PlaneBasis("XY")
	if !ok || o != [3]float64{0, 0, 0} || x != [3]float64{1, 0, 0} || y != [3]float64{0, 1, 0} {
		t.Fatalf("XY basis wrong: %v %v %v %v", o, x, y, ok)
	}
	_, _, _, ok = PlaneBasis("YZ")
	if !ok {
		t.Fatal("YZ must resolve")
	}
	_, _, _, ok = PlaneBasis(nil)
	if ok {
		t.Fatal("nil plane must not resolve")
	}
	custom := map[string]interface{}{
		"origin": []interface{}{10.0, 20.0, 30.0},
		"x_dir":  []interface{}{0.0, 1.0, 0.0},
		"y_dir":  []interface{}{0.0, 0.0, 1.0},
	}
	o, x, y, ok = PlaneBasis(custom)
	if !ok || o != [3]float64{10, 20, 30} || x != [3]float64{0, 1, 0} || y != [3]float64{0, 0, 1} {
		t.Fatalf("custom plane basis wrong: %v %v %v %v", o, x, y, ok)
	}
}
```

Run `go test ./pkg/cadir/ -run TestPlaneBasis -v` — expect FAIL (`undefined: PlaneBasis`).

Append to `pkg/cadir/precondition.go`:

```go
// PlaneBasis returns the (origin, xDir, yDir) world basis for an IR sketch
// plane value: "XY", "XZ", "YZ", or a map with "origin"/"x_dir"/"y_dir"
// float arrays. ok is false for unknown shapes.
func PlaneBasis(plane interface{}) (origin, xDir, yDir [3]float64, ok bool) {
	switch p := plane.(type) {
	case string:
		switch p {
		case "XY":
			return [3]float64{}, [3]float64{1, 0, 0}, [3]float64{0, 1, 0}, true
		case "XZ":
			return [3]float64{}, [3]float64{1, 0, 0}, [3]float64{0, 0, 1}, true
		case "YZ":
			return [3]float64{}, [3]float64{0, 1, 0}, [3]float64{0, 0, 1}, true
		}
	case map[string]interface{}:
		readVec := func(key string) ([3]float64, bool) {
			raw, present := p[key]
			if !present {
				return [3]float64{}, false
			}
			arr, isArr := raw.([]interface{})
			if !isArr || len(arr) < 3 {
				return [3]float64{}, false
			}
			var v [3]float64
			for i := 0; i < 3; i++ {
				f, isNum := arr[i].(float64)
				if !isNum {
					return [3]float64{}, false
				}
				v[i] = f
			}
			return v, true
		}
		o, okO := readVec("origin")
		x, okX := readVec("x_dir")
		y, okY := readVec("y_dir")
		if okO && okX && okY {
			return o, x, y, true
		}
	}
	return origin, xDir, yDir, false
}
```

Run `go test ./pkg/cadir/ -v` — expect all PASS. Commit:

```bash
git add pkg/cadir/precondition.go pkg/cadir/precondition_test.go
git commit -m "feat(cadir): PlaneBasis helper for IR sketch planes"
```

- [ ] **Step 2: Record sketch planes in the compiler**

Add a field to `Compiler`:

```go
	sketchPlaneByName map[string][3][3]float64 // name -> {origin, xDir, yDir}
```

Initialize in `NewCompiler`:

```go
		sketchPlaneByName: make(map[string][3][3]float64),
```

At the end of `compileAndExecuteSketch`, just before the `inspOut, iErr := ...` block, add:

```go
	if o, xd, yd, ok := cadir.PlaneBasis(s.Plane); ok {
		c.sketchPlaneByName[s.Name] = [3][3]float64{o, xd, yd}
	}
```

In `hydrateStateFromDesign`, inside the `for _, step := range model.Timeline` loop that populates planes, also record sketch planes:

```go
		if step.Index < startStep && step.Type == "sketch" && step.Sketch != nil {
			if o, xd, yd, ok := cadir.PlaneBasis(step.Sketch.Plane); ok {
				c.sketchPlaneByName[step.Sketch.Name] = [3][3]float64{o, xd, yd}
			}
		}
```

- [ ] **Step 3: Rework `mergeParticipantBodies`**

Change the signature and head (replace the current function head including the `Extrude56` early return):

```go
func (c *Compiler) mergeParticipantBodies(e cadir.ExtrudeFeature, snap []cadir.BodySnapshot, desiredBodyName string) {
	if e.Operation != "join" || len(e.ParticipantBodies) <= 1 {
		return
	}
	// Fusion's own post-step snapshot says which participants it actually
	// consumed (merged away): exactly those absent from the snapshot.
	if snap != nil {
		present := make(map[string]bool, len(snap))
		for _, b := range snap {
			present[b.Name] = true
		}
		var consumed []string
		for _, pb := range e.ParticipantBodies {
			if !present[pb] {
				consumed = append(consumed, pb)
			}
		}
		if len(consumed) == 0 {
			return
		}
		e.ParticipantBodies = consumed
	}
	inspAfter, err := c.client.CallTool("inspect_design", map[string]interface{}{})
```

Keep everything from `inspAfter, err := ...` to the end of the function unchanged, and remove the old comment `// Do not merge the small MCU Lid...` line together with the `if e.Name == "Extrude56" { return }` block.

- [ ] **Step 4: Thread snapshots through extrude compilation**

Change the signature:

```go
func (c *Compiler) compileAndExecuteExtrude(e cadir.ExtrudeFeature, snap []cadir.BodySnapshot) error {
```

Update every `c.mergeParticipantBodies(e, desiredBodyName)` call inside `compileAndExecuteExtrude` (there are six: in the face-extrude success paths, the no-profile participant path, the multi-new-body join path, the main success path, and the sequential-fallback path) to:

```go
c.mergeParticipantBodies(e, snap, desiredBodyName)
```

Update the call site in `CompileModelWithOptions`:

```go
			if err := c.compileAndExecuteExtrude(*step.Extrude, step.BodySnapshot); err != nil {
```

- [ ] **Step 5: Delete the name-based target override, add the swept-volume fallback**

In `compileAndExecuteExtrude`, delete this entire block from the `if e.Operation == "join" || e.Operation == "cut" {` target-resolution section:

```go
		if e.Name == "Extrude56" || e.Name == "Extrude57" || e.Name == "Extrude59" || e.Name == "Extrude60" {
			for _, db := range designData.Bodies {
				if db.Name == "Corne36_Left_MCULid" {
					targetBodies = append(targetBodies, "Corne36_Left_MCULid")
					break
				}
			}
		}
		for _, rb := range sourceList {
			if rb == "Corne36_Left_MCULid" && len(targetBodies) > 0 && targetBodies[0] == "Corne36_Left_MCULid" {
				continue
			}
```

keeping only:

```go
		for _, rb := range sourceList {
```

and the rest of that loop unchanged.

Immediately after that target-resolution loop closes (before the `// If no profile indices matched, check if we have a FaceTarget` comment), add:

```go
	// Generic fallback: when IR-listed body names do not resolve to scene
	// bodies, aim at bodies whose bbox intersects the swept volume.
	if len(targetBodies) == 0 && (e.Operation == "join" || e.Operation == "cut") {
		targetBodies = c.sweptVolumeTargets(e)
		if len(targetBodies) > 0 {
			fmt.Printf("Extrude %s: name-based targets unresolved, using swept-volume fallback %v\n", e.Name, targetBodies)
		}
	}
```

Add the helper at package level:

```go
// sweptVolumeTargets returns scene bodies whose bbox intersects the bbox of
// the extrusion's swept volume, computed from the sketch plane basis, profile
// bounds, start offset, and extrude distance. Used only as a fallback when
// name-based target resolution finds nothing.
func (c *Compiler) sweptVolumeTargets(e cadir.ExtrudeFeature) []string {
	basis, ok := c.sketchPlaneByName[e.SketchName]
	if !ok {
		return nil
	}
	origin, xDir, yDir := basis[0], basis[1], basis[2]
	normal := [3]float64{
		xDir[1]*yDir[2] - xDir[2]*yDir[1],
		xDir[2]*yDir[0] - xDir[0]*yDir[2],
		xDir[0]*yDir[1] - xDir[1]*yDir[0],
	}
	minSw := [3]float64{1e9, 1e9, 1e9}
	maxSw := [3]float64{-1e9, -1e9, -1e9}
	expand := func(corner2d [2]float64, z float64) {
		w := [3]float64{
			origin[0] + xDir[0]*corner2d[0] + yDir[0]*corner2d[1],
			origin[1] + xDir[1]*corner2d[0] + yDir[1]*corner2d[1],
			origin[2] + xDir[2]*corner2d[0] + yDir[2]*corner2d[1],
		}
		for i := 0; i < 3; i++ {
			minSw[i] = math.Min(minSw[i], w[i])
			maxSw[i] = math.Max(maxSw[i], w[i])
		}
	}
	for _, fp := range e.Profiles {
		minX, maxX := fp.Centroid.X, fp.Centroid.X
		minY, maxY := fp.Centroid.Y, fp.Centroid.Y
		if fp.BBoxMin != nil && fp.BBoxMax != nil {
			minX, minY = fp.BBoxMin.X, fp.BBoxMin.Y
			maxX, maxY = fp.BBoxMax.X, fp.BBoxMax.Y
		} else {
			half := math.Sqrt(math.Max(fp.AreaMM2, 1e-9)) / 2
			minX, minY = fp.Centroid.X-half, fp.Centroid.Y-half
			maxX, maxY = fp.Centroid.X+half, fp.Centroid.Y+half
		}
		for _, corner := range [][2]float64{{minX, minY}, {maxX, minY}, {minX, maxY}, {maxX, maxY}} {
			expand(corner, 0)
		}
	}
	if len(e.Profiles) == 0 {
		for _, pc := range e.ProfileCentroids {
			half := 1.0
			for _, corner := range [][2]float64{{pc.X - half, pc.Y - half}, {pc.X + half, pc.Y + half}} {
				expand(corner, 0)
			}
		}
	}
	// Extrude along the normal from the start plane to start+distance (either sign).
	z0 := e.StartOffsetMM
	z1 := e.StartOffsetMM + e.DistanceMM
	for _, z := range []float64{z0, z1} {
		wz := origin[2] + normal[2]*z
		minSw[2] = math.Min(minSw[2], math.Min(origin[2], wz))
		maxSw[2] = math.Max(maxSw[2], math.Max(origin[2], wz))
	}
	// Expand laterally by the normal's in-plane contribution (tilted planes).
	for i := 0; i < 2; i++ {
		tol := 1e-6
		minSw[i] -= math.Abs(normal[i]*math.Min(z0, z1)) + tol
		maxSw[i] += math.Abs(normal[i]*math.Max(z0, z1)) + tol
	}

	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return nil
	}
	var design struct {
		Bodies []struct {
			Name string `json:"name"`
			BBox struct {
				Min []float64 `json:"min"`
				Max []float64 `json:"max"`
			} `json:"bbox"`
		} `json:"bodies"`
	}
	if json.Unmarshal([]byte(inspOut), &design) != nil {
		return nil
	}
	var hits []string
	for _, b := range design.Bodies {
		if len(b.BBox.Min) < 3 || len(b.BBox.Max) < 3 {
			continue
		}
		overlap := true
		for i := 0; i < 3; i++ {
			if b.BBox.Max[i] < minSw[i] || b.BBox.Min[i] > maxSw[i] {
				overlap = false
				break
			}
		}
		if overlap {
			hits = append(hits, b.Name)
		}
	}
	sort.Strings(hits)
	return hits
}
```

- [ ] **Step 6: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add pkg/solidcraft/compiler.go
git commit -m "feat(compiler): snapshot-driven participant merging and swept-volume target fallback"
```

---

### Task 8: Mirror cleanup + dead code removal

**Files:**
- Modify: `pkg/solidcraft/compiler.go` (`compileAndExecuteMirror`, `finalizeExtrudeBodies`, `compileMultiBodyExtrude`, `profCentroid`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `compileMultiBodyExtrude(e cadir.ExtrudeFeature, profs []int, sketchRef interface{}) bool` (signature slimmed).

- [ ] **Step 1: Simplify mirror target resolution**

In `compileAndExecuteMirror`, delete the entire block:

```go
	var targetBodies []string
	for _, b := range designData.Bodies {
		if b.Name == "Corne36_Left_MCULid" {
			targetBodies = []string{"Corne36_Left_MCULid"}
			break
		}
	}
	if len(targetBodies) == 0 && len(mf.TargetBodies) > 0 {
```

and replace with:

```go
	var targetBodies []string
	if len(mf.TargetBodies) > 0 {
```

Then delete the whole `"mcucover"` face-count heuristic block:

```go
	// If the resolved target body is a huge main keyboard body (> 200 faces) while a smaller lid body exists, prefer the smaller lid body
	if len(targetBodies) == 1 && strings.Contains(strings.ToLower(targetBodies[0]), "mcucover") {
		... (through its closing brace) ...
	}
```

- [ ] **Step 2: Delete `finalizeExtrudeBodies`**

Remove the entire `finalizeExtrudeBodies` function (including the `if false { ... }` dead block and the `Extrude60` rename chain) and the `defer c.finalizeExtrudeBodies(e)` line in `compileAndExecuteExtrude`.

- [ ] **Step 3: Remove the `profCentroid` stub**

Delete:

```go
type profCentroid struct {
	Index int
	X, Y  float64
}

func scProfilesCentroids[T any](ps []T) []profCentroid { return nil }
```

Change `compileMultiBodyExtrude`'s signature to drop the unused parameter:

```go
func (c *Compiler) compileMultiBodyExtrude(e cadir.ExtrudeFeature, profs []int, sketchRef interface{}) bool {
```

and update its call site:

```go
		if c.compileMultiBodyExtrude(e, targetIndices, sketchRef) {
```

- [ ] **Step 4: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors. If `strings` is now unused in a scope, remove the import accordingly (it is still used elsewhere, e.g. `Synthetic_` prefix checks).

- [ ] **Step 5: Commit**

```bash
git add pkg/solidcraft/compiler.go
git commit -m "refactor(compiler): remove name-keyed mirror heuristics and dead code"
```

---

### Task 9: `--skip-steps` CLI flag

**Files:**
- Modify: `cmd/fusion2solidcraft/main.go`
- Test: `cmd/fusion2solidcraft/main_test.go` (create)

**Interfaces:**
- Consumes: `solidcraft.CompileOptions.SkipSteps map[int]bool` (Task 6).
- Produces: CLI flag `-skip-steps` and `parseSkipSteps(spec string) (map[int]bool, error)`.

- [ ] **Step 1: Write failing tests in `cmd/fusion2solidcraft/main_test.go`**

```go
package main

import (
	"reflect"
	"testing"
)

func TestParseSkipSteps(t *testing.T) {
	cases := []struct {
		spec string
		want map[int]bool
		err  bool
	}{
		{spec: "", want: map[int]bool{}},
		{spec: "51", want: map[int]bool{51: true}},
		{spec: "51,52", want: map[int]bool{51: true, 52: true}},
		{spec: "40-45", want: map[int]bool{40: true, 41: true, 42: true, 43: true, 44: true, 45: true}},
		{spec: "10, 20-22 ,30", want: map[int]bool{10: true, 20: true, 21: true, 22: true, 30: true}},
		{spec: "5-3", err: true},
		{spec: "abc", err: true},
		{spec: "1,", want: map[int]bool{1: true}},
	}
	for _, tc := range cases {
		got, err := parseSkipSteps(tc.spec)
		if tc.err {
			if err == nil {
				t.Errorf("parseSkipSteps(%q): expected error", tc.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSkipSteps(%q): unexpected error %v", tc.spec, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseSkipSteps(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/fusion2solidcraft/ -run TestParseSkipSteps -v`
Expected: FAIL — `undefined: parseSkipSteps`.

- [ ] **Step 3: Implement in `cmd/fusion2solidcraft/main.go`**

Add `"strconv"` and `"strings"` to imports. Add the flag after `stepFrom`:

```go
	skipSteps := flag.String("skip-steps", "", "Comma-separated step indices or ranges (e.g. 51,52 or 40-45) to skip without executing (diagnostics)")
```

After `flag.Parse()`, add:

```go
	skipSet, err := parseSkipSteps(*skipSteps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid --skip-steps: %v\n", err)
		os.Exit(1)
	}
```

Pass it into `CompileOptions`:

```go
		SkipSteps:        skipSet,
```

Add the parser at the bottom of the file:

```go
// parseSkipSteps parses a comma-separated list of step indices and inclusive
// a-b ranges into a set.
func parseSkipSteps(spec string) (map[int]bool, error) {
	out := map[int]bool{}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return out, nil
	}
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if lo, hi, found := strings.Cut(tok, "-"); found {
			a, errLo := strconv.Atoi(strings.TrimSpace(lo))
			b, errHi := strconv.Atoi(strings.TrimSpace(hi))
			if errLo != nil || errHi != nil || b < a {
				return nil, fmt.Errorf("bad range %q", tok)
			}
			for i := a; i <= b; i++ {
				out[i] = true
			}
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad index %q", tok)
		}
		out[n] = true
	}
	return out, nil
}
```

Note: `err` is already declared in main's scope from earlier uses — if the compiler complains about redeclaration in the same scope, use `skipSet, skipErr := parseSkipSteps(...)` and check `skipErr`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/fusion2solidcraft/ -v && go build ./...`
Expected: PASS, no build errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/fusion2solidcraft/main.go cmd/fusion2solidcraft/main_test.go
git commit -m "feat(cli): add --skip-steps for diagnostic step skipping"
```

---

### Task 10: Design-specific literal gate (TDD)

**Files:**
- Test: `pkg/solidcraft/compiler_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `TestNoDesignSpecificLiterals` — a regression gate scanning all non-test Go files under `pkg/`.

- [ ] **Step 1: Write the test**

```go
package solidcraft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDesignSpecificLiterals fails if any non-test Go file under pkg/
// mentions a design-specific name. The translator must be design-agnostic.
func TestNoDesignSpecificLiterals(t *testing.T) {
	banned := []string{
		"Extrude56", "Extrude57", "Extrude59", "Extrude60",
		"Corne36", "Xiao_", "McuCover", "MCULid",
		"usbc", "Socket", "connectors", "bottom_covers", "Sketch1",
	}
	walkErr := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, b := range banned {
				if strings.Contains(line, b) {
					t.Errorf("%s:%d contains design-specific literal %q", path, i+1, b)
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk failed: %v", walkErr)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/solidcraft/ -run TestNoDesignSpecificLiterals -v`
Expected: PASS (all name-keyed workarounds were removed in Tasks 4-8). If it FAILS, a literal survived — locate and remove it, then re-run.

- [ ] **Step 3: Commit**

```bash
git add pkg/solidcraft/compiler_test.go
git commit -m "test: gate against design-specific literals in pkg/"
```

---

### Task 11: Full build + test verification

- [ ] **Step 1: Build, vet, and test everything**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS, no output anomalies.

- [ ] **Step 2: Commit any remaining fixups**

If Steps 1 required fixes, commit them:

```bash
git add -A pkg cmd
git commit -m "fix: address build and test findings from full verification"
```

---

### Task 12: Live regression (requires user's Fusion 360 + SolidCraft)

**Files:** none modified (verification only). Tuning edits may touch `pkg/solidcraft/compiler.go` and `pkg/cadir/snapshot.go` (match thresholds only).

- [ ] **Step 1: Re-extract the IR with body snapshots**

With Fusion 360 running and the TestDrive Fifi Xiao v29 model active:

```bash
go run ./cmd/fusion2solidcraft --json-out model_ir.json --inspect
```

Verify the new IR has snapshots:

```bash
python3 -c "
import json
m = json.load(open('model_ir.json'))
missing = [s['index'] for s in m['timeline'] if 'body_snapshot' not in s]
print('steps without body_snapshot:', missing[:10], '... total', len(missing))
"
```

Expected: only steps with zero valid bodies (if any) lack a snapshot.

- [ ] **Step 2: Replay TestDrive end-to-end**

```bash
go run ./cmd/fusion2solidcraft --input model_ir.json --find-divergence
```

Expected: all checkpoints MATCH; watch for `Reconcile: renamed body ...` lines — renames must be sparse and sane (the old `Extrude60` rename dance should appear as one or two automatic renames around steps 77-82).

- [ ] **Step 3: If divergence occurs, diagnose with the new tools**

- Use `--skip-steps N` to jump past a suspicious step, and `--until-step N --start-step M` windows around it.
- Inspect the IR snapshots around the divergent step (`body_snapshot` names/volumes) to see Fusion's ground truth.
- Tune only **universal** constants (match thresholds in `pkg/cadir/snapshot.go`, profile-matching tolerances in `compileAndExecuteExtrude`) — never reintroduce name checks. Re-run until all checkpoints pass.

- [ ] **Step 4: Replay the second design**

Load the user's second design in Fusion 360 and repeat Steps 1-3. Expected: replay completes with no design-specific code path involved (guaranteed by the Task 10 gate) and checkpoints pass or divergences are attributable to genuinely unsupported features (not name workarounds).

- [ ] **Step 5: Commit tuned constants (if any)**

```bash
git add pkg/cadir/snapshot.go pkg/solidcraft/compiler.go model_ir.json
git commit -m "feat: tune universal thresholds against live regression runs"
```
