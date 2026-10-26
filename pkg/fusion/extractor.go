package fusion

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

// Extractor handles querying Fusion 360 and compiling its timeline into a cadir.Model.
type Extractor struct {
	client *Client
}

// NewExtractor creates a new Extractor using the given Fusion MCP client.
func NewExtractor(client *Client) *Extractor {
	return &Extractor{client: client}
}

// ExtractModel extracts the full timeline and geometry from Fusion 360 into CAD-IR.
func (e *Extractor) ExtractModel() (*cadir.Model, error) {
	tempFile := "/tmp/fusion_cadir_export.json"
	_ = os.Remove(tempFile)

	// Embedded python script to extract all features, sketches, profiles, and intermediate B-Rep stats directly to disk
	script := fmt.Sprintf(`import adsk.core, adsk.fusion, json, math

def run(_context: str):
    app = adsk.core.Application.get()
    doc = app.activeDocument
    design = adsk.fusion.Design.cast(app.activeProduct)
    if not design:
        with open('%s', 'w') as f:
            json.dump({"error": "No active design"}, f)
        return

    model_data = {
        "name": doc.name if doc else "Untitled",
        "timeline": []
    }

    # Timeline traversal
    for i in range(design.timeline.count):
        item = design.timeline.item(i)
        entity = item.entity
        step_data = {
            "index": i,
            "name": item.name,
            "type": "unknown"
        }

        if hasattr(entity, 'objectType'):
            obj_type = entity.objectType.split('::')[-1]
            
            if obj_type == 'Sketch':
                step_data["type"] = "sketch"
                sketch = adsk.fusion.Sketch.cast(entity)
                
                plane_name = "XY"
                try:
                    ref_plane = sketch.referencePlane
                    if hasattr(ref_plane, 'name'):
                        plane_name = ref_plane.name.replace(' Origin Plane', '').replace(' Plane', '')
                except Exception:
                    try:
                        n = sketch.primaryNormalVector
                        if abs(n.z - 1.0) < 0.01 or abs(n.z + 1.0) < 0.01:
                            plane_name = "XY"
                        elif abs(n.y - 1.0) < 0.01 or abs(n.y + 1.0) < 0.01:
                            plane_name = "XZ"
                        elif abs(n.x - 1.0) < 0.01 or abs(n.x + 1.0) < 0.01:
                            plane_name = "YZ"
                    except Exception:
                        pass
                
                curves = []
                for idx, line in enumerate(sketch.sketchCurves.sketchLines):
                    sp = line.startSketchPoint.geometry
                    ep = line.endSketchPoint.geometry
                    curves.append({
                        "id": f"l{idx+1}",
                        "type": "line",
                        "start": {"x": sp.x * 10, "y": sp.y * 10},
                        "end": {"x": ep.x * 10, "y": ep.y * 10},
                        "construction": line.isConstruction
                    })

                for idx, circle in enumerate(sketch.sketchCurves.sketchCircles):
                    cp = circle.centerSketchPoint.geometry
                    curves.append({
                        "id": f"c{idx+1}",
                        "type": "circle",
                        "center": {"x": cp.x * 10, "y": cp.y * 10},
                        "radius": circle.radius * 10,
                        "construction": circle.isConstruction
                    })

                for idx, arc in enumerate(sketch.sketchCurves.sketchArcs):
                    cp = arc.centerSketchPoint.geometry
                    sp = arc.startSketchPoint.geometry
                    ep = arc.endSketchPoint.geometry
                    ev = arc.geometry.evaluator
                    p0 = ev.getParameterExtents()[1]
                    p1 = ev.getParameterExtents()[2]
                    mid = ev.getPointAtParameter((p0 + p1) / 2)[1]
                    curves.append({
                        "id": f"a{idx+1}",
                        "type": "arc",
                        "center": {"x": cp.x * 10, "y": cp.y * 10},
                        "start": {"x": sp.x * 10, "y": sp.y * 10},
                        "end": {"x": ep.x * 10, "y": ep.y * 10},
                        "radius": arc.radius * 10,
                        "mid_point": {"x": mid.x * 10, "y": mid.y * 10},
                        "construction": arc.isConstruction
                    })

                profiles = []
                for p_idx, prof in enumerate(sketch.profiles):
                    props = prof.areaProperties()
                    c = props.centroid
                    profiles.append({
                        "index": p_idx,
                        "area_mm2": props.area * 100,
                        "centroid": {"x": c.x * 10, "y": c.y * 10}
                    })

                step_data["sketch"] = {
                    "id": f"sketch_{i+1}",
                    "name": item.name,
                    "plane": plane_name,
                    "curves": curves,
                    "profiles": profiles
                }

            elif obj_type == 'ExtrudeFeature':
                step_data["type"] = "extrude"
                ext = adsk.fusion.ExtrudeFeature.cast(entity)
                
                op_map = {0: "join", 1: "cut", 2: "intersect", 3: "new"}
                op_str = op_map.get(ext.operation, "new")
                
                dist_val = 0.0
                if ext.extentOne and hasattr(ext.extentOne, 'distance'):
                    dist_val = ext.extentOne.distance.value * 10
                    
                centroids = []
                try:
                    prof = ext.profile
                    if hasattr(prof, '__iter__'):
                        for p in prof:
                            if hasattr(p, 'areaProperties'):
                                c = p.areaProperties().centroid
                                centroids.append({"x": c.x * 10, "y": c.y * 10})
                    elif hasattr(prof, 'areaProperties'):
                        c = prof.areaProperties().centroid
                        centroids.append({"x": c.x * 10, "y": c.y * 10})
                except Exception:
                    try:
                        for f in ext.startFaces:
                            c = f.centroid
                            centroids.append({"x": c.x * 10, "y": c.y * 10})
                    except Exception:
                        pass

                step_data["extrude"] = {
                    "id": f"extrude_{i+1}",
                    "name": item.name,
                    "distance_mm": dist_val,
                    "operation": op_str,
                    "profile_centroids": centroids
                }

            elif obj_type == 'FilletFeature':
                step_data["type"] = "fillet"
                fillet = adsk.fusion.FilletFeature.cast(entity)
                rad = 0.0
                if fillet.edgeSets.count > 0:
                    es = fillet.edgeSets.item(0)
                    if hasattr(es, 'radius'):
                        rad = es.radius.value * 10
                
                edges = []
                for f_idx in range(fillet.faces.count):
                    f = fillet.faces.item(f_idx)
                    c = f.centroid
                    edges.append({"x": c.x * 10, "y": c.y * 10, "z": c.z * 10})
                    
                step_data["fillet"] = {
                    "id": f"fillet_{i+1}",
                    "name": item.name,
                    "radius_mm": rad,
                    "target_edges": edges
                }

        # Check intermediate body stats if bodies exist
        if design.rootComponent.bRepBodies.count > 0:
            b = design.rootComponent.bRepBodies.item(0)
            bb = b.boundingBox
            com = b.physicalProperties.centerOfMass
            step_data["target_stats"] = {
                "volume_mm3": b.volume * 1000,
                "area_mm2": b.area * 100,
                "bbox_min": {"x": bb.minPoint.x * 10, "y": bb.minPoint.y * 10, "z": bb.minPoint.z * 10},
                "bbox_max": {"x": bb.maxPoint.x * 10, "y": bb.maxPoint.y * 10, "z": bb.maxPoint.z * 10},
                "centroid": {"x": com.x * 10, "y": com.y * 10, "z": com.z * 10}
            }

        model_data["timeline"].append(step_data)

    with open('%s', 'w') as f:
        json.dump(model_data, f)
    print("EXTRACT_OK")
`, tempFile, tempFile)

	out, err := e.client.ExecuteScript(script, true)
	if err != nil {
		return nil, fmt.Errorf("extractor script failed: %w", err)
	}

	dataBytes, err := os.ReadFile(tempFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read exported file (%s): %w (script output: %s)", tempFile, err, out)
	}

	var model cadir.Model
	if err := json.Unmarshal(dataBytes, &model); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CAD-IR model: %w", err)
	}

	return &model, nil
}
