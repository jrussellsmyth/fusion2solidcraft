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

// SeparateIsolatedTangentCircles detects circles that are part of an isolated closed
// feature (like a slot or standalone hole) touching a boundary line tangentially,
// which would otherwise create a non-manifold self-touching pinch point in B-rep
// kernels like SMLib. It nudges the circle away from the line by a micron (0.001 mm)
// to ensure a valid manifold polygon boundary while preserving volume and geometry.
func SeparateIsolatedTangentCircles(curves []Curve) []Curve {
	const (
		tangencyTol = 0.005 // 5 microns
		gap         = 0.001 // 1 micron clearance
		connEps     = 0.05  // connection tolerance
	)

	out := make([]Curve, len(curves))
	copy(out, curves)

	type indexedCurve struct {
		origIdx int
		c       Curve
	}
	var nonConst []indexedCurve
	for i, c := range out {
		if !c.Construction {
			nonConst = append(nonConst, indexedCurve{origIdx: i, c: c})
		}
	}
	if len(nonConst) == 0 {
		return out
	}

	getEndpoints := func(c Curve) []Point2D {
		if c.Type == CurveTypeCircle {
			return nil
		}
		return []Point2D{c.Start, c.End}
	}

	curvesConnect := func(ca, cb Curve, exemptPt Point2D) bool {
		ptsA := getEndpoints(ca)
		ptsB := getEndpoints(cb)
		if ca.Type == CurveTypeCircle {
			for _, pb := range ptsB {
				if math.Abs(Distance2D(pb, ca.Center)-ca.Radius) < connEps && Distance2D(pb, exemptPt) > connEps {
					return true
				}
			}
		}
		if cb.Type == CurveTypeCircle {
			for _, pa := range ptsA {
				if math.Abs(Distance2D(pa, cb.Center)-cb.Radius) < connEps && Distance2D(pa, exemptPt) > connEps {
					return true
				}
			}
		}
		for _, pa := range ptsA {
			for _, pb := range ptsB {
				if Distance2D(pa, pb) < connEps && Distance2D(pa, exemptPt) > connEps {
					return true
				}
			}
		}
		return false
	}

	for ncIdx, nc := range nonConst {
		c := nc.c
		if c.Type != CurveTypeCircle || c.Radius <= 0 {
			continue
		}

		type tangentHit struct {
			lineIdx   int
			contactPt Point2D
			dist      float64
		}
		var hits []tangentHit

		for lIdx, lItem := range nonConst {
			l := lItem.c
			if l.Type != CurveTypeLine {
				continue
			}
			vx := l.End.X - l.Start.X
			vy := l.End.Y - l.Start.Y
			lenSq := vx*vx + vy*vy
			if lenSq < 1e-12 {
				continue
			}
			t := ((c.Center.X-l.Start.X)*vx + (c.Center.Y-l.Start.Y)*vy) / lenSq
			if t < 0.01 || t > 0.99 {
				continue
			}
			qx := l.Start.X + t*vx
			qy := l.Start.Y + t*vy
			q := Point2D{X: qx, Y: qy}
			d := Distance2D(c.Center, q)
			if math.Abs(d-c.Radius) < tangencyTol {
				hits = append(hits, tangentHit{lineIdx: lIdx, contactPt: q, dist: d})
			}
		}

		if len(hits) == 0 {
			continue
		}

		// If tangent to multiple non-parallel lines (corner fillet), skip
		isCorner := false
		for j := 0; j < len(hits); j++ {
			for k := j + 1; k < len(hits); k++ {
				l1 := nonConst[hits[j].lineIdx].c
				l2 := nonConst[hits[k].lineIdx].c
				v1x, v1y := l1.End.X-l1.Start.X, l1.End.Y-l1.Start.Y
				v2x, v2y := l2.End.X-l2.Start.X, l2.End.Y-l2.Start.Y
				cross := v1x*v2y - v1y*v2x
				l1Len := math.Hypot(v1x, v1y)
				l2Len := math.Hypot(v2x, v2y)
				if l1Len > 1e-6 && l2Len > 1e-6 && math.Abs(cross)/(l1Len*l2Len) > 0.1 {
					isCorner = true
					break
				}
			}
			if isCorner {
				break
			}
		}
		if isCorner {
			continue
		}

		for _, hit := range hits {
			lIdx := hit.lineIdx
			q := hit.contactPt
			d := hit.dist

			visited := make([]bool, len(nonConst))
			visited[ncIdx] = true
			queue := []int{ncIdx}

			for len(queue) > 0 {
				currIdx := queue[0]
				queue = queue[1:]
				curr := nonConst[currIdx].c
				for otherIdx, otherItem := range nonConst {
					if visited[otherIdx] || otherIdx == lIdx {
						continue
					}
					if curvesConnect(curr, otherItem.c, q) {
						visited[otherIdx] = true
						queue = append(queue, otherIdx)
					}
				}
			}

			touchesOutside := false
			visitedCount := 0
			for vIdx, isV := range visited {
				if !isV {
					continue
				}
				visitedCount++
				vc := nonConst[vIdx].c

				// Check if vc connects to line l away from q
				pts := getEndpoints(vc)
				for _, p := range pts {
					if Distance2D(p, q) <= connEps {
						continue
					}
					lCurve := nonConst[lIdx].c
					vx := lCurve.End.X - lCurve.Start.X
					vy := lCurve.End.Y - lCurve.Start.Y
					lLenSq := vx*vx + vy*vy
					if lLenSq > 1e-12 {
						tP := ((p.X-lCurve.Start.X)*vx + (p.Y-lCurve.Start.Y)*vy) / lLenSq
						if tP >= -0.01 && tP <= 1.01 {
							proj := Point2D{X: lCurve.Start.X + tP*vx, Y: lCurve.Start.Y + tP*vy}
							if Distance2D(p, proj) < connEps {
								touchesOutside = true
								break
							}
						}
					}
				}
				if touchesOutside {
					break
				}

				for otherIdx, otherItem := range nonConst {
					if visited[otherIdx] || otherIdx == lIdx {
						continue
					}
					if curvesConnect(vc, otherItem.c, q) {
						touchesOutside = true
						break
					}
				}
				if touchesOutside {
					break
				}
			}

			if !touchesOutside && visitedCount <= 6 && d > 1e-12 {
				ux := (c.Center.X - q.X) / d
				uy := (c.Center.Y - q.Y) / d
				shift := (c.Radius + gap) - d
				out[nc.origIdx].Center.X += shift * ux
				out[nc.origIdx].Center.Y += shift * uy
				break
			}
		}
	}
	return out
}

// BridgeOpenCollinearGaps detects pairs of non-construction line segments that
// are collinear and have open (degree-1) endpoints separated by a sub-quarter-mm
// gap (0.005 mm < gap < 0.25 mm), such as when a projected BRep edge in Fusion
// falls slightly short of an adjacent collinear segment that Fusion's profile
// finder still treats as continuous. It snaps the first open endpoint to the
// second so SolidCraft's exact sketch profile detector closes the loop.
func BridgeOpenCollinearGaps(curves []Curve) []Curve {
	out := make([]Curve, len(curves))
	copy(out, curves)

	var allPts []Point2D
	for _, c := range out {
		if c.Construction {
			continue
		}
		switch c.Type {
		case CurveTypeLine, CurveTypeArc:
			allPts = append(allPts, c.Start, c.End)
		case CurveTypeSpline:
			if len(c.ControlPoints) >= 2 {
				allPts = append(allPts, c.ControlPoints[0], c.ControlPoints[len(c.ControlPoints)-1])
			} else if len(c.Points) >= 2 {
				allPts = append(allPts, c.Points[0], c.Points[len(c.Points)-1])
			}
		}
	}

	isOpen := func(pt Point2D) bool {
		cnt := 0
		for _, q := range allPts {
			if Distance2D(pt, q) < 0.01 {
				cnt++
			}
		}
		return cnt == 1
	}

	for i := 0; i < len(out); i++ {
		if out[i].Construction || out[i].Type != CurveTypeLine {
			continue
		}
		p1, p2 := out[i].Start, out[i].End
		dx1, dy1 := p2.X-p1.X, p2.Y-p1.Y
		l1 := math.Hypot(dx1, dy1)
		if l1 < 1e-4 {
			continue
		}
		for j := i + 1; j < len(out); j++ {
			if out[j].Construction || out[j].Type != CurveTypeLine {
				continue
			}
			q1, q2 := out[j].Start, out[j].End
			dx2, dy2 := q2.X-q1.X, q2.Y-q1.Y
			l2 := math.Hypot(dx2, dy2)
			if l2 < 1e-4 {
				continue
			}
			if math.Abs(dx1*dy2-dy1*dx2)/(l1*l2) > 1e-3 {
				continue
			}
			if math.Abs((q1.X-p1.X)*dy1-(q1.Y-p1.Y)*dx1)/l1 > 1e-3 {
				continue
			}
			ptsI := [2]*Point2D{&out[i].Start, &out[i].End}
			ptsJ := [2]Point2D{out[j].Start, out[j].End}
			bridged := false
			for _, pi := range ptsI {
				if !isOpen(*pi) {
					continue
				}
				for _, pj := range ptsJ {
					if !isOpen(pj) {
						continue
					}
					d := Distance2D(*pi, pj)
					if d > 0.005 && d < 0.25 {
						*pi = pj
						bridged = true
						break
					}
				}
				if bridged {
					break
				}
			}
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
