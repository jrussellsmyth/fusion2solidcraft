package cadir

import (
	"math"
	"sort"
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
