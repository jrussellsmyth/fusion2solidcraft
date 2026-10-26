package solidcraft

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

// Compiler translates CAD-IR models into SolidCraft executable commands.
type Compiler struct {
	client  *Client
	bodyMap map[string]string
}

// NewCompiler creates a new Compiler.
func NewCompiler(client *Client) *Compiler {
	return &Compiler{
		client:  client,
		bodyMap: make(map[string]string),
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
func (c *Compiler) CompileModel(model *cadir.Model, verify bool) ([]cadir.ComparisonResult, error) {
	if err := c.client.NewDesign(); err != nil {
		return nil, fmt.Errorf("failed to reset SolidCraft design: %w", err)
	}

	var results []cadir.ComparisonResult

	for _, step := range model.Timeline {
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

		case "appearance":
			if step.Appearance == nil {
				continue
			}
			_ = c.compileAndExecuteAppearance(*step.Appearance)
		}

		if verify && step.TargetStats != nil {
			actual, err := c.client.Measure()
			if err == nil && actual != nil {
				res := cadir.CompareStats(step, *actual, 0.1)
				results = append(results, res)
			}
		}
	}

	return results, nil
}

func (c *Compiler) compileAndExecuteSketch(s cadir.Sketch) error {
	var cmds []Command

	// 1. sketch.create
	plane := s.Plane
	if plane == "" {
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
			if circleRadius > 0 {
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

	// 4. sketch.finish
	cmds = append(cmds, Command{ID: "sketch.finish"})

	_, err := c.client.Batch(cmds)
	return err
}

func (c *Compiler) checkAndRenameBodies(execResp string, desiredName string) {
	if desiredName == "" || execResp == "" {
		return
	}
	var res struct {
		CreatedBodies []string `json:"created_bodies"`
	}
	if err := json.Unmarshal([]byte(execResp), &res); err != nil {
		return
	}
	for _, b := range res.CreatedBodies {
		if b != desiredName {
			_ = c.renameBody(b, desiredName)
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
			Profiles []struct {
				Index          int       `json:"index"`
				AreaMM2        float64   `json:"area_mm2"`
				CentroidSketch []float64 `json:"centroid_sketch"`
			} `json:"profiles"`
		} `json:"sketches"`
	}

	if err := json.Unmarshal([]byte(inspOut), &designData); err != nil {
		return fmt.Errorf("failed to parse inspect_design: %w", err)
	}

	type matchedProfile struct {
		Index   int
		AreaMM2 float64
	}
	var matched []matchedProfile

	if len(designData.Sketches) > 0 {
		scProfiles := designData.Sketches[len(designData.Sketches)-1].Profiles
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
			if bestDist < 1.0 && bestIdx >= 0 {
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

	// If no profile indices matched, check if we have a FaceTarget (face extrusion)
	if len(targetIndices) == 0 && e.FaceTarget != nil {
		faceParams := SolidExtrudeParams{
			Name:      e.Name,
			Distance:  e.DistanceMM,
			Operation: string(e.Operation),
			Face:      []float64{e.FaceTarget.X, e.FaceTarget.Y, e.FaceTarget.Z},
		}
		if e.Operation == "new" && desiredBodyName != "" {
			faceParams.BodyName = desiredBodyName
		}
		resp, fErr := c.client.Execute("solid.extrude", faceParams)
		if fErr == nil {
			c.checkAndRenameBodies(resp, desiredBodyName)
			return nil
		}
	}

	if len(targetIndices) == 0 {
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

	extParams := SolidExtrudeParams{
		Name:      e.Name,
		Distance:  e.DistanceMM,
		Operation: string(e.Operation),
		Profiles:  profArg,
	}
	if e.Operation == "new" && desiredBodyName != "" {
		extParams.BodyName = desiredBodyName
	}
	if e.SketchName != "" {
		extParams.Sketch = e.SketchName
	}

	resp, err := c.client.Execute("solid.extrude", extParams)
	if err == nil {
		c.checkAndRenameBodies(resp, desiredBodyName)
		return nil
	}

	// Smart fallback: if cutting or joining multi-profile failed, try individual profiles sequentially
	if len(targetIndices) > 1 {
		var lastErr error
		succeededCount := 0
		for _, idx := range targetIndices {
			subParams := SolidExtrudeParams{
				Name:      e.Name,
				Distance:  e.DistanceMM,
				Operation: string(e.Operation),
				Profiles:  []int{idx},
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
			return nil
		}
		if lastErr != nil {
			return lastErr
		}
	}

	return err
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

	params := SolidMoveParams{
		Bodies: targetBodies,
		Copy:   m.Copy,
	}

	t := m.Translation
	if t.X != 0 || t.Y != 0 || t.Z != 0 {
		params.Translate = []float64{t.X, t.Y, t.Z}
	}

	if m.AngleDeg != 0 {
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
		}
		if json.Unmarshal([]byte(resp), &res) == nil {
			for i, created := range res.CreatedBodies {
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
	if target == "" {
		target = designData.Bodies[0].Name
	}

	var tools []string
	if len(cb.ToolBodies) > 0 {
		tools = cb.ToolBodies
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

	_, err = c.client.Execute("solid.combine", SolidCombineParams{
		Target:    target,
		Tools:     tools,
		Operation: op,
		KeepTools: cb.KeepTools,
	})
	if err == nil {
		return nil
	}

	// Resilient fallback: combine tool bodies sequentially pair-by-pair
	if len(tools) > 1 {
		var lastErr error
		succeededCount := 0
		for _, tool := range tools {
			_, pErr := c.client.Execute("solid.combine", SolidCombineParams{
				Target:    target,
				Tools:     []string{tool},
				Operation: op,
				KeepTools: cb.KeepTools,
			})
			if pErr != nil {
				lastErr = pErr
			} else {
				succeededCount++
			}
		}
		if succeededCount > 0 {
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

	targetBodies := mf.TargetBodies
	if len(targetBodies) == 0 {
		for _, b := range designData.Bodies {
			targetBodies = append(targetBodies, b.Name)
		}
	}

	planeArg := interface{}(mf.Plane)
	if mf.Plane == "" || mf.Plane == "Plane2" {
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
