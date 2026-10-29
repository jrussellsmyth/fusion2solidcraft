package cadir

import (
	"math"
)

// NormalizeArc ensures the arc points [start, end] follow SolidCraft's counter-clockwise (CCW)
// convention relative to the midpoint. If traveling CCW from start to end does not pass through
// mid, start and end are swapped.
func NormalizeArc(center, start, end, mid Point2D) (Point2D, Point2D) {
	angleStart := math.Atan2(start.Y-center.Y, start.X-center.X)
	angleEnd := math.Atan2(end.Y-center.Y, end.X-center.X)
	angleMid := math.Atan2(mid.Y-center.Y, mid.X-center.X)

	if angleStart < 0 {
		angleStart += 2 * math.Pi
	}
	if angleEnd < 0 {
		angleEnd += 2 * math.Pi
	}
	if angleMid < 0 {
		angleMid += 2 * math.Pi
	}

	// Check if angleMid lies in the CCW interval [angleStart, angleEnd]
	isCCW := false
	if angleStart <= angleEnd {
		isCCW = angleMid >= angleStart && angleMid <= angleEnd
	} else {
		// Interval wraps around 2*pi
		isCCW = angleMid >= angleStart || angleMid <= angleEnd
	}

	if !isCCW {
		// Swap start and end
		return end, start
	}
	return start, end
}

// Distance2D computes the Euclidean distance between two 2D points.
func Distance2D(p1, p2 Point2D) float64 {
	dx := p1.X - p2.X
	dy := p1.Y - p2.Y
	return math.Hypot(dx, dy)
}

// Distance3D computes the Euclidean distance between two 3D points.
func Distance3D(p1, p2 Point3D) float64 {
	dx := p1.X - p2.X
	dy := p1.Y - p2.Y
	dz := p1.Z - p2.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// CircleArcIntersections finds intersection points between a circle centered at origin and an arc.
func CircleArcIntersections(circleRadius float64, arcCenter Point2D, arcRadius float64) []Point2D {
	cx, cy := arcCenter.X, arcCenter.Y
	R := circleRadius
	r := arcRadius
	d2 := cx*cx + cy*cy
	d := math.Sqrt(d2)

	if d > R+r || d < math.Abs(R-r) || d == 0 {
		return nil
	}

	// Chord line: 2*cx*x + 2*cy*y = R^2 + d^2 - r^2
	A := 2 * cx
	B := 2 * cy
	C := R*R + d2 - r*r

	// Solve quadratic by substituting x = (C - B*y)/A or vice-versa
	var pts []Point2D
	if math.Abs(A) > math.Abs(B) {
		aQuad := A*A + B*B
		bQuad := -2 * B * C
		cQuad := C*C - A*A*R*R
		disc := bQuad*bQuad - 4*aQuad*cQuad
		if disc >= 0 {
			sqrtDisc := math.Sqrt(disc)
			y1 := (-bQuad + sqrtDisc) / (2 * aQuad)
			y2 := (-bQuad - sqrtDisc) / (2 * aQuad)
			x1 := (C - B*y1) / A
			x2 := (C - B*y2) / A
			pts = append(pts, Point2D{X: x1, Y: y1}, Point2D{X: x2, Y: y2})
		}
	} else {
		aQuad := A*A + B*B
		bQuad := -2 * A * C
		cQuad := C*C - B*B*R*R
		disc := bQuad*bQuad - 4*aQuad*cQuad
		if disc >= 0 {
			sqrtDisc := math.Sqrt(disc)
			x1 := (-bQuad + sqrtDisc) / (2 * aQuad)
			x2 := (-bQuad - sqrtDisc) / (2 * aQuad)
			y1 := (C - A*x1) / B
			y2 := (C - A*x2) / B
			pts = append(pts, Point2D{X: x1, Y: y1}, Point2D{X: x2, Y: y2})
		}
	}
	return pts
}
