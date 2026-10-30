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

func TestCircleArcIntersections(t *testing.T) {
	// Circle of radius 5 at (0,0)
	// Arc of radius 5 centered at (3, 0)
	// Intersection should occur at x = 1.5, y = +/- sqrt(25 - 2.25) = +/- 4.7697
	inters := CircleArcIntersections(5.0, Point2D{X: 3.0, Y: 0.0}, 5.0)
	if len(inters) != 2 {
		t.Fatalf("expected 2 intersections, got %d", len(inters))
	}

	for _, pt := range inters {
		dist := math.Hypot(pt.X, pt.Y)
		if math.Abs(dist-5.0) > 1e-4 {
			t.Errorf("intersection point %v not on circle (dist=%f)", pt, dist)
		}
	}
}

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
