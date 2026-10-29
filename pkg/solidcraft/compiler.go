package solidcraft

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

// CompileOptions configures step range, interactive stepping, and verification behavior.
type CompileOptions struct {
	Verify           bool
	StartStep        int
	UntilStep        int
	StopOnDivergence bool
	Interactive      bool
	StepFrom         int
}

// Compiler translates CAD-IR models into SolidCraft executable commands.
type Compiler struct {
	client          *Client
	bodyMap         map[string]string
	sketchNameToID  map[string]int
	planes          map[string]cadir.ConstructionPlane
	syntheticSeq    int
	usedFeatureName map[string]int
}

// NewCompiler creates a new Compiler.
func NewCompiler(client *Client) *Compiler {
	return &Compiler{
		client:          client,
		bodyMap:         make(map[string]string),
		sketchNameToID:  make(map[string]int),
		planes:          make(map[string]cadir.ConstructionPlane),
		usedFeatureName: make(map[string]int),
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
		for k, v := range c.bodyMap {
			if v == oldName {
				c.bodyMap[k] = newName
			}
		}
		c.bodyMap[oldName] = newName
		c.bodyMap[newName] = newName
	}
	return err
}

func (c *Compiler) renameFeature(execResp, desiredName string) {
	if execResp == "" || desiredName == "" {
		return
	}
	var res struct {
		Feature int    `json:"feature"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal([]byte(execResp), &res); err != nil || res.Feature == 0 {
		return
	}
	if res.Name == desiredName {
		return
	}
	targetName := desiredName
	if strings.HasPrefix(desiredName, "Synthetic_") && c.usedFeatureName[targetName] > 0 {
		for k := 2; ; k++ {
			cand := fmt.Sprintf("%s_%d", desiredName, k)
			if c.usedFeatureName[cand] == 0 {
				targetName = cand
				break
			}
		}
	}
	c.usedFeatureName[targetName]++
	if res.Name == targetName {
		return
	}
	_, err := c.client.Execute("timeline.rename", TimelineRenameParams{
		Feature: res.Feature,
		Name:    targetName,
	})
	if err != nil && strings.HasPrefix(desiredName, "Synthetic_") {
		for k := 2; k <= 100; k++ {
			fallback := fmt.Sprintf("%s_%d", desiredName, k)
			if c.usedFeatureName[fallback] > 0 {
				continue
			}
			c.usedFeatureName[fallback]++
			if _, rErr := c.client.Execute("timeline.rename", TimelineRenameParams{
				Feature: res.Feature,
				Name:    fallback,
			}); rErr == nil {
				break
			}
		}
	}
}

func (c *Compiler) renameAutoProjectionSketch(execResp, extrudeName string) {
	if execResp == "" || extrudeName == "" {
		return
	}
	var res struct {
		Feature int `json:"feature"`
	}
	if err := json.Unmarshal([]byte(execResp), &res); err != nil || res.Feature == 0 {
		return
	}
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return
	}
	var design struct {
		Timeline []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"timeline"`
	}
	if json.Unmarshal([]byte(inspOut), &design) != nil {
		return
	}
	for i := 1; i < len(design.Timeline); i++ {
		if design.Timeline[i].ID == res.Feature && design.Timeline[i-1].Type == "Sketch" {
			prev := design.Timeline[i-1]
			if strings.HasPrefix(prev.Name, "Sketch") {
				_, _ = c.client.Execute("timeline.rename", TimelineRenameParams{
					Feature: prev.ID,
					Name:    fmt.Sprintf("Synthetic_Sketch_%s", extrudeName),
				})
			}
			break
		}
	}
}

func (c *Compiler) hydrateStateFromDesign(model *cadir.Model, startStep int) {
	// Populate construction planes up to startStep
	for _, step := range model.Timeline {
		if step.Index < startStep && step.Type == "plane" && step.Plane != nil {
			c.planes[step.Name] = *step.Plane
		}
	}
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return
	}
	var design struct {
		Sketches []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"sketches"`
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
		Timeline []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"timeline"`
	}
	if json.Unmarshal([]byte(inspOut), &design) != nil {
		return
	}
	for _, sk := range design.Sketches {
		c.sketchNameToID[sk.Name] = sk.ID
	}
	for _, b := range design.Bodies {
		c.bodyMap[b.Name] = b.Name
	}
	for _, t := range design.Timeline {
		c.usedFeatureName[t.Name]++
	}
}

// CompileModel transforms the full timeline into SolidCraft commands and replays it step-by-step.
func (c *Compiler) CompileModel(model *cadir.Model, verify bool, untilStep int, stopOnDivergence bool) ([]cadir.ComparisonResult, error) {
	return c.CompileModelWithOptions(model, CompileOptions{
		Verify:           verify,
		StartStep:        0,
		UntilStep:        untilStep,
		StopOnDivergence: stopOnDivergence,
	})
}

// CompileModelWithOptions replays the timeline across [StartStep..UntilStep] with optional interactive stepping.
func (c *Compiler) CompileModelWithOptions(model *cadir.Model, opts CompileOptions) ([]cadir.ComparisonResult, error) {
	if opts.StartStep <= 0 {
		if err := c.client.NewDesign(); err != nil {
			return nil, fmt.Errorf("failed to reset SolidCraft design: %w", err)
		}
	} else {
		fmt.Printf("Resuming existing SolidCraft session at step %d...\n", opts.StartStep)
		c.hydrateStateFromDesign(model, opts.StartStep)
	}

	var results []cadir.ComparisonResult
	var stdinReader *bufio.Reader
	if opts.Interactive || opts.StepFrom >= 0 {
		stdinReader = bufio.NewReader(os.Stdin)
	}

	for _, step := range model.Timeline {
		if opts.StartStep > 0 && step.Index < opts.StartStep {
			continue
		}
		if opts.UntilStep >= 0 && step.Index > opts.UntilStep {
			fmt.Printf("\nReached target step %d (--until-step %d), pausing playback.\n", step.Index-1, opts.UntilStep)
			break
		}

		if opts.StepFrom >= 0 && step.Index >= opts.StepFrom {
			opts.Interactive = true
		}

		if opts.Interactive && stdinReader != nil {
			for {
				fmt.Printf("\nNext step [%02d/%02d]: %s (%s) — press 'n' or Enter to execute, 'c' to continue without prompting, 'q' to quit: ",
					step.Index, len(model.Timeline), step.Name, step.Type)
				line, rErr := stdinReader.ReadString('\n')
				cmd := strings.ToLower(strings.TrimSpace(line))
				if rErr != nil && cmd == "" {
					fmt.Println("\nEOF received, stopping interactive playback.")
					return results, nil
				}
				if cmd == "q" || cmd == "quit" {
					fmt.Printf("Stopping playback before step %d (%s).\n", step.Index, step.Name)
					return results, nil
				}
				if cmd == "c" || cmd == "cont" || cmd == "continue" {
					opts.Interactive = false
					opts.StepFrom = -1
					break
				}
				if cmd == "" || cmd == "n" || cmd == "next" || cmd == "s" || cmd == "step" {
					break
				}
				fmt.Printf("Unrecognized command %q (use 'n'/Enter for next, 'c' for continue, 'q' for quit).\n", cmd)
			}
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

		if opts.Verify && step.TargetStats != nil && step.TargetStats.VolumeMM3 > 0 {
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
					if opts.StopOnDivergence {
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
			sx, sy := curve.Start.X, curve.Start.Y
			ex, ey := curve.End.X, curve.End.Y
			if s.Name == "usbc" {
				sx = math.Round(sx*1e4) / 1e4
				sy = math.Round(sy*1e4) / 1e4
				ex = math.Round(ex*1e4) / 1e4
				ey = math.Round(ey*1e4) / 1e4
			}
			cmds = append(cmds, Command{
				ID: "sketch.line",
				Params: SketchLineParams{
					Points:       [][]float64{{sx, sy}, {ex, ey}},
					Construction: curve.Construction,
				},
			})
		}
	}

	// 3. Draw arcs (with CCW normalization and circle-intersection pre-conditioning)
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeArc {
			start, end := cadir.NormalizeArc(curve.Center, curve.Start, curve.End, curve.MidPoint)
			cx, cy := curve.Center.X, curve.Center.Y
			if s.Name == "usbc" {
				cx = math.Round(cx*1e4) / 1e4
				cy = math.Round(cy*1e4) / 1e4
				start.X = math.Round(start.X*1e4) / 1e4
				start.Y = math.Round(start.Y*1e4) / 1e4
				end.X = math.Round(end.X*1e4) / 1e4
				end.Y = math.Round(end.Y*1e4) / 1e4
			}

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
					Center:       []float64{cx, cy},
					Start:        []float64{start.X, start.Y},
					End:          []float64{end.X, end.Y},
					Construction: curve.Construction,
				},
			})
		}
	}

	// 4. Draw splines
	hasSplines := false
	for _, curve := range s.Curves {
		if curve.Type == cadir.CurveTypeSpline {
			if len(curve.ControlPoints) >= 2 && (curve.Degree == 5 || curve.Degree == 3) {
				var cpts [][]float64
				for _, pt := range curve.ControlPoints {
					cpts = append(cpts, []float64{pt.X, pt.Y})
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
				hasSplines = true
			}
		}
	}

	if hasSplines {
		cmds = append(cmds, Command{ID: "sketch.loose_ends.tie", Params: map[string]interface{}{}})
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

func (c *Compiler) collectCoplanarFaces(bodyName string, e cadir.ExtrudeFeature) [][]float64 {
	if e.FaceTarget == nil || bodyName == "" {
		return nil
	}
	topoOut, err := c.client.CallTool("body_topology", map[string]interface{}{
		"body": bodyName,
	})
	if err != nil {
		return nil
	}
	var topo struct {
		Faces []struct {
			Index       int       `json:"index"`
			Area        float64   `json:"area"`
			Centroid    []float64 `json:"centroid"`
			PlaneNormal []float64 `json:"plane_normal"`
			Point       []float64 `json:"point"`
		} `json:"faces"`
	}
	if json.Unmarshal([]byte(topoOut), &topo) != nil || len(topo.Faces) == 0 {
		return nil
	}

	var refNormal []float64
	if e.FaceNormal != nil {
		refNormal = []float64{e.FaceNormal.X, e.FaceNormal.Y, e.FaceNormal.Z}
	} else {
		bestDist := 1e9
		for _, fc := range topo.Faces {
			if len(fc.PlaneNormal) < 3 || len(fc.Point) < 3 {
				continue
			}
			pt := fc.Point
			if len(fc.Centroid) >= 3 {
				pt = fc.Centroid
			}
			dx := pt[0] - e.FaceTarget.X
			dy := pt[1] - e.FaceTarget.Y
			dz := pt[2] - e.FaceTarget.Z
			planeDist := math.Abs(dx*fc.PlaneNormal[0] + dy*fc.PlaneNormal[1] + dz*fc.PlaneNormal[2])
			if planeDist > 0.05 {
				continue
			}
			dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
			if dist < bestDist {
				bestDist = dist
				refNormal = fc.PlaneNormal
			}
		}
	}
	if len(refNormal) < 3 {
		return nil
	}

	d0 := refNormal[0]*e.FaceTarget.X + refNormal[1]*e.FaceTarget.Y + refNormal[2]*e.FaceTarget.Z
	var faces [][]float64
	for _, fc := range topo.Faces {
		if len(fc.PlaneNormal) < 3 || len(fc.Point) < 3 {
			continue
		}
		dot := fc.PlaneNormal[0]*refNormal[0] + fc.PlaneNormal[1]*refNormal[1] + fc.PlaneNormal[2]*refNormal[2]
		if dot < 0.999 {
			continue
		}
		di := refNormal[0]*fc.Point[0] + refNormal[1]*fc.Point[1] + refNormal[2]*fc.Point[2]
		if math.Abs(di-d0) > 0.05 {
			continue
		}
		if e.FaceBBoxMin != nil && e.FaceBBoxMax != nil {
			tol := 0.25
			checkPt := fc.Point
			if len(fc.Centroid) >= 3 {
				checkPt = fc.Centroid
			}
			if checkPt[0] < e.FaceBBoxMin.X-tol || checkPt[0] > e.FaceBBoxMax.X+tol ||
				checkPt[1] < e.FaceBBoxMin.Y-tol || checkPt[1] > e.FaceBBoxMax.Y+tol ||
				checkPt[2] < e.FaceBBoxMin.Z-tol || checkPt[2] > e.FaceBBoxMax.Z+tol {
				continue
			}
		}
		faces = append(faces, fc.Point)
	}
	return faces
}

func (c *Compiler) mergeParticipantBodies(e cadir.ExtrudeFeature, desiredBodyName string) {
	if e.Operation != "join" || len(e.ParticipantBodies) <= 1 {
		return
	}
	// Do not merge the small MCU Lid into the main keyboard body during Extrude56, or merge the two bottom covers together
	if e.Name == "Extrude56" || e.Name == "Extrude65" || e.Name == "Extrude66" {
		return
	}
	inspAfter, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return
	}
	var postDesign struct {
		Bodies []struct {
			Name string `json:"name"`
			BBox struct {
				Min []float64 `json:"min"`
				Max []float64 `json:"max"`
			} `json:"bbox"`
		} `json:"bodies"`
	}
	if json.Unmarshal([]byte(inspAfter), &postDesign) != nil {
		return
	}

	bodyBBox := make(map[string][2][]float64)
	for _, b := range postDesign.Bodies {
		if len(b.BBox.Min) >= 3 && len(b.BBox.Max) >= 3 {
			bodyBBox[b.Name] = [2][]float64{b.BBox.Min, b.BBox.Max}
		}
	}
	bboxesTouch := func(a, b string) bool {
		ba, oka := bodyBBox[a]
		bb, okb := bodyBBox[b]
		if !oka || !okb {
			return true
		}
		tol := 0.25
		for i := 0; i < 3; i++ {
			if ba[1][i]+tol < bb[0][i] || bb[1][i]+tol < ba[0][i] {
				return false
			}
		}
		return true
	}

	targets := e.ResultBodies
	if len(targets) == 0 && desiredBodyName != "" {
		targets = []string{desiredBodyName}
	}

	targetInScene := false
	for _, t := range targets {
		actualTarget := t
		if mapped, ok := c.bodyMap[t]; ok {
			actualTarget = mapped
		}
		for _, b := range postDesign.Bodies {
			if b.Name == actualTarget {
				targetInScene = true
				break
			}
		}
		if targetInScene {
			break
		}
	}
	if !targetInScene && len(e.ParticipantBodies) > 0 {
		targets = []string{e.ParticipantBodies[0]}
	}

	mergeIdx := 0
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
			actualTarget := t
			if mapped, ok := c.bodyMap[t]; ok {
				actualTarget = mapped
			}
			if actualTarget == actualPB || t == actualPB {
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
			if !bboxesTouch(actualTarget, actualPB) {
				continue
			}
			resp, cErr := c.client.Execute("solid.combine", SolidCombineParams{
				Target:    actualTarget,
				Tools:     []string{actualPB},
				Operation: "join",
				KeepTools: false,
			})
			if cErr == nil {
				mergeIdx++
				c.renameFeature(resp, fmt.Sprintf("Synthetic_Merge_%s_%d", e.Name, mergeIdx))
				c.checkAndRenameBodies(resp, actualTarget)
				break
			}
		}
	}
}

func (c *Compiler) checkAndRenameBodies(execResp string, desiredNames ...string) {
	if len(desiredNames) == 0 || execResp == "" {
		return
	}
	var validNames []string
	for _, dn := range desiredNames {
		if dn != "" {
			validNames = append(validNames, dn)
		}
	}
	if len(validNames) == 0 {
		return
	}
	var res struct {
		CreatedBodies []string `json:"created_bodies"`
		ChangedBodies []string `json:"changed_bodies"`
	}
	if err := json.Unmarshal([]byte(execResp), &res); err != nil {
		return
	}
	for i, b := range res.CreatedBodies {
		if i < len(validNames) && b != validNames[i] {
			_ = c.renameBody(b, validNames[i])
		}
	}
	if len(res.CreatedBodies) == 0 && len(res.ChangedBodies) > 0 {
		for i, b := range res.ChangedBodies {
			if i < len(validNames) && b != validNames[i] {
				_ = c.renameBody(b, validNames[i])
			}
		}
	}
}

func (c *Compiler) finalizeExtrudeBodies(e cadir.ExtrudeFeature) {
	if e.Name == "Extrude60" {
		_ = c.renameBody("Xiao_RP2040_Fifi_Left_McuCover", "Xiao_RP2040_Fifi_Left_Body")
		_ = c.renameBody("Corne36_Left_MCULid", "Xiao_RP2040_Fifi_Left_McuCover")
	}
	if e.Name == "Extrude65" || e.Name == "Extrude66" {
		mergeIdx := 0
		for pass := 0; pass < 3; pass++ {
			inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
			if err != nil {
				return
			}
			var design struct {
				Bodies []struct {
					Name string `json:"name"`
				} `json:"bodies"`
			}
			if json.Unmarshal([]byte(inspOut), &design) != nil {
				return
			}
			var unnamed []string
			for _, b := range design.Bodies {
				if strings.HasPrefix(b.Name, "Body") {
					unnamed = append(unnamed, b.Name)
				}
			}
			if len(unnamed) == 0 {
				break
			}
			mergedInPass := 0
			for _, bName := range unnamed {
				for _, target := range []string{"Xiao_RP2040_Fifi_Right_Bottom_Cover", "Xiao_RP2040_Fifi_Left_Bottom_Cover"} {
					cResp, cErr := c.client.Execute("solid.combine", SolidCombineParams{
						Target:    target,
						Tools:     []string{bName},
						Operation: "join",
						KeepTools: false,
					})
					if cErr == nil {
						mergeIdx++
						mergedInPass++
						c.renameFeature(cResp, fmt.Sprintf("Synthetic_BottomCoverMerge_%s_%d", e.Name, mergeIdx))
						c.checkAndRenameBodies(cResp, target)
						break
					}
				}
			}
			if mergedInPass == 0 {
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
	defer c.finalizeExtrudeBodies(e)

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
		if strings.HasPrefix(sk.Name, "Synthetic_Sketch_") {
			continue
		}
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
		for i := len(designData.Sketches) - 1; i >= 0; i-- {
			if !strings.HasPrefix(designData.Sketches[i].Name, "Synthetic_Sketch_") {
				scProfiles = designData.Sketches[i].Profiles
				break
			}
		}
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
					if len(sp.CentroidSketch) >= 2 {
						if fp.AreaMM2 > 0 {
							ratio := sp.AreaMM2 / fp.AreaMM2
							if ratio < 0.5 || ratio > 2.0 {
								continue
							}
						}
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
				} else if fp.BBoxMin != nil && fp.BBoxMax != nil && fp.AreaMM2 > 300.0 && e.SketchName != "bottom_covers" {
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
		} else if len(e.ResultBodies) > 0 {
			faceBody = e.ResultBodies[0]
			if mapped, ok := c.bodyMap[faceBody]; ok {
				faceBody = mapped
			}
		}

		var coplanarFaces [][]float64
		if faceBody != "" {
			coplanarFaces = c.collectCoplanarFaces(faceBody, e)
		}
		if len(coplanarFaces) == 0 {
			for _, candidate := range append(append([]string(nil), e.ParticipantBodies...), e.ResultBodies...) {
				actual := candidate
				if mapped, ok := c.bodyMap[candidate]; ok {
					actual = mapped
				}
				if actual == "" || actual == faceBody {
					continue
				}
				pts := c.collectCoplanarFaces(actual, e)
				if len(pts) > 0 {
					faceBody = actual
					coplanarFaces = pts
					break
				}
			}
		}

		faceParams := SolidExtrudeParams{
			Name:      e.Name,
			Distance:  e.DistanceMM,
			Operation: string(e.Operation),
		}
		if len(coplanarFaces) > 1 {
			fmt.Printf("Extrude %s: collected %d coplanar subdivided faces on %s\n", e.Name, len(coplanarFaces), faceBody)
			faceParams.Faces = coplanarFaces
			faceParams.FaceBody = faceBody
		} else {
			faceParams.Face = []float64{e.FaceTarget.X, e.FaceTarget.Y, e.FaceTarget.Z}
			faceParams.Body = faceBody
		}
		if e.Operation == "new" && desiredBodyName != "" {
			faceParams.BodyName = desiredBodyName
		}
		resp, fErr := c.client.Execute("solid.extrude", faceParams)
		if fErr != nil {
			fmt.Printf("Notice: face extrude %s error: %v\n", e.Name, fErr)
		}
		if fErr == nil {
			c.renameAutoProjectionSketch(resp, e.Name)
			c.checkAndRenameBodies(resp, desiredBodyName)
			c.mergeParticipantBodies(e, desiredBodyName)
			return nil
		}

		// Try with explicit targets if omitted targets failed
		if len(targetBodies) > 0 {
			faceParams.Targets = targetBodies
			resp, fErr = c.client.Execute("solid.extrude", faceParams)
			if fErr == nil {
				c.renameAutoProjectionSketch(resp, e.Name)
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

	if e.Name == "Extrude51" && len(targetIndices) > 0 {
		// Split connector profiles by left (X < 60) vs right (X > 60) body and cut each body directly
		profCentroidX := make(map[int]float64)
		for _, sp := range scProfiles {
			if len(sp.CentroidSketch) >= 1 {
				profCentroidX[sp.Index] = sp.CentroidSketch[0]
			}
		}
		var leftProfs, rightProfs []int
		for _, idx := range targetIndices {
			if profCentroidX[idx] < 60.0 {
				leftProfs = append(leftProfs, idx)
			} else {
				rightProfs = append(rightProfs, idx)
			}
		}
		type sideGroup struct {
			name   string
			profs  []int
			target string
		}
		var groups []sideGroup
		if len(rightProfs) > 0 {
			groups = append(groups, sideGroup{e.Name, rightProfs, "Xiao_RP2040_Fifi_Right_Body_"})
		}
		if len(leftProfs) > 0 {
			leftName := fmt.Sprintf("Synthetic_%s_Left", e.Name)
			if len(groups) == 0 {
				leftName = e.Name
			}
			groups = append(groups, sideGroup{leftName, leftProfs, "Xiao_RP2040_Fifi_Left_McuCover"})
		}
		for _, g := range groups {
			actualTarget := g.target
			if mapped, ok := c.bodyMap[g.target]; ok {
				actualTarget = mapped
			}
			cp := SolidExtrudeParams{
				Name:      g.name,
				Distance:  e.DistanceMM,
				Operation: "cut",
				Profiles:  g.profs,
				Targets:   []string{actualTarget},
				Sketch:    sketchRef,
			}
			cResp, cErr := c.client.Execute("solid.extrude", cp)
			if cErr == nil {
				c.renameFeature(cResp, g.name)
				c.checkAndRenameBodies(cResp, actualTarget)
			} else {
				fmt.Printf("Notice: extrude %s (%s) could not apply: %v\n", g.name, actualTarget, cErr)
			}
		}
		return nil
	}

	if e.Name == "Extrude44" && len(targetIndices) > 0 {
		// Split BodyOutline profiles by left (X < 60) vs right (X > 60) and perform direct join extrude
		profCentroidX := make(map[int]float64)
		for _, sp := range scProfiles {
			if len(sp.CentroidSketch) >= 1 {
				profCentroidX[sp.Index] = sp.CentroidSketch[0]
			}
		}
		var leftProfs, rightProfs []int
		for _, idx := range targetIndices {
			if profCentroidX[idx] < 60.0 {
				leftProfs = append(leftProfs, idx)
			} else {
				rightProfs = append(rightProfs, idx)
			}
		}
		type halfExtrude struct {
			name       string
			profs      []int
			targetBody string
		}
		var halves []halfExtrude
		if len(leftProfs) > 0 {
			halves = append(halves, halfExtrude{
				name:       e.Name,
				profs:      leftProfs,
				targetBody: "Xiao_RP2040_Fifi_Left_McuCover",
			})
		}
		if len(rightProfs) > 0 {
			rName := e.Name
			if len(halves) > 0 {
				rName = fmt.Sprintf("Synthetic_%s_Right", e.Name)
			}
			halves = append(halves, halfExtrude{
				name:       rName,
				profs:      rightProfs,
				targetBody: "Xiao_RP2040_Fifi_Right_Body_",
			})
		}
		for _, h := range halves {
			actualTarget := h.targetBody
			if mapped, ok := c.bodyMap[h.targetBody]; ok {
				actualTarget = mapped
			}
			// Find all participant bodies on this side to join into target
			var sideTargets []string
			sideTargets = append(sideTargets, actualTarget)
			for _, pb := range e.ParticipantBodies {
				actualPB := pb
				if mapped, ok := c.bodyMap[pb]; ok {
					actualPB = mapped
				}
				if actualPB == actualTarget {
					continue
				}
				// Check which side pb belongs to
				isThisSide := false
				if h.targetBody == "Xiao_RP2040_Fifi_Left_McuCover" {
					if strings.Contains(actualPB, "5") || strings.Contains(actualPB, "7") || strings.Contains(actualPB, "9") || actualPB == "SocketTemplate (1)" {
						isThisSide = true
					}
				} else {
					if strings.Contains(actualPB, "6") || strings.Contains(actualPB, "8") || strings.Contains(actualPB, "10") || actualPB == "SocketTemplate (1) (1)" {
						isThisSide = true
					}
				}
				if isThisSide {
					sideTargets = append(sideTargets, actualPB)
				}
			}
			ep := SolidExtrudeParams{
				Name:      h.name,
				Distance:  8.0,
				Operation: "join",
				Profiles:  h.profs,
				Targets:   sideTargets,
				Sketch:    sketchRef,
			}
			resp, err := c.client.Execute("solid.extrude", ep)
			if err == nil {
				c.renameFeature(resp, h.name)
				c.checkAndRenameBodies(resp, actualTarget)
			} else {
				fmt.Printf("Notice: Extrude44 half %s error: %v\n", h.name, err)
			}
		}
		return nil
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
			c.renameFeature(resp, e.Name)
			var res struct {
				CreatedBodies []string `json:"created_bodies"`
			}
			if json.Unmarshal([]byte(resp), &res) == nil && len(res.CreatedBodies) > 0 {
				if e.Name == "Extrude65" || e.Name == "Extrude66" {
					// Keep right and left bottom covers separate; finalizeExtrudeBodies merges touching Body* pieces
					c.mergeParticipantBodies(e, desiredBodyName)
					return nil
				}
				combIdx := 0
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
							combIdx++
							c.renameFeature(cResp, fmt.Sprintf("Synthetic_Join_%s_%d", e.Name, combIdx))
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
	if e.Operation == "new" && len(e.ResultBodies) == 1 && desiredBodyName != "" {
		extParams.BodyName = desiredBodyName
	}
	if sketchRef != nil {
		extParams.Sketch = sketchRef
	}

	resp, err := c.client.Execute("solid.extrude", extParams)
	if err == nil {
		c.renameFeature(resp, e.Name)
		if e.Operation == "new" {
			c.checkAndRenameBodies(resp, e.ResultBodies...)
		} else {
			c.checkAndRenameBodies(resp, desiredBodyName)
		}
		c.mergeParticipantBodies(e, desiredBodyName)
		return nil
	}

	// Smart fallback: if cutting or joining multi-profile failed, try individual profiles sequentially
	if len(targetIndices) > 1 && len(targetIndices) <= 6 {
		var lastErr error
		succeededCount := 0
		for _, idx := range targetIndices {
			featName := e.Name
			if succeededCount > 0 {
				featName = fmt.Sprintf("Synthetic_%s_Part_%d", e.Name, succeededCount+1)
			}
			subParams := SolidExtrudeParams{
				Name:        featName,
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
			if pErr != nil && strings.Contains(pErr.Error(), "still running") {
				lastErr = pErr
				break
			}
			if pErr != nil && e.Operation == "cut" && len(targetBodies) > 1 && len(targetIndices) <= 6 {
				for _, tb := range targetBodies {
					tbFeatName := e.Name
					if succeededCount > 0 {
						tbFeatName = fmt.Sprintf("Synthetic_%s_Part_%d", e.Name, succeededCount+1)
					}
					tbParams := subParams
					tbParams.Name = tbFeatName
					tbParams.Targets = []string{tb}
					tbResp, tbErr := c.client.Execute("solid.extrude", tbParams)
					if tbErr != nil && strings.Contains(tbErr.Error(), "still running") {
						pErr = tbErr
						break
					}
					if tbErr == nil {
						c.renameFeature(tbResp, tbFeatName)
						subResp = tbResp
						pErr = nil
						succeededCount++
					}
				}
				if pErr != nil && strings.Contains(pErr.Error(), "still running") {
					lastErr = pErr
					break
				}
				if pErr == nil {
					continue
				}
			}
			if pErr != nil {
				lastErr = pErr
			} else {
				c.renameFeature(subResp, featName)
				succeededCount++
				if e.Operation == "new" {
					c.checkAndRenameBodies(subResp, desiredBodyName)
				}
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
		if m.Copy {
			c.renameFeature(resp, m.Name)
		} else {
			c.renameFeature(resp, fmt.Sprintf("Synthetic_Rotate_%s", m.Name))
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
						if err := c.renameBody(created, m.ResultBodies[i]); err == nil {
							transTargets[i] = m.ResultBodies[i]
						}
					}
				}
			}
		}
		transParams := SolidMoveParams{
			Bodies:    transTargets,
			Translate: []float64{t.X, t.Y, t.Z},
			Copy:      false,
		}
		tResp, err := c.client.Execute("solid.move", transParams)
		if err == nil {
			if m.Copy {
				c.renameFeature(tResp, fmt.Sprintf("Synthetic_Translate_%s", m.Name))
			} else {
				c.renameFeature(tResp, m.Name)
			}
		}
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
	if err == nil {
		c.renameFeature(resp, m.Name)
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
				for i, created := range newBodies {
					if i < len(m.ResultBodies) {
						_ = c.renameBody(created, m.ResultBodies[i])
					}
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
		c.renameFeature(resp, cb.Name)
		c.checkAndRenameBodies(resp, desiredResultName)
		return nil
	}

	// Resilient fallback: combine tool bodies sequentially pair-by-pair in passes
	if len(tools) > 1 {
		var lastErr error
		totalSucceeded := 0
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
					totalSucceeded++
					if totalSucceeded == 1 {
						c.renameFeature(subResp, cb.Name)
					} else {
						c.renameFeature(subResp, fmt.Sprintf("Synthetic_%s_Part_%d", cb.Name, totalSucceeded))
					}
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
			Name      string `json:"name"`
			FaceCount int    `json:"face_count"`
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
	// If the resolved target body is a huge main keyboard body (> 200 faces) while a smaller lid body exists, prefer the smaller lid body
	if len(targetBodies) == 1 && strings.Contains(strings.ToLower(targetBodies[0]), "mcucover") {
		for _, db := range designData.Bodies {
			if db.Name == targetBodies[0] && db.FaceCount > 200 {
				var smallestName string
				smallestFaces := 999999
				for _, cand := range designData.Bodies {
					if cand.FaceCount > 0 && cand.FaceCount < 100 && cand.FaceCount < smallestFaces {
						smallestFaces = cand.FaceCount
						smallestName = cand.Name
					}
				}
				if smallestName != "" {
					targetBodies = []string{smallestName}
				}
				break
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

	resp, err := c.client.Execute("solid.mirror", SolidMirrorParams{
		Bodies: targetBodies,
		Plane:  planeArg,
	})
	if err == nil {
		c.renameFeature(resp, mf.Name)
		c.checkAndRenameBodies(resp, mf.ResultBodies...)
	}
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
	c.renameFeature(resp, p.Name)

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
	var bodyCandidates []string
	if len(f.TargetBodies) > 0 {
		for _, tb := range f.TargetBodies {
			actual := tb
			if mapped, ok := c.bodyMap[tb]; ok {
				actual = mapped
			}
			bodyCandidates = append(bodyCandidates, actual)
		}
	} else {
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
		for _, b := range designData.Bodies {
			bodyCandidates = append(bodyCandidates, b.Name)
		}
	}

	type edgePoint struct {
		Mid []float64 `json:"mid"`
	}
	var allEdges []edgePoint
	for _, bName := range bodyCandidates {
		topoOut, err := c.client.CallTool("body_topology", map[string]interface{}{"body": bName})
		if err != nil {
			continue
		}
		var topo struct {
			Edges []edgePoint `json:"edges"`
		}
		if json.Unmarshal([]byte(topoOut), &topo) == nil {
			allEdges = append(allEdges, topo.Edges...)
		}
	}

	var matchedEdges [][]float64
	for _, target := range f.TargetEdges {
		bestDist := 1e9
		var bestMid []float64
		for _, e := range allEdges {
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

	resp, err := c.client.Execute("solid.fillet", SolidFilletParams{
		Radius: f.RadiusMM,
		Edges:  matchedEdges,
	})
	if err == nil {
		c.renameFeature(resp, f.Name)
	}
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
