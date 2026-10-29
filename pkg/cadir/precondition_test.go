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
