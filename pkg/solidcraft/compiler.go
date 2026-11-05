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
	SkipSteps        map[int]bool
	// DisableBodyRenames skips all body.rename commands — diagnostic only:
	// body renames are load-bearing for later body targeting.
	DisableBodyRenames bool
	// DisableTimelineRenames skips all timeline.rename commands —
	// diagnostic: suspected of invalidating internal face references.
	DisableTimelineRenames bool
}

// Compiler translates CAD-IR models into SolidCraft executable commands.
type Compiler struct {
	client                 *Client
	bodyMap                map[string]string
	sketchNameToID         map[string]int
	planes                 map[string]cadir.ConstructionPlane
	sketchPlaneByName      map[string][3][3]float64 // name -> {origin, xDir, yDir}
	usedFeatureName        map[string]int
	model                  *cadir.Model // active model during CompileModelWithOptions (snapshot lookahead)
	curStep                int          // index of the timeline step currently being compiled
	disableBodyRenames     bool
	disableTimelineRenames bool
}

// NewCompiler creates a new Compiler.
func NewCompiler(client *Client) *Compiler {
	return &Compiler{
		client:            client,
		bodyMap:           make(map[string]string),
		sketchNameToID:    make(map[string]int),
		planes:            make(map[string]cadir.ConstructionPlane),
		sketchPlaneByName: make(map[string][3][3]float64),
		usedFeatureName:   make(map[string]int),
	}
}

func (c *Compiler) renameBody(oldName, newName string) error {
	if oldName == "" || newName == "" || oldName == newName || c.disableBodyRenames {
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
	if execResp == "" || desiredName == "" || c.disableTimelineRenames {
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
		if step.Index < startStep && step.Type == "sketch" && step.Sketch != nil {
			if o, xd, yd, ok := cadir.PlaneBasis(step.Sketch.Plane); ok {
				c.sketchPlaneByName[step.Sketch.Name] = [3][3]float64{o, xd, yd}
			}
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

// reconcileBodies aligns SolidCraft body names with Fusion's post-step state:
// bodies are matched geometrically (volume + bbox center) and mismatched names
// are renamed with collision-safe ordering so IR body-name references written
// at this timeline point resolve correctly afterwards.
func (c *Compiler) reconcileBodies(expected []cadir.BodySnapshot) error {
	if len(expected) == 0 {
		return nil
	}
	actual, err := c.inspectBodies()
	if err != nil {
		return err
	}
	if len(actual) == 0 {
		return nil
	}
	matches := cadir.MatchBodies(actual, expected)
	matchedExpected := make(map[int]bool, len(matches))
	desired := make(map[string]string, len(matches))
	for ai, ei := range matches {
		matchedExpected[ei] = true
		if actual[ai].Name != expected[ei].Name {
			desired[actual[ai].Name] = expected[ei].Name
		}
	}
	for ei, ex := range expected {
		if !matchedExpected[ei] {
			fmt.Printf("Reconcile notice: Fusion body %q (%.1f mm3) has no SolidCraft counterpart\n", ex.Name, ex.VolumeMM3)
		}
	}
	if len(desired) == 0 {
		return nil
	}
	names := make([]string, 0, len(actual))
	for _, b := range actual {
		names = append(names, b.Name)
	}
	for _, mv := range cadir.PlanRenames(names, desired) {
		if err := c.renameBody(mv[0], mv[1]); err != nil {
			fmt.Printf("Notice: reconcile rename %s -> %s failed: %v\n", mv[0], mv[1], err)
			continue
		}
		fmt.Printf("Reconcile: renamed body %s -> %s to match Fusion state\n", mv[0], mv[1])
	}
	return nil
}

// inspectBodies reads the current SolidCraft bodies as geometric snapshots.
func (c *Compiler) inspectBodies() ([]cadir.BodySnapshot, error) {
	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var design struct {
		Bodies []struct {
			Name         string    `json:"name"`
			VolumeMM3    float64   `json:"volume_mm3"`
			AreaMM2      float64   `json:"area_mm2"`
			CenterOfMass []float64 `json:"center_of_mass"`
			BBox         struct {
				Min []float64 `json:"min"`
				Max []float64 `json:"max"`
			} `json:"bbox"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal([]byte(inspOut), &design); err != nil {
		return nil, err
	}
	out := make([]cadir.BodySnapshot, 0, len(design.Bodies))
	for _, b := range design.Bodies {
		snap := cadir.BodySnapshot{Name: b.Name, VolumeMM3: b.VolumeMM3, AreaMM2: b.AreaMM2}
		if len(b.CenterOfMass) >= 3 {
			snap.Centroid = cadir.Point3D{X: b.CenterOfMass[0], Y: b.CenterOfMass[1], Z: b.CenterOfMass[2]}
		}
		if len(b.BBox.Min) >= 3 && len(b.BBox.Max) >= 3 {
			snap.BBoxMin = cadir.Point3D{X: b.BBox.Min[0], Y: b.BBox.Min[1], Z: b.BBox.Min[2]}
			snap.BBoxMax = cadir.Point3D{X: b.BBox.Max[0], Y: b.BBox.Max[1], Z: b.BBox.Max[2]}
		}
		out = append(out, snap)
	}
	return out, nil
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
	c.disableBodyRenames = opts.DisableBodyRenames
	c.disableTimelineRenames = opts.DisableTimelineRenames
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

	skipOccurred := false
	c.model = model
	for _, step := range model.Timeline {
		c.curStep = step.Index
		if opts.StartStep > 0 && step.Index < opts.StartStep {
			continue
		}
		if opts.UntilStep >= 0 && step.Index > opts.UntilStep {
			fmt.Printf("\nReached target step %d (--until-step %d), pausing playback.\n", step.Index-1, opts.UntilStep)
			break
		}

		if opts.SkipSteps[step.Index] {
			fmt.Printf("Skipping step %d (%s) as requested (--skip-steps); body reconciliation disabled for the rest of this run.\n", step.Index, step.Name)
			skipOccurred = true
			continue
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
			if err := c.compileAndExecuteExtrude(*step.Extrude, step.BodySnapshot); err != nil {
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

		if !skipOccurred && len(step.BodySnapshot) > 0 {
			if err := c.reconcileBodies(step.BodySnapshot); err != nil {
				fmt.Printf("Notice: body reconciliation at step %d failed: %v\n", step.Index, err)
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

	// Universal pre-conditioning:
	// 1. Separate isolated tangent circles from boundary lines so SolidCraft
	//    avoids non-manifold self-touching pinch points in SMLib.
	curves := cadir.SeparateIsolatedTangentCircles(s.Curves)
	// 2. Split arcs at circle intersections so SolidCraft region detection sees the
	//    same trimmed curve arrangement Fusion used.
	curves = cadir.SplitArcsAtCircles(curves)

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

	// 2. Draw circles
	for _, curve := range curves {
		if curve.Type == cadir.CurveTypeCircle && curve.Radius > 0 {
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

	// 3. Draw lines
	for _, curve := range curves {
		if curve.Type == cadir.CurveTypeLine {
			cmds = append(cmds, Command{
				ID: "sketch.line",
				Params: SketchLineParams{
					Points: [][]float64{
						{curve.Start.X, curve.Start.Y},
						{curve.End.X, curve.End.Y},
					},
					Construction: curve.Construction,
				},
			})
		}
	}

	// 4. Draw arcs (CCW-normalized)
	for _, curve := range curves {
		if curve.Type == cadir.CurveTypeArc {
			start, end := cadir.NormalizeArc(curve.Center, curve.Start, curve.End, curve.MidPoint)
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

	// 5. Draw splines
	hasSplines := false
	for _, curve := range curves {
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

	// 6. sketch.finish
	cmds = append(cmds, Command{ID: "sketch.finish"})

	_, err := c.client.Batch(cmds)
	if err != nil {
		return err
	}
	if o, xd, yd, ok := cadir.PlaneBasis(s.Plane); ok {
		c.sketchPlaneByName[s.Name] = [3][3]float64{o, xd, yd}
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

func (c *Compiler) mergeParticipantBodies(e cadir.ExtrudeFeature, snap []cadir.BodySnapshot, desiredBodyName string) {
	if e.Operation != "join" || len(e.ParticipantBodies) <= 1 {
		return
	}
	// Fusion's own post-step snapshot says which participants it actually
	// consumed (merged away): exactly those absent from the snapshot. A merge
	// is skipped when it is temporary: the participant reappears (geometrically)
	// in a later snapshot, meaning a later feature separates it again, and
	// keeping it separate lets those features target it directly.
	if snap != nil {
		present := make(map[string]bool, len(snap))
		for _, b := range snap {
			present[b.Name] = true
		}
		var consumed []string
		for _, pb := range e.ParticipantBodies {
			if present[pb] {
				continue
			}
			if c.mergeIsTemporary(pb) {
				fmt.Printf("Merge skip: %s is consumed now but separates again later; keeping it a separate body\n", pb)
				continue
			}
			consumed = append(consumed, pb)
		}
		if len(consumed) == 0 {
			return
		}
		e.ParticipantBodies = consumed
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

// mergeIsTemporary reports whether Fusion merges this participant only
// temporarily: a later step splits a body into two with conserved volume and
// one piece geometrically matching the participant's pre-merge body. Such
// merges are skipped so the participant stays a separate body that the later
// feature can target directly.
func (c *Compiler) mergeIsTemporary(pb string) bool {
	if c.model == nil {
		return false
	}
	var pre *cadir.BodySnapshot
	for i := c.curStep - 1; i >= 0 && pre == nil; i-- {
		for _, b := range c.model.Timeline[i].BodySnapshot {
			if b.Name == pb {
				bb := b
				pre = &bb
				break
			}
		}
	}
	if pre == nil {
		return false
	}
	preVol := pre.VolumeMM3
	preCenter := [3]float64{
		(pre.BBoxMin.X + pre.BBoxMax.X) / 2,
		(pre.BBoxMin.Y + pre.BBoxMax.Y) / 2,
		(pre.BBoxMin.Z + pre.BBoxMax.Z) / 2,
	}
	matchesParticipant := func(b cadir.BodySnapshot) bool {
		rel := math.Abs(b.VolumeMM3-preVol) / math.Max(math.Abs(preVol), 1e-9)
		center := [3]float64{
			(b.BBoxMin.X + b.BBoxMax.X) / 2,
			(b.BBoxMin.Y + b.BBoxMax.Y) / 2,
			(b.BBoxMin.Z + b.BBoxMax.Z) / 2,
		}
		d := math.Sqrt((center[0]-preCenter[0])*(center[0]-preCenter[0]) +
			(center[1]-preCenter[1])*(center[1]-preCenter[1]) +
			(center[2]-preCenter[2])*(center[2]-preCenter[2]))
		return rel <= 0.05 && d <= 1.0
	}
	snapVolume := func(b cadir.BodySnapshot) float64 { return b.VolumeMM3 }
	snapCenter := func(b cadir.BodySnapshot) [3]float64 {
		return [3]float64{
			(b.BBoxMin.X + b.BBoxMax.X) / 2,
			(b.BBoxMin.Y + b.BBoxMax.Y) / 2,
			(b.BBoxMin.Z + b.BBoxMax.Z) / 2,
		}
	}
	for s := c.curStep + 1; s < len(c.model.Timeline); s++ {
		cur := c.model.Timeline[s].BodySnapshot
		if len(cur) == 0 || s == 0 {
			continue
		}
		prev := c.model.Timeline[s-1].BodySnapshot
		for _, p := range cur {
			if !matchesParticipant(p) {
				continue
			}
			// Split signature: some previous-step body M has a volume equal to
			// P plus another current-step body T (within 5%), i.e. M was split
			// into P and T.
			for _, t := range cur {
				if t.Name == p.Name {
					continue
				}
				sum := snapVolume(p) + snapVolume(t)
				for _, m := range prev {
					relSum := math.Abs(snapVolume(m)-sum) / math.Max(snapVolume(m), 1e-9)
					if relSum > 0.05 {
						continue
					}
					mc, tc := snapCenter(m), snapCenter(t)
					d := math.Sqrt((mc[0]-tc[0])*(mc[0]-tc[0]) + (mc[1]-tc[1])*(mc[1]-tc[1]) + (mc[2]-tc[2])*(mc[2]-tc[2]))
					if d <= 2.0 {
						return true
					}
				}
			}
		}
	}
	return false
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

func (c *Compiler) compileAndExecuteExtrude(e cadir.ExtrudeFeature, snap []cadir.BodySnapshot) error {
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
			for fi, fp := range e.Profiles {
				bestIdx := -1
				bestArea := 0.0
				bestDist := 1e9
				for _, sp := range scProfiles {
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
				// Tolerance scales with profile size: small profiles (e.g. keyswitch
				// sockets on a fine grid) allow a few mm of centroid discrepancy,
				// large ones stay near the 2 mm floor, capped for safety.
				tol := math.Min(2.0+math.Sqrt(fp.AreaMM2), 6.0)
				if bestDist < tol && bestIdx >= 0 {
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
							// Claim test: a sub-profile belongs to the nearest Fusion
							// profile only; this prevents regions of adjacent compound
							// profiles (e.g. mirrored pairs in one sketch) from being
							// absorbed by the wrong one.
							if !isNearestFusionProfile(sp.CentroidSketch, fi, e.Profiles) {
								continue
							}
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
		// A join must not target a participant that Fusion consumes now but
		// separates again later (a temporary merge): joining it would fuse it
		// into the target, and the later split that restores Fusion's state
		// cannot separate fused bodies in SolidCraft. Keep such a body
		// untouched; the later split restores it via reconciliation.
		var temporary []string
		if e.Operation == "join" && snap != nil {
			present := make(map[string]bool, len(snap))
			for _, b := range snap {
				present[b.Name] = true
			}
			for _, rb := range sourceList {
				if !present[rb] && c.mergeIsTemporary(rb) {
					temporary = append(temporary, rb)
				}
			}
		}
		for _, rb := range sourceList {
			skip := false
			for _, t := range temporary {
				if rb == t {
					skip = true
					break
				}
			}
			if skip {
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
		if len(targetBodies) == 0 && len(temporary) > 0 {
			// Every listed participant was temporary; fall back to targeting
			// them (old behavior) rather than extruding with no target.
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
	}

	// Generic fallback: when IR-listed body names do not resolve to scene
	// bodies, aim at bodies whose bbox intersects the swept volume.
	if len(targetBodies) == 0 && (e.Operation == "join" || e.Operation == "cut") {
		targetBodies = c.sweptVolumeTargets(e)
		if len(targetBodies) > 0 {
			fmt.Printf("Extrude %s: name-based targets unresolved, using swept-volume fallback %v\n", e.Name, targetBodies)
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
			c.mergeParticipantBodies(e, snap, desiredBodyName)
			return nil
		}

		// Try with explicit targets if omitted targets failed
		if len(targetBodies) > 0 {
			faceParams.Targets = targetBodies
			resp, fErr = c.client.Execute("solid.extrude", faceParams)
			if fErr == nil {
				c.renameAutoProjectionSketch(resp, e.Name)
				c.checkAndRenameBodies(resp, desiredBodyName)
				c.mergeParticipantBodies(e, snap, desiredBodyName)
				return nil
			}
		}
	}

	if len(targetIndices) == 0 {
		if len(e.ParticipantBodies) > 1 {
			c.mergeParticipantBodies(e, snap, desiredBodyName)
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

	if (e.Operation == "join" || e.Operation == "cut") && len(targetIndices) > 0 && len(e.ResultBodies) > 1 {
		if c.compileMultiBodyExtrude(e, targetIndices, sketchRef) {
			return nil
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
		c.mergeParticipantBodies(e, snap, desiredBodyName)
		return nil
	}

	// Raised-tool retry for cuts: when a cutting tool's start face is coplanar
	// with a curved target surface, SolidCraft's boolean needs an unsupported
	// touching-faces fallback. Raise the tool's start plane above the surface
	// and extend the depth by the same amount — the tool volume outside the
	// target contributes nothing, so the cut result is geometrically identical.
	if e.Operation == "cut" {
		for _, raise := range [...]float64{1, 5} {
			raised := extParams
			raised.StartOffset = extParams.StartOffset + raise
			raised.Distance = extParams.Distance - raise
			rResp, rErr := c.client.Execute("solid.extrude", raised)
			if rErr == nil {
				c.renameFeature(rResp, e.Name)
				c.checkAndRenameBodies(rResp, desiredBodyName)
				c.mergeParticipantBodies(e, snap, desiredBodyName)
				return nil
			}
			fmt.Printf("Notice: raised-tool cut %s (+%gmm) failed: %v\n", e.Name, raise, rErr)
		}
	}

	// Fallback for joins when the direct join extrude was rejected: extrude a
	// new body, then combine it into the targets. Current SolidCraft versions
	// support extrude-with-join directly, so this path only runs for kernels
	// that cannot.
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
		newResp, newErr := c.client.Execute("solid.extrude", newParams)
		if newErr == nil {
			c.renameFeature(newResp, e.Name)
			var res struct {
				CreatedBodies []string `json:"created_bodies"`
			}
			if json.Unmarshal([]byte(newResp), &res) == nil && len(res.CreatedBodies) > 0 {
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
				c.mergeParticipantBodies(e, snap, desiredBodyName)
				return nil
			}
		}
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
			c.mergeParticipantBodies(e, snap, desiredBodyName)
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

// sweptVolumeTargets returns scene bodies whose bbox intersects the bbox of
// the extrusion's swept volume, computed from the sketch plane basis, profile
// bounds, start offset, and extrude distance. Used only as a fallback when
// name-based target resolution finds nothing.
func (c *Compiler) sweptVolumeTargets(e cadir.ExtrudeFeature) []string {
	basis, ok := c.sketchPlaneByName[e.SketchName]
	if !ok {
		return nil
	}
	origin, xDir, yDir := basis[0], basis[1], basis[2]
	normal := [3]float64{
		xDir[1]*yDir[2] - xDir[2]*yDir[1],
		xDir[2]*yDir[0] - xDir[0]*yDir[2],
		xDir[0]*yDir[1] - xDir[1]*yDir[0],
	}
	minSw := [3]float64{1e9, 1e9, 1e9}
	maxSw := [3]float64{-1e9, -1e9, -1e9}
	expand := func(corner2d [2]float64, z float64) {
		w := [3]float64{
			origin[0] + xDir[0]*corner2d[0] + yDir[0]*corner2d[1],
			origin[1] + xDir[1]*corner2d[0] + yDir[1]*corner2d[1],
			origin[2] + xDir[2]*corner2d[0] + yDir[2]*corner2d[1],
		}
		for i := 0; i < 3; i++ {
			minSw[i] = math.Min(minSw[i], w[i])
			maxSw[i] = math.Max(maxSw[i], w[i])
		}
	}
	for _, fp := range e.Profiles {
		minX, maxX := fp.Centroid.X, fp.Centroid.X
		minY, maxY := fp.Centroid.Y, fp.Centroid.Y
		if fp.BBoxMin != nil && fp.BBoxMax != nil {
			minX, minY = fp.BBoxMin.X, fp.BBoxMin.Y
			maxX, maxY = fp.BBoxMax.X, fp.BBoxMax.Y
		} else {
			half := math.Sqrt(math.Max(fp.AreaMM2, 1e-9)) / 2
			minX, minY = fp.Centroid.X-half, fp.Centroid.Y-half
			maxX, maxY = fp.Centroid.X+half, fp.Centroid.Y+half
		}
		for _, corner := range [][2]float64{{minX, minY}, {maxX, minY}, {minX, maxY}, {maxX, maxY}} {
			expand(corner, 0)
		}
	}
	if len(e.Profiles) == 0 {
		for _, pc := range e.ProfileCentroids {
			half := 1.0
			for _, corner := range [][2]float64{{pc.X - half, pc.Y - half}, {pc.X + half, pc.Y + half}} {
				expand(corner, 0)
			}
		}
	}
	// Extrude along the normal from the start plane to start+distance (either sign).
	z0 := e.StartOffsetMM
	z1 := e.StartOffsetMM + e.DistanceMM
	for _, z := range []float64{z0, z1} {
		wz := origin[2] + normal[2]*z
		minSw[2] = math.Min(minSw[2], math.Min(origin[2], wz))
		maxSw[2] = math.Max(maxSw[2], math.Max(origin[2], wz))
	}
	// Expand laterally by the normal's in-plane contribution (tilted planes).
	for i := 0; i < 2; i++ {
		tol := 1e-6
		minSw[i] -= math.Abs(normal[i]*math.Min(z0, z1)) + tol
		maxSw[i] += math.Abs(normal[i]*math.Max(z0, z1)) + tol
	}

	inspOut, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return nil
	}
	var design struct {
		Bodies []struct {
			Name string `json:"name"`
			BBox struct {
				Min []float64 `json:"min"`
				Max []float64 `json:"max"`
			} `json:"bbox"`
		} `json:"bodies"`
	}
	if json.Unmarshal([]byte(inspOut), &design) != nil {
		return nil
	}
	var hits []string
	for _, b := range design.Bodies {
		if len(b.BBox.Min) < 3 || len(b.BBox.Max) < 3 {
			continue
		}
		overlap := true
		for i := 0; i < 3; i++ {
			if b.BBox.Max[i] < minSw[i] || b.BBox.Min[i] > maxSw[i] {
				overlap = false
				break
			}
		}
		if overlap {
			hits = append(hits, b.Name)
		}
	}
	sort.Strings(hits)
	return hits
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
	if len(targetBodies) == 0 {
		targetBodies = []string{designData.Bodies[len(designData.Bodies)-1].Name}
	}

	planeArg := interface{}(mf.Plane)
	if pl, ok := c.planes[mf.Plane]; ok {
		planeArg = map[string]interface{}{
			"origin": []float64{pl.Origin.X, pl.Origin.Y, pl.Origin.Z},
			"normal": []float64{pl.Normal.X, pl.Normal.Y, pl.Normal.Z},
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

// isNearestFusionProfile reports whether the Fusion profile at index fi is the
// closest Fusion profile (by sketch-space centroid) to the given point.
func isNearestFusionProfile(pt []float64, fi int, profiles []cadir.ExtrudeProfileInfo) bool {
	if len(pt) < 2 || fi < 0 || fi >= len(profiles) {
		return false
	}
	best := -1
	bestDist := math.MaxFloat64
	for i, p := range profiles {
		d := math.Hypot(p.Centroid.X-pt[0], p.Centroid.Y-pt[1])
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	return best == fi
}

// compileMultiBodyExtrude performs a join/cut extrude whose result spans several
// disjoint bodies. Profiles and participant bodies are assigned to whichever result
// body they overlap (by bounding box) rather than by name or coordinate thresholds.
func (c *Compiler) compileMultiBodyExtrude(e cadir.ExtrudeFeature, profs []int, sketchRef interface{}) bool {
	insp, err := c.client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		return false
	}
	var d struct {
		Bodies []struct {
			Name string `json:"name"`
			BBox struct {
				Min []float64 `json:"min"`
				Max []float64 `json:"max"`
			} `json:"bbox"`
		} `json:"bodies"`
		Sketches []struct {
			ID       int    `json:"id"`
			Name     string `json:"name"`
			Profiles []struct {
				Index          int       `json:"index"`
				CentroidSketch []float64 `json:"centroid_sketch"`
			} `json:"profiles"`
		} `json:"sketches"`
	}
	if json.Unmarshal([]byte(insp), &d) != nil {
		return false
	}
	type box struct{ min, max []float64 }
	boxes := map[string]box{}
	for _, b := range d.Bodies {
		if len(b.BBox.Min) >= 3 && len(b.BBox.Max) >= 3 {
			boxes[b.Name] = box{b.BBox.Min, b.BBox.Max}
		}
	}
	var targets []string
	for _, rb := range e.ResultBodies {
		n := rb
		if m, ok := c.bodyMap[rb]; ok {
			n = m
		}
		if _, ok := boxes[n]; ok {
			targets = append(targets, n)
		}
	}
	if len(targets) < 2 {
		return false
	}
	dist2D := func(b box, x, y float64) float64 {
		dx := math.Max(math.Max(b.min[0]-x, 0), x-b.max[0])
		dy := math.Max(math.Max(b.min[1]-y, 0), y-b.max[1])
		return math.Hypot(dx, dy)
	}
	centroids := map[int][2]float64{}
	for _, sk := range d.Sketches {
		match := false
		if id, ok := c.sketchNameToID[e.SketchName]; ok {
			match = sk.ID == id
		} else {
			match = sk.Name == e.SketchName
		}
		if match {
			for _, p := range sk.Profiles {
				if len(p.CentroidSketch) >= 2 {
					centroids[p.Index] = [2]float64{p.CentroidSketch[0], p.CentroidSketch[1]}
				}
			}
		}
	}
	groups := map[string][]int{}
	for _, idx := range profs {
		ct, ok := centroids[idx]
		if !ok {
			return false
		}
		best, bd := "", math.MaxFloat64
		for _, t := range targets {
			if dd := dist2D(boxes[t], ct[0], ct[1]); dd < bd {
				best, bd = t, dd
			}
		}
		groups[best] = append(groups[best], idx)
	}
	center := func(b box) (float64, float64, float64) {
		return (b.min[0] + b.max[0]) / 2, (b.min[1] + b.max[1]) / 2, (b.min[2] + b.max[2]) / 2
	}
	side := map[string][]string{}
	for _, pb := range e.ParticipantBodies {
		n := pb
		if m, ok := c.bodyMap[pb]; ok {
			n = m
		}
		pbx, ok := boxes[n]
		if !ok {
			continue
		}
		isTarget := false
		for _, t := range targets {
			if t == n {
				isTarget = true
			}
		}
		if isTarget {
			continue
		}
		px, py, pz := center(pbx)
		best, bd := "", math.MaxFloat64
		for _, t := range targets {
			x, y, z := center(boxes[t])
			if dd := math.Sqrt((px-x)*(px-x) + (py-y)*(py-y) + (pz-z)*(pz-z)); dd < bd {
				best, bd = t, dd
			}
		}
		side[best] = append(side[best], n)
	}
	first := true
	for _, t := range targets {
		ps := groups[t]
		if len(ps) == 0 {
			continue
		}
		name := e.Name
		if !first {
			name = fmt.Sprintf("Synthetic_%s_%s", e.Name, t)
		}
		first = false
		params := SolidExtrudeParams{
			Name:      name,
			Distance:  e.DistanceMM,
			Operation: string(e.Operation),
			Profiles:  ps,
			Targets:   append([]string{t}, side[t]...),
			Sketch:    sketchRef,
		}
		if e.StartOffsetMM != 0 {
			params.StartOffset = e.StartOffsetMM
		}
		resp, err := c.client.Execute("solid.extrude", params)
		if err != nil {
			fmt.Printf("Notice: extrude %s (%s) could not apply: %v\n", name, t, err)
			// Recovery ladder for a failed join/cut extrude against this
			// target. SolidCraft's boolean rejects tools that only touch
			// curved target faces; a raising of the tool's start plane makes
			// it strictly intersect (geometry preserved: tool volume outside
			// the target is inert, and raised cut depth compensates exactly).
			allTargets := append([]string{t}, side[t]...)
			done := false
			if e.Operation == "cut" {
				for _, raise := range [...]float64{1, 5} {
					raised := params
					raised.StartOffset = params.StartOffset + raise
					raised.Distance = params.Distance - raise
					rResp, rErr := c.client.Execute("solid.extrude", raised)
					if rErr == nil {
						c.renameFeature(rResp, name)
						c.checkAndRenameBodies(rResp, t)
						done = true
						break
					}
					fmt.Printf("Notice: raised cut %s (%s, +%gmm) failed: %v\n", name, t, raise, rErr)
				}
			}
			if done {
				continue
			}
			for i, bt := range allTargets {
				fbName := fmt.Sprintf("Synthetic_Join_%s_%d", e.Name, i+1)
				raises := []float64{0}
				if e.Operation == "cut" {
					raises = []float64{1, 5}
				}
				var combineErr error
				for _, raise := range raises {
					newParams := SolidExtrudeParams{
						Name:        fbName,
						Distance:    e.DistanceMM - raise,
						StartOffset: e.StartOffsetMM + raise,
						Operation:   "new",
						Profiles:    ps,
						Sketch:      sketchRef,
					}
					newResp, newErr := c.client.Execute("solid.extrude", newParams)
					if newErr == nil {
						c.renameFeature(newResp, fbName)
						var res struct {
							CreatedBodies []string `json:"created_bodies"`
						}
						if json.Unmarshal([]byte(newResp), &res) == nil && len(res.CreatedBodies) > 0 {
							cResp, cErr := c.client.Execute("solid.combine", SolidCombineParams{
								Target:    bt,
								Tools:     []string{res.CreatedBodies[0]},
								Operation: string(e.Operation),
								KeepTools: false,
							})
							if cErr == nil {
								c.renameFeature(cResp, fbName+"_merge")
								c.checkAndRenameBodies(cResp, bt)
								combineErr = nil
								break
							}
							combineErr = cErr
						}
					} else {
						combineErr = newErr
					}
				}
				if combineErr != nil {
					fmt.Printf("Notice: synthetic combine %s into %s failed: %v\n", fbName, bt, combineErr)
				}
			}
			continue
		}
		c.renameFeature(resp, name)
		c.checkAndRenameBodies(resp, t)
	}
	return true
}
