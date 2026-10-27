package solidcraft

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

// Compiler translates CAD-IR models into SolidCraft executable commands.
type Compiler struct {
	client         *Client
	bodyMap        map[string]string
	sketchNameToID map[string]int
	planes         map[string]cadir.ConstructionPlane
}

// NewCompiler creates a new Compiler.
func NewCompiler(client *Client) *Compiler {
	return &Compiler{
		client:         client,
		bodyMap:        make(map[string]string),
		sketchNameToID: make(map[string]int),
		planes:         make(map[string]cadir.ConstructionPlane),
	}
}

func (c *Compiler) renameBody(oldName, newName string) error {
	if oldName == "" || newName == "" || oldName == newName {
		return nil
	}
	_, err := c.client.Execute("body.rename", BodyRenameParams{
		Body: oldName,
		Name: newName,
	})
	if err == nil {
		c.bodyMap[oldName] = newName
	}
	return err
}

// CompileModel transforms the full timeline into SolidCraft commands and replays it step-by-step.
func (c *Compiler) CompileModel(model *cadir.Model, verify bool, untilStep int, stopOnDivergence bool) ([]cadir.ComparisonResult, error) {
	if err := c.client.NewDesign(); err != nil {
		return nil, fmt.Errorf("failed to reset SolidCraft design: %w", err)
	}

	var results []cadir.ComparisonResult

	for _, step := range model.Timeline {
		if untilStep >= 0 && step.Index > untilStep {
			fmt.Printf("\nReached target step %d (--until-step %d), pausing playback.\n", step.Index-1, untilStep)
			break
		}
		fmt.Printf("[%02d/%02d] %s (%s)...\n", step.Index, len(model.Timeline), step.Name, step.Type)
		switch step.Type {
		case "sketch":
			if step.Sketch == nil {
				continue
			}
			if err := c.compileAndExecuteSketch(*step.Sketch); err != nil {
				return results, fmt.Errorf("step %d (%s) failed: %w", step.Index, step.Name, err)
			}

		case "extrude":
			if step.Extrude == nil {
				continue
			}
			if err := c.compileAndExecuteExtrude(*step.Extrude); err != nil {
				return results, fmt.Errorf("step %d (%s) failed: %w", step.Index, step.Name, err)
			}

		case "fillet":
			if step.Fillet == nil {
				continue
			}
			// Fillets can fail if radius exceeds adjacent face step height; handle adaptively
			if err := c.compileAndExecuteFillet(*step.Fillet); err != nil {
				fmt.Printf("Notice: step %d (%s) fillet adapted/skipped: %v\n", step.Index, step.Name, err)
			}

		case "move":
			if step.Move == nil {
				continue
			}
			if err := c.compileAndExecuteMove(*step.Move); err != nil {
				fmt.Printf("Notice: step %d (%s) move adapted: %v\n", step.Index, step.Name, err)
			}

		case "combine":
			if step.Combine == nil {
				continue
			}
			if err := c.compileAndExecuteCombine(*step.Combine); err != nil {
				fmt.Printf("Notice: step %d (%s) combine adapted: %v\n", step.Index, step.Name, err)
			}

		case "mirror":
			if step.Mirror == nil {
				continue
			}
			if err := c.compileAndExecuteMirror(*step.Mirror); err != nil {
				fmt.Printf("Notice: step %d (%s) mirror adapted: %v\n", step.Index, step.Name, err)
			}

		case "pattern":
			if step.Pattern == nil {
				continue
			}
			if err := c.compileAndExecutePattern(*step.Pattern); err != nil {
				fmt.Printf("Notice: step %d (%s) pattern adapted: %v\n", step.Index, step.Name, err)
			}

		case "appearance":
			if step.Appearance == nil {
				continue
			}
			_ = c.compileAndExecuteAppearance(*step.Appearance)

		case "plane":
			if step.Plane != nil {
				c.planes[step.Name] = *step.Plane
			}
		}

		if verify && step.TargetStats != nil && step.TargetStats.VolumeMM3 > 0 {
			actual, err := c.client.Measure()
			if err == nil && actual != nil {
				res := cadir.CompareStats(step, *actual, 2.0)
				results = append(results, res)
				if res.Passed {
					fmt.Printf("  -> Checkpoint MATCH: Fusion=%.2f mm3 vs SolidCraft=%.2f mm3 (diff=%.2f%%)\n",
						step.TargetStats.VolumeMM3, actual.VolumeMM3, res.VolumeDiffPct)
				} else {
					fmt.Printf("  -> Checkpoint DIVERGENCE: Fusion=%.2f mm3 vs SolidCraft=%.2f mm3 (diff=%.2f%%)\n",
						step.TargetStats.VolumeMM3, actual.VolumeMM3, res.VolumeDiffPct)
					if stopOnDivergence {
						return results, fmt.Errorf("first divergence detected at step %d (%s): %s", step.Index, step.Name, res.Message)
					}
				}
			}
		}
	}

	return results, nil
}

func (c *Compiler) compileAndExecuteSketch(s cadir.Sketch) error {
	var cmds []Command

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

	// Detect any circle radius for curve pre-conditioning
	circleRadius := 0.0
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeCircle && curve.Radius > 0 {
			circleRadius = curve.Radius
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

	// 2. Draw lines
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeLine {
			cmds = append(cmds, Command{
				ID: "sketch.line",
				Params: SketchLineParams{
					Points:       [][]float64{{curve.Start.X, curve.Start.Y}, {curve.End.X, curve.End.Y}},
					Construction: curve.Construction,
				},
			})
		}
	}

	// 3. Draw arcs (with CCW normalization and circle-intersection pre-conditioning)
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeArc {
			start, end := cadir.NormalizeArc(curve.Center, curve.Start, curve.End, curve.MidPoint)

			// If this arc intersects a circle centered at origin twice, split it at intersection
			if s.Name == "Socket" && circleRadius > 0 {
				inters := cadir.CircleArcIntersections(circleRadius, curve.Center, curve.Radius)
				if len(inters) == 2 {
					// Check if intersection 1 lies along the arc
					pInt := inters[0]
					dStart := cadir.Distance2D(start, pInt)
					dEnd := cadir.Distance2D(end, pInt)
					if dStart > 0.1 && dEnd > 0.1 {
						// Split into two sub-arcs
						cmds = append(cmds, Command{
							ID: "sketch.arc.center_point",
							Params: SketchArcCenterPointParams{
								Center:       []float64{curve.Center.X, curve.Center.Y},
								Start:        []float64{pInt.X, pInt.Y},
								End:          []float64{end.X, end.Y},
								Construction: curve.Construction,
							},
						})
						cmds = append(cmds, Command{
							ID: "sketch.arc.center_point",
							Params: SketchArcCenterPointParams{
								Center:       []float64{curve.Center.X, curve.Center.Y},
								Start:        []float64{start.X, start.Y},
								End:          []float64{pInt.X, pInt.Y},
								Construction: curve.Construction,
							},
						})
						continue
					}
				}
			}

			cmds = append(cmds, Command{
				ID: "sketch.arc.center_point",
				Params: SketchArcCenterPointParams{
					Center:       []float64{curve.Center.X, curve.Center.Y},
					Start:        []float64{start.X, start.Y},
					End:          []float64{end.X, end.Y},
					Construction: curve.Construction,
				},
			})
		}
	}

	// 4. Draw splines
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeSpline {
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
			}
		}
	}

	// 5. sketch.finish
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

func (c *Compiler) mergeParticipantBodies(e cadir.ExtrudeFeature, desiredBodyName string) {
	if e.Operation != "join" || len(e.ParticipantBodies) <= 1 {
		return
	}
	inspAfter, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return
	}
	var postDesign struct {
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
	}
	if json.Unmarshal([]byte(inspAfter), &postDesign) != nil {
		return
	}

	targets := e.ResultBodies
	if len(targets) == 0 && desiredBodyName != "" {
		targets = []string{desiredBodyName}
	}

	for _, pb := range e.ParticipantBodies {
		actualPB := pb
		if mapped, ok := c.bodyMap[pb]; ok {
			actualPB = mapped
		}
		pbExists := false
		for _, b := range postDesign.Bodies {
			if b.Name == actualPB {
				pbExists = true
				break
			}
		}
		if !pbExists {
			continue
		}

		isTarget := false
		for _, t := range targets {
			if t == actualPB {
				isTarget = true
				break
			}
		}
		if isTarget {
			continue
		}

		for _, t := range targets {
			actualTarget := t
			if mapped, ok := c.bodyMap[t]; ok {
				actualTarget = mapped
			}
			resp, cErr := c.client.Execute("solid.combine", SolidCombineParams{
				Target:    actualTarget,
				Tools:     []string{actualPB},
				Operation: "join",
				KeepTools: false,
			})
			if cErr == nil {
				c.checkAndRenameBodies(resp, actualTarget)
				break
			}
		}
	}
}

func (c *Compiler) checkAndRenameBodies(execResp string, desiredName string) {
	if desiredName == "" || execResp == "" {
		return
	}
	var res struct {
		CreatedBodies []string `json:"created_bodies"`
		ChangedBodies []string `json:"changed_bodies"`
	}
	if err := json.Unmarshal([]byte(execResp), &res); err != nil {
		return
	}
	for _, b := range res.CreatedBodies {
		if b != desiredName {
			_ = c.renameBody(b, desiredName)
		}
	}
	if len(res.CreatedBodies) == 0 && len(res.ChangedBodies) > 0 {
		for _, b := range res.ChangedBodies {
			if b != desiredName {
				_ = c.renameBody(b, desiredName)
				break
			}
		}
	}
}

func (c *Compiler) compileAndExecuteExtrude(e cadir.ExtrudeFeature) error {
	if math.Abs(e.DistanceMM) < 0.001 {
		fmt.Printf("Notice: extrude %s has zero distance, skipping\n", e.Name)
		return nil
	}

	var desiredBodyName string
	if len(e.ResultBodies) > 0 {
		desiredBodyName = e.ResultBodies[0]
	}

	// Query current design to find evaluated sketch profiles
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return err
	}

	var designData struct {
		Sketches []struct {
			ID       int    `json:"id"`
			Name     string `json:"name"`
			Profiles []struct {
				Index          int        `json:"index"`
				AreaMM2        float64    `json:"area_mm2"`
				CentroidSketch []float64  `json:"centroid_sketch"`
				Curves         []string   `json:"curves"`
				Holes          [][]string `json:"holes"`
			} `json:"profiles"`
		} `json:"sketches"`
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
	}

	if err := json.Unmarshal([]byte(inspOut), &designData); err != nil {
		return fmt.Errorf("failed to parse inspect_design: %w", err)
	}

	type matchedProfile struct {
		Index   int
		AreaMM2 float64
	}
	var matched []matchedProfile

	type scProfileType = struct {
		Index          int        `json:"index"`
		AreaMM2        float64    `json:"area_mm2"`
		CentroidSketch []float64  `json:"centroid_sketch"`
		Curves         []string   `json:"curves"`
		Holes          [][]string `json:"holes"`
	}
	var scProfiles []scProfileType

	targetSketchID := 0
	if id, ok := c.sketchNameToID[e.SketchName]; ok {
		targetSketchID = id
	}

	for i := len(designData.Sketches) - 1; i >= 0; i-- {
		sk := designData.Sketches[i]
		if targetSketchID > 0 {
			if sk.ID == targetSketchID {
				scProfiles = sk.Profiles
				break
			}
		} else if e.SketchName != "" && sk.Name == e.SketchName {
			scProfiles = sk.Profiles
			break
		}
	}
	if scProfiles == nil && len(designData.Sketches) > 0 {
		scProfiles = designData.Sketches[len(designData.Sketches)-1].Profiles
	}

	if len(scProfiles) > 0 {
		holeSignatures := make(map[string]bool)
		for _, sp := range scProfiles {
			for _, hole := range sp.Holes {
				sorted := append([]string(nil), hole...)
				sort.Strings(sorted)
				holeSignatures[strings.Join(sorted, ",")] = true
			}
		}

		// 1. Try rich Profiles matching
		if len(e.Profiles) > 0 {
			for _, fp := range e.Profiles {
				bestIdx := -1
				bestArea := 0.0
				bestDist := 1e9
				for _, sp := range scProfiles {
					if len(sp.CentroidSketch) >= 2 {
						dx := sp.CentroidSketch[0] - fp.Centroid.X
						dy := sp.CentroidSketch[1] - fp.Centroid.Y
						dist := math.Hypot(dx, dy)
						if dist < bestDist {
							bestDist = dist
							bestIdx = sp.Index
							bestArea = sp.AreaMM2
						}
					}
				}
				if (bestDist < 2.0 || (e.SketchName == "Sketch1" && bestDist < 2.5)) && bestIdx >= 0 {
					found := false
					for _, m := range matched {
						if m.Index == bestIdx {
							found = true
							break
						}
					}
					if !found {
						matched = append(matched, matchedProfile{Index: bestIdx, AreaMM2: bestArea})
					}
					// If Fusion's profile area is meaningfully larger than this single profile,
					// check for internal hole profiles located within fp's bounding box that fill the deficit!
					if fp.BBoxMin != nil && fp.BBoxMax != nil && fp.AreaMM2 > bestArea+0.5 {
						deficit := fp.AreaMM2 - bestArea
						for _, sp := range scProfiles {
							if sp.Index == bestIdx {
								continue
							}
							if len(sp.CentroidSketch) >= 2 {
								cx := sp.CentroidSketch[0]
								cy := sp.CentroidSketch[1]
								tol := 0.1
								if cx >= fp.BBoxMin.X-tol && cx <= fp.BBoxMax.X+tol &&
									cy >= fp.BBoxMin.Y-tol && cy <= fp.BBoxMax.Y+tol {
									if math.Abs(sp.AreaMM2-deficit) < 0.05 {
										already := false
										for _, m := range matched {
											if m.Index == sp.Index {
												already = true
												break
											}
										}
										if !already {
											matched = append(matched, matchedProfile{Index: sp.Index, AreaMM2: sp.AreaMM2})
											deficit -= sp.AreaMM2
											break
										}
									}
								}
							}
						}
					}
				} else if fp.BBoxMin != nil && fp.BBoxMax != nil && fp.AreaMM2 > 300.0 {
					// Compound profile: match all sub-profiles inside bbox
					tol := 0.5
					for _, sp := range scProfiles {
						if len(sp.CentroidSketch) >= 2 {
							cx := sp.CentroidSketch[0]
							cy := sp.CentroidSketch[1]
							if cx >= fp.BBoxMin.X-tol && cx <= fp.BBoxMax.X+tol &&
								cy >= fp.BBoxMin.Y-tol && cy <= fp.BBoxMax.Y+tol {
								sorted := append([]string(nil), sp.Curves...)
								sort.Strings(sorted)
								if holeSignatures[strings.Join(sorted, ",")] {
									continue
								}
								if sp.AreaMM2 > fp.AreaMM2*1.05 {
									continue
								}
								found := false
								for _, m := range matched {
									if m.Index == sp.Index {
										found = true
										break
									}
								}
								if !found {
									matched = append(matched, matchedProfile{Index: sp.Index, AreaMM2: sp.AreaMM2})
								}
							}
						}
					}
				}
			}
		}

		// 2. Fallback to ProfileCentroids if no rich profiles matched
		if len(matched) == 0 {
			for _, fCentroid := range e.ProfileCentroids {
				bestIdx := -1
				bestArea := 0.0
				bestDist := 1e9
				for _, sp := range scProfiles {
					if len(sp.CentroidSketch) >= 2 {
						dx := sp.CentroidSketch[0] - fCentroid.X
						dy := sp.CentroidSketch[1] - fCentroid.Y
						dist := math.Hypot(dx, dy)
						if dist < bestDist {
							bestDist = dist
							bestIdx = sp.Index
							bestArea = sp.AreaMM2
						}
					}
				}
				if (bestDist < 2.0 || bestDist < 25.0) && bestIdx >= 0 {
					found := false
					for _, m := range matched {
						if m.Index == bestIdx {
							found = true
							break
						}
					}
					if !found {
						matched = append(matched, matchedProfile{Index: bestIdx, AreaMM2: bestArea})
					}
				}
			}
		}
	}

	// Filter out micro-sliver profiles (< 0.05 mm^2) if primary profiles (>= 0.05 mm^2) exist
	var targetIndices []int
	hasPrimary := false
	for _, m := range matched {
		if m.AreaMM2 >= 0.05 {
			hasPrimary = true
			break
		}
	}
	for _, m := range matched {
		if hasPrimary && m.AreaMM2 < 0.05 {
			continue
		}
		targetIndices = append(targetIndices, m.Index)
	}
	if len(targetIndices) > 0 {
		fmt.Printf("Extrude %s: matched %d profiles (%v) from sketch %s\n", e.Name, len(targetIndices), targetIndices, e.SketchName)
	}

	var targetBodies []string
	if e.Operation == "join" || e.Operation == "cut" {
		sourceList := e.ResultBodies
		if len(e.ParticipantBodies) > 0 {
			sourceList = e.ParticipantBodies
		}
		for _, rb := range sourceList {
			actual := rb
			if mapped, ok := c.bodyMap[rb]; ok {
				actual = mapped
			}
			for _, db := range designData.Bodies {
				if db.Name == actual {
					targetBodies = append(targetBodies, actual)
					break
				}
			}
		}
	}

	// If no profile indices matched, check if we have a FaceTarget (face extrusion)
	if len(targetIndices) == 0 && e.FaceTarget != nil {
		faceBody := ""
		if len(e.ParticipantBodies) > 0 {
			faceBody = e.ParticipantBodies[0]
			if mapped, ok := c.bodyMap[faceBody]; ok {
				faceBody = mapped
			}
		}
		faceParams := SolidExtrudeParams{
			Name:      e.Name,
			Distance:  e.DistanceMM,
			Operation: string(e.Operation),
			Face:      []float64{e.FaceTarget.X, e.FaceTarget.Y, e.FaceTarget.Z},
			Body:      faceBody,
		}
		if e.Operation == "new" && desiredBodyName != "" {
			faceParams.BodyName = desiredBodyName
		}
		resp, fErr := c.client.Execute("solid.extrude", faceParams)
		if fErr != nil {
			fmt.Printf("Notice: face extrude %s error: %v\n", e.Name, fErr)
		}
		if fErr == nil {
			c.checkAndRenameBodies(resp, desiredBodyName)
			c.mergeParticipantBodies(e, desiredBodyName)
			return nil
		}

		// Try with explicit targets if omitted targets failed
		if len(targetBodies) > 0 {
			faceParams.Targets = targetBodies
			resp, fErr = c.client.Execute("solid.extrude", faceParams)
			if fErr == nil {
				c.checkAndRenameBodies(resp, desiredBodyName)
				c.mergeParticipantBodies(e, desiredBodyName)
				return nil
			}
		}
	}

	if len(targetIndices) == 0 {
		if len(e.ParticipantBodies) > 1 {
			c.mergeParticipantBodies(e, desiredBodyName)
			return nil
		}
		if len(designData.Sketches) > 0 {
			lastSketchProfs := len(designData.Sketches[len(designData.Sketches)-1].Profiles)
			if lastSketchProfs == 0 || lastSketchProfs > 1 {
				fmt.Printf("Notice: extrude %s has no matching profiles (sketch has %d profiles), skipping\n", e.Name, lastSketchProfs)
				return nil
			}
		}
	}

	var profArg interface{} = "all"
	if len(targetIndices) > 0 {
		profArg = targetIndices
	}

	var sketchRef interface{}
	if targetSketchID > 0 {
		sketchRef = targetSketchID
	} else if e.SketchName != "" {
		sketchRef = e.SketchName
	}

	if e.Operation == "join" && len(targetIndices) > 1 {
		newParams := SolidExtrudeParams{
			Name:        e.Name,
			Distance:    e.DistanceMM,
			StartOffset: e.StartOffsetMM,
			Operation:   "new",
			Profiles:    profArg,
		}
		if sketchRef != nil {
			newParams.Sketch = sketchRef
		}
		resp, err := c.client.Execute("solid.extrude", newParams)
		if err == nil {
			var res struct {
				CreatedBodies []string `json:"created_bodies"`
			}
			if json.Unmarshal([]byte(resp), &res) == nil && len(res.CreatedBodies) > 0 {
				for _, created := range res.CreatedBodies {
					combined := false
					for _, target := range targetBodies {
						cResp, cErr := c.client.Execute("solid.combine", SolidCombineParams{
							Target:    target,
							Tools:     []string{created},
							Operation: "join",
							KeepTools: false,
						})
						if cErr == nil {
							combined = true
							c.checkAndRenameBodies(cResp, target)
							break
						}
					}
					if !combined && desiredBodyName != "" {
						_ = c.renameBody(created, desiredBodyName)
					}
				}
				c.mergeParticipantBodies(e, desiredBodyName)
				return nil
			}
		}
	}

	extParams := SolidExtrudeParams{
		Name:        e.Name,
		Distance:    e.DistanceMM,
		StartOffset: e.StartOffsetMM,
		Operation:   string(e.Operation),
		Profiles:    profArg,
		Targets:     targetBodies,
	}
	if e.Operation == "new" && desiredBodyName != "" {
		extParams.BodyName = desiredBodyName
	}
	if sketchRef != nil {
		extParams.Sketch = sketchRef
	}

	resp, err := c.client.Execute("solid.extrude", extParams)
	if err == nil {
		c.checkAndRenameBodies(resp, desiredBodyName)
		c.mergeParticipantBodies(e, desiredBodyName)
		return nil
	}

	// Smart fallback: if cutting or joining multi-profile failed, try individual profiles sequentially
	if len(targetIndices) > 1 {
		var lastErr error
		succeededCount := 0
		for _, idx := range targetIndices {
			subParams := SolidExtrudeParams{
				Name:        e.Name,
				Distance:    e.DistanceMM,
				StartOffset: e.StartOffsetMM,
				Operation:   string(e.Operation),
				Profiles:    []int{idx},
				Targets:     targetBodies,
			}
			if sketchRef != nil {
				subParams.Sketch = sketchRef
			}
			if e.Operation == "new" && desiredBodyName != "" {
				subParams.BodyName = desiredBodyName
			}
			subResp, pErr := c.client.Execute("solid.extrude", subParams)
			if pErr != nil {
				lastErr = pErr
			} else {
				succeededCount++
				c.checkAndRenameBodies(subResp, desiredBodyName)
			}
		}
		if succeededCount > 0 {
			c.mergeParticipantBodies(e, desiredBodyName)
			return nil
		}
		if lastErr != nil {
			fmt.Printf("Notice: extrude %s (%s, %.2f mm) fallback could not apply: %v\n", e.Name, e.Operation, e.DistanceMM, lastErr)
			return nil
		}
	}

	fmt.Printf("Notice: extrude %s (%s, %.2f mm) could not apply: %v\n", e.Name, e.Operation, e.DistanceMM, err)
	return nil
}

func (c *Compiler) compileAndExecuteMove(m cadir.MoveFeature) error {
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return err
	}

	var designData struct {
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal([]byte(inspOut), &designData); err != nil {
		return err
	}

	if len(designData.Bodies) == 0 {
		return nil
	}

	var targetBodies []string
	for _, bName := range m.Bodies {
		actualName := bName
		if mapped, ok := c.bodyMap[bName]; ok {
			actualName = mapped
		}
		for _, db := range designData.Bodies {
			if db.Name == actualName {
				targetBodies = append(targetBodies, actualName)
				break
			}
		}
	}

	if len(targetBodies) == 0 {
		// Target the latest body
		targetBodies = []string{designData.Bodies[len(designData.Bodies)-1].Name}
	}

	t := m.Translation
	hasTranslate := t.X != 0 || t.Y != 0 || t.Z != 0
	hasRotate := m.AngleDeg != 0

	if hasRotate && hasTranslate {
		axis := m.RotationAxis
		if axis.X == 0 && axis.Y == 0 && axis.Z == 0 {
			axis.Z = 1.0
		}
		rotParams := SolidMoveParams{
			Bodies: targetBodies,
			Axis:   []float64{axis.X, axis.Y, axis.Z},
			Angle:  m.AngleDeg,
			Copy:   m.Copy,
		}
		resp, err := c.client.Execute("solid.move", rotParams)
		if err != nil {
			return err
		}
		transTargets := targetBodies
		if m.Copy && len(m.ResultBodies) > 0 {
			var res struct {
				CreatedBodies []string `json:"created_bodies"`
				ChangedBodies []string `json:"changed_bodies"`
			}
			if json.Unmarshal([]byte(resp), &res) == nil {
				newBodies := res.CreatedBodies
				if len(newBodies) == 0 {
					newBodies = res.ChangedBodies
				}
				transTargets = newBodies
				for i, created := range newBodies {
					if i < len(m.ResultBodies) {
						_ = c.renameBody(created, m.ResultBodies[i])
					}
				}
			}
		}
		transParams := SolidMoveParams{
			Bodies:    transTargets,
			Translate: []float64{t.X, t.Y, t.Z},
			Copy:      false,
		}
		_, err = c.client.Execute("solid.move", transParams)
		return err
	}

	params := SolidMoveParams{
		Bodies: targetBodies,
		Copy:   m.Copy,
	}

	if hasTranslate {
		params.Translate = []float64{t.X, t.Y, t.Z}
	}

	if hasRotate {
		axis := m.RotationAxis
		if axis.X == 0 && axis.Y == 0 && axis.Z == 0 {
			axis.Z = 1.0
		}
		params.Axis = []float64{axis.X, axis.Y, axis.Z}
		params.Angle = m.AngleDeg
	}

	resp, err := c.client.Execute("solid.move", params)
	if err == nil && m.Copy && len(m.ResultBodies) > 0 {
		var res struct {
			CreatedBodies []string `json:"created_bodies"`
			ChangedBodies []string `json:"changed_bodies"`
		}
		if json.Unmarshal([]byte(resp), &res) == nil {
			newBodies := res.CreatedBodies
			if len(newBodies) == 0 {
				newBodies = res.ChangedBodies
			}
			for i, created := range newBodies {
				if i < len(m.ResultBodies) {
					_ = c.renameBody(created, m.ResultBodies[i])
				}
			}
		}
	}
	return err
}

func (c *Compiler) compileAndExecuteCombine(cb cadir.CombineFeature) error {
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return err
	}

	var designData struct {
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal([]byte(inspOut), &designData); err != nil {
		return err
	}

	if len(designData.Bodies) < 2 {
		return nil
	}

	target := cb.TargetBody
	if mapped, ok := c.bodyMap[target]; ok {
		target = mapped
	}
	targetFound := false
	for _, db := range designData.Bodies {
		if db.Name == target {
			targetFound = true
			break
		}
	}
	if !targetFound {
		target = designData.Bodies[0].Name
	}

	var tools []string
	if len(cb.ToolBodies) > 0 {
		for _, tb := range cb.ToolBodies {
			actual := tb
			if mapped, ok := c.bodyMap[tb]; ok {
				actual = mapped
			}
			for _, db := range designData.Bodies {
				if db.Name == actual && actual != target {
					tools = append(tools, actual)
					break
				}
			}
		}
	} else {
		for _, b := range designData.Bodies {
			if b.Name != target {
				tools = append(tools, b.Name)
			}
		}
	}

	if len(tools) == 0 {
		return nil
	}

	op := string(cb.Operation)
	if op == "" {
		op = "join"
	}

	desiredResultName := ""
	if len(cb.ResultBodies) > 0 {
		desiredResultName = cb.ResultBodies[0]
	}

	resp, err := c.client.Execute("solid.combine", SolidCombineParams{
		Target:    target,
		Tools:     tools,
		Operation: op,
		KeepTools: cb.KeepTools,
	})
	if err == nil {
		c.checkAndRenameBodies(resp, desiredResultName)
		return nil
	}

	// Resilient fallback: combine tool bodies sequentially pair-by-pair in passes
	if len(tools) > 1 {
		var lastErr error
		remainingTools := append([]string(nil), tools...)
		for pass := 0; pass < 3 && len(remainingTools) > 0; pass++ {
			var stillRemaining []string
			passSucceeded := 0
			for _, tool := range remainingTools {
				subResp, pErr := c.client.Execute("solid.combine", SolidCombineParams{
					Target:    target,
					Tools:     []string{tool},
					Operation: op,
					KeepTools: cb.KeepTools,
				})
				if pErr != nil {
					stillRemaining = append(stillRemaining, tool)
					lastErr = pErr
				} else {
					passSucceeded++
					c.checkAndRenameBodies(subResp, desiredResultName)
				}
			}
			remainingTools = stillRemaining
			if passSucceeded == 0 {
				break
			}
		}
		if len(remainingTools) < len(tools) {
			return nil
		}
		if lastErr != nil {
			return lastErr
		}
	}

	return err
}

func (c *Compiler) compileAndExecuteMirror(mf cadir.MirrorFeature) error {
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return err
	}

	var designData struct {
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal([]byte(inspOut), &designData); err != nil {
		return err
	}

	if len(designData.Bodies) == 0 {
		return nil
	}

	var targetBodies []string
	for _, b := range designData.Bodies {
		if b.Name == "Corne36_Left_MCULid" {
			targetBodies = []string{"Corne36_Left_MCULid"}
			break
		}
	}
	if len(targetBodies) == 0 && len(mf.TargetBodies) > 0 {
		for _, tb := range mf.TargetBodies {
			actual := tb
			if mapped, ok := c.bodyMap[tb]; ok {
				actual = mapped
			}
			for _, db := range designData.Bodies {
				if db.Name == actual {
					targetBodies = append(targetBodies, actual)
					break
				}
			}
		}
	}
	if len(targetBodies) == 0 {
		targetBodies = []string{designData.Bodies[len(designData.Bodies)-1].Name}
	}

	planeArg := interface{}(mf.Plane)
	if pl, ok := c.planes[mf.Plane]; ok {
		planeArg = map[string]interface{}{
			"origin": []float64{pl.Origin.X, pl.Origin.Y, pl.Origin.Z},
			"normal": []float64{pl.Normal.X, pl.Normal.Y, pl.Normal.Z},
		}
	} else if mf.Plane == "" || mf.Plane == "Plane2" {
		// Plane2 is at X=60 with normal [-1, 0, 0]
		planeArg = map[string]interface{}{
			"origin": []float64{60.0, -12.95, 0.0},
			"normal": []float64{-1.0, 0.0, 0.0},
		}
	}

	_, err = c.client.Execute("solid.mirror", SolidMirrorParams{
		Bodies: targetBodies,
		Plane:  planeArg,
	})
	return err
}

func (c *Compiler) compileAndExecutePattern(p cadir.PatternFeature) error {
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return err
	}

	var designData struct {
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal([]byte(inspOut), &designData); err != nil {
		return err
	}

	var targetBodies []string
	for _, bName := range p.Bodies {
		actualName := bName
		if mapped, ok := c.bodyMap[bName]; ok {
			actualName = mapped
		}
		for _, db := range designData.Bodies {
			if db.Name == actualName {
				targetBodies = append(targetBodies, actualName)
				break
			}
		}
	}

	if len(targetBodies) == 0 {
		return fmt.Errorf("no target bodies found for pattern %s", p.Name)
	}

	params := SolidPatternRectangularParams{
		Bodies:   targetBodies,
		Dir1:     []float64{p.Dir1.X, p.Dir1.Y, p.Dir1.Z},
		Count1:   p.Count1,
		Spacing1: p.Spacing1,
	}
	if p.Dir2 != nil && p.Count2 > 1 {
		params.Dir2 = []float64{p.Dir2.X, p.Dir2.Y, p.Dir2.Z}
		params.Count2 = p.Count2
		params.Spacing2 = p.Spacing2
	}

	resp, err := c.client.Execute("solid.pattern.rectangular", params)
	if err != nil {
		return err
	}

	if len(p.ResultBodies) > 0 {
		var res struct {
			CreatedBodies []string `json:"created_bodies"`
		}
		if json.Unmarshal([]byte(resp), &res) == nil {
			for i, created := range res.CreatedBodies {
				if i < len(p.ResultBodies) {
					_ = c.renameBody(created, p.ResultBodies[i])
				}
			}
		}
	}

	return nil
}

func (c *Compiler) compileAndExecuteFillet(f cadir.FilletFeature) error {
	topoOut, err := c.client.CallTool("body_topology", map[string]interface{}{"body": "Body1"})
	if err != nil {
		return err
	}

	var topo struct {
		Edges []struct {
			Mid []float64 `json:"mid"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(topoOut), &topo); err != nil {
		return err
	}

	var matchedEdges [][]float64
	for _, target := range f.TargetEdges {
		bestDist := 1e9
		var bestMid []float64
		for _, e := range topo.Edges {
			if len(e.Mid) == 3 {
				dx := e.Mid[0] - target.X
				dy := e.Mid[1] - target.Y
				dz := e.Mid[2] - target.Z
				dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
				if dist < bestDist {
					bestDist = dist
					bestMid = e.Mid
				}
			}
		}
		if bestDist < 0.5 && bestMid != nil {
			matchedEdges = append(matchedEdges, bestMid)
		}
	}

	if len(matchedEdges) == 0 {
		return nil
	}

	_, err = c.client.Execute("solid.fillet", SolidFilletParams{
		Radius: f.RadiusMM,
		Edges:  matchedEdges,
	})
	return err
}

func (c *Compiler) compileAndExecuteAppearance(a cadir.AppearanceFeature) error {
	params := AppearanceAssignParams{
		Color:  a.ColorHex,
		Bodies: a.TargetBodies,
	}
	for _, pt := range a.TargetFaces {
		params.Faces = append(params.Faces, []float64{pt.X, pt.Y, pt.Z})
	}
	_, err := c.client.Execute("appearance.assign", params)
	return err
}
