package solidcraft

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

// Compiler translates CAD-IR models into SolidCraft executable commands.
type Compiler struct {
	client *Client
}

// NewCompiler creates a new Compiler.
func NewCompiler(client *Client) *Compiler {
	return &Compiler{client: client}
}

// CompileModel transforms the full timeline into SolidCraft commands and replays it step-by-step.
func (c *Compiler) CompileModel(model *cadir.Model, verify bool) ([]cadir.ComparisonResult, error) {
	if err := c.client.NewDesign(); err != nil {
		return nil, fmt.Errorf("failed to reset SolidCraft design: %w", err)
	}

	var results []cadir.ComparisonResult

	for _, step := range model.Timeline {
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
		ID:     "sketch.create",
		Params: SketchCreateParams{Plane: plane},
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

func (c *Compiler) compileAndExecuteExtrude(e cadir.ExtrudeFeature) error {
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

	var targetIndices []int
	if len(designData.Sketches) > 0 {
		scProfiles := designData.Sketches[len(designData.Sketches)-1].Profiles
		for _, fCentroid := range e.ProfileCentroids {
			bestIdx := -1
			bestDist := 1e9
			for _, sp := range scProfiles {
				if len(sp.CentroidSketch) >= 2 {
					dx := sp.CentroidSketch[0] - fCentroid.X
					dy := sp.CentroidSketch[1] - fCentroid.Y
					dist := math.Hypot(dx, dy)
					if dist < bestDist {
						bestDist = dist
						bestIdx = sp.Index
					}
				}
			}
			if bestDist < 1.0 && bestIdx >= 0 {
				// avoid duplicates
				found := false
				for _, idx := range targetIndices {
					if idx == bestIdx {
						found = true
						break
					}
				}
				if !found {
					targetIndices = append(targetIndices, bestIdx)
				}
			}
		}
	}

	var profArg interface{} = "all"
	if len(targetIndices) > 0 {
		profArg = targetIndices
	}

	_, err = c.client.Execute("solid.extrude", SolidExtrudeParams{
		Distance:  e.DistanceMM,
		Operation: string(e.Operation),
		Profiles:  profArg,
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
