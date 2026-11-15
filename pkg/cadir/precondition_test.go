package cadir

import (
	"math"
	"testing"
)

func TestNormalizeArc(t *testing.T) {
	// Center at (0, 0), arc from (1, 0) through (0, 1) to (-1, 0) -> this is CCW
	center := Point2D{X: 0, Y: 0}
	start := Point2D{X: 1, Y: 0}
	end := Point2D{X: -1, Y: 0}
	mid := Point2D{X: 0, Y: 1}

	sNorm, eNorm := NormalizeArc(center, start, end, mid)
	if sNorm != start || eNorm != end {
		t.Errorf("expected start %v and end %v, got %v and %v", start, end, sNorm, eNorm)
	}

	// Reverse points: start from (-1, 0) to (1, 0) passing through (0, 1)
	// That means traveling CCW from (1, 0) to (-1, 0). Normalization should swap them so start is (1,0) and end is (-1,0)
	sRev, eRev := NormalizeArc(center, end, start, mid)
	if sRev != start || eRev != end {
		t.Errorf("expected swap to start %v and end %v, got %v and %v", start, end, sRev, eRev)
	}
}

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

func TestSeparateIsolatedTangentCircles_NudgesInternalSlot(t *testing.T) {
	// A boundary line at y = 10 from x = -10 to x = 10.
	// A circle at (0, 9) with radius 1 is tangent to the line at (0, 10).
	// Two slot lines extend downward from the circle to (0.5, 5) and (-0.5, 5),
	// capped by another circle at (0, 5) with radius 0.5.
	// This is an isolated closed feature that pinches against y = 10.
	curves := []Curve{
		{ID: "boundary", Type: CurveTypeLine, Start: Point2D{X: -10, Y: 10}, End: Point2D{X: 10, Y: 10}},
		{ID: "circ_top", Type: CurveTypeCircle, Center: Point2D{X: 0, Y: 9}, Radius: 1.0},
		{ID: "slot_r", Type: CurveTypeLine, Start: Point2D{X: 0.866025, Y: 8.5}, End: Point2D{X: 0.866025, Y: 5}},
		{ID: "slot_l", Type: CurveTypeLine, Start: Point2D{X: -0.866025, Y: 8.5}, End: Point2D{X: -0.866025, Y: 5}},
		{ID: "circ_bot", Type: CurveTypeCircle, Center: Point2D{X: 0, Y: 5}, Radius: 0.866025},
	}
	out := SeparateIsolatedTangentCircles(curves)
	if len(out) != len(curves) {
		t.Fatalf("expected %d curves, got %d", len(curves), len(out))
	}
	// The top circle should be nudged downward along normal (0, -1) by gap = 0.001 mm.
	topCirc := out[1]
	expectedY := 9.0 - 0.001
	if math.Abs(topCirc.Center.Y-expectedY) > 1e-6 {
		t.Errorf("expected circ_top center Y to be nudged to %f, got %f", expectedY, topCirc.Center.Y)
	}
	if math.Abs(topCirc.Center.X-0.0) > 1e-6 {
		t.Errorf("expected circ_top center X to remain 0, got %f", topCirc.Center.X)
	}
}

func TestSeparateIsolatedTangentCircles_LeavesCornerFilletAlone(t *testing.T) {
	// A circle at (9, 9) with radius 1 tangent to x = 10 (at (10, 9)) AND y = 10 (at (9, 10)).
	// This is a corner fillet between perpendicular lines, not an internal pinch point.
	curves := []Curve{
		{ID: "l_vert", Type: CurveTypeLine, Start: Point2D{X: 10, Y: 0}, End: Point2D{X: 10, Y: 10}},
		{ID: "l_horiz", Type: CurveTypeLine, Start: Point2D{X: 0, Y: 10}, End: Point2D{X: 10, Y: 10}},
		{ID: "corner_circ", Type: CurveTypeCircle, Center: Point2D{X: 9, Y: 9}, Radius: 1.0},
	}
	out := SeparateIsolatedTangentCircles(curves)
	c := out[2]
	if c.Center.X != 9.0 || c.Center.Y != 9.0 {
		t.Errorf("expected corner fillet to remain untouched at (9, 9), got %v", c.Center)
	}
}

func TestSeparateIsolatedTangentCircles_LeavesDividingContourAlone(t *testing.T) {
	// A circle at (0, 9) with radius 1 tangent to y = 10 at (0, 10).
	// But it has a line extending to x = -20 connecting to an outer boundary.
	// Since it connects to external geometry, it divides faces and is not an isolated pinch.
	curves := []Curve{
		{ID: "boundary_top", Type: CurveTypeLine, Start: Point2D{X: -10, Y: 10}, End: Point2D{X: 10, Y: 10}},
		{ID: "circ", Type: CurveTypeCircle, Center: Point2D{X: 0, Y: 9}, Radius: 1.0},
		{ID: "chain1", Type: CurveTypeLine, Start: Point2D{X: -1, Y: 9}, End: Point2D{X: -10, Y: 9}},
		{ID: "chain2", Type: CurveTypeLine, Start: Point2D{X: -10, Y: 9}, End: Point2D{X: -20, Y: 0}},
		{ID: "boundary_ext", Type: CurveTypeLine, Start: Point2D{X: -20, Y: 0}, End: Point2D{X: -20, Y: 10}},
		{ID: "boundary_ext2", Type: CurveTypeLine, Start: Point2D{X: -20, Y: 10}, End: Point2D{X: -10, Y: 10}},
		{ID: "extra1", Type: CurveTypeLine, Start: Point2D{X: -25, Y: 0}, End: Point2D{X: -25, Y: 5}},
	}
	out := SeparateIsolatedTangentCircles(curves)
	c := out[1]
	if c.Center.X != 0.0 || c.Center.Y != 9.0 {
		t.Errorf("expected dividing contour circle to remain at (0, 9), got %v", c.Center)
	}
}

func TestBridgeOpenCollinearGaps(t *testing.T) {
	// Two vertical collinear segments along x = 50:
	// l1 runs from (50, 40) to (50, 17.3883)
	// l2 runs from (50, 17.5750) to (50, 15.9750)
	// Both (50, 17.3883) and (50, 17.5750) are open (degree-1) endpoints separated by 0.1867 mm.
	curves := []Curve{
		{ID: "top", Type: CurveTypeLine, Start: Point2D{X: 50, Y: 40}, End: Point2D{X: 60, Y: 40}},
		{ID: "l1", Type: CurveTypeLine, Start: Point2D{X: 50, Y: 40}, End: Point2D{X: 50, Y: 17.3883}},
		{ID: "l2", Type: CurveTypeLine, Start: Point2D{X: 50, Y: 17.5750}, End: Point2D{X: 50, Y: 15.9750}},
		{ID: "bot", Type: CurveTypeLine, Start: Point2D{X: 50, Y: 15.9750}, End: Point2D{X: 60, Y: 15.9750}},
	}
	out := BridgeOpenCollinearGaps(curves)
	if Distance2D(out[1].End, out[2].Start) > 1e-6 {
		t.Errorf("expected open collinear gap to be bridged, got l1.End=%v and l2.Start=%v", out[1].End, out[2].Start)
	}
}
