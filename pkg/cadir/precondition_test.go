package cadir

import (
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
