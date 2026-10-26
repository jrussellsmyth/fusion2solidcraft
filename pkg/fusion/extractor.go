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

    # Flatten timeline groups
    flat_items = []
    def collect(item):
        if item.isGroup:
            g = adsk.fusion.TimelineGroup.cast(item)
            for j in range(g.count):
                collect(g.item(j))
        else:
            flat_items.append(item)

    for i in range(design.timeline.count):
        collect(design.timeline.item(i))

    for idx, item in enumerate(flat_items):
        entity = item.entity
        step_data = {
            "index": idx,
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
                for l_idx, line in enumerate(sketch.sketchCurves.sketchLines):
                    sp = line.startSketchPoint.geometry
                    ep = line.endSketchPoint.geometry
                    curves.append({
                        "id": f"l{l_idx+1}",
                        "type": "line",
                        "start": {"x": sp.x * 10, "y": sp.y * 10},
                        "end": {"x": ep.x * 10, "y": ep.y * 10},
                        "construction": line.isConstruction
                    })

                for c_idx, circle in enumerate(sketch.sketchCurves.sketchCircles):
                    cp = circle.centerSketchPoint.geometry
                    curves.append({
                        "id": f"c{c_idx+1}",
                        "type": "circle",
                        "center": {"x": cp.x * 10, "y": cp.y * 10},
                        "radius": circle.radius * 10,
                        "construction": circle.isConstruction
                    })

                for a_idx, arc in enumerate(sketch.sketchCurves.sketchArcs):
                    cp = arc.centerSketchPoint.geometry
                    sp = arc.startSketchPoint.geometry
                    ep = arc.endSketchPoint.geometry
                    ev = arc.geometry.evaluator
                    p0 = ev.getParameterExtents()[1]
                    p1 = ev.getParameterExtents()[2]
                    mid = ev.getPointAtParameter((p0 + p1) / 2)[1]
                    curves.append({
                        "id": f"a{a_idx+1}",
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
                    "id": f"sketch_{idx+1}",
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
                if ext.extentOne and hasattr(ext.extentOne, 'distance') and ext.extentOne.distance:
                    dist_val = ext.extentOne.distance.value * 10
                if abs(dist_val) < 0.001 and ext.faces and ext.faces.count > 0:
                    try:
                        zs = [ext.faces.item(fi).centroid.z * 10 for fi in range(ext.faces.count)]
                        if zs:
                            h = max(zs) - min(zs)
                            if h > 0.01:
                                dist_val = h
                    except Exception:
                        pass
                    
                result_bodies = []
                try:
                    for b in ext.bodies:
                        result_bodies.append(b.name)
                except Exception:
                    pass

                sketch_name = ""
                centroids = []
                try:
                    prof = ext.profile
                    if hasattr(prof, 'parentSketch') and prof.parentSketch:
                        sketch_name = prof.parentSketch.name
                    elif hasattr(prof, 'count') and prof.count > 0:
                        p0 = prof.item(0)
                        if hasattr(p0, 'parentSketch') and p0.parentSketch:
                            sketch_name = p0.parentSketch.name

                    if hasattr(prof, '__iter__'):
                        for p in prof:
                            if hasattr(p, 'areaProperties'):
                                c = p.areaProperties().centroid
                                centroids.append({"x": c.x * 10, "y": c.y * 10})
                    elif hasattr(prof, 'areaProperties'):
                        c = prof.areaProperties().centroid
                        centroids.append({"x": c.x * 10, "y": c.y * 10})
                    elif hasattr(prof, 'count'):
                        for i in range(prof.count):
                            p = prof.item(i)
                            if hasattr(p, 'areaProperties'):
                                c = p.areaProperties().centroid
                                centroids.append({"x": c.x * 10, "y": c.y * 10})
                except Exception:
                    pass

                face_target = None
                if not centroids:
                    try:
                        for f in ext.startFaces:
                            c = f.centroid
                            face_target = {"x": c.x * 10, "y": c.y * 10, "z": c.z * 10}
                            break
                    except Exception:
                        pass
                if not centroids and not face_target:
                    try:
                        for f in ext.faces:
                            if hasattr(f.geometry, 'normal') and abs(f.geometry.normal.z) > 0.9:
                                c = f.centroid
                                face_target = {"x": c.x * 10, "y": c.y * 10, "z": c.z * 10}
                                break
                    except Exception:
                        pass

                step_data["extrude"] = {
                    "id": f"extrude_{idx+1}",
                    "name": item.name,
                    "distance_mm": dist_val,
                    "operation": op_str,
                    "profile_centroids": centroids,
                    "sketch_name": sketch_name,
                    "result_bodies": result_bodies,
                    "face_target": face_target
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
                    "id": f"fillet_{idx+1}",
                    "name": item.name,
                    "radius_mm": rad,
                    "target_edges": edges
                }

            elif obj_type == 'CombineFeature':
                step_data["type"] = "combine"
                comb = adsk.fusion.CombineFeature.cast(entity)
                op_map = {0: "join", 1: "cut", 2: "intersect"}
                
                target_name = ""
                tool_names = []
                res_bodies = []
                try:
                    if comb.targetBody:
                        target_name = comb.targetBody.name
                    if hasattr(comb.toolBodies, 'count'):
                        for ti in range(comb.toolBodies.count):
                            tool_names.append(comb.toolBodies.item(ti).name)
                    else:
                        tool_names = [t.name for t in comb.toolBodies]
                except Exception:
                    pass
                try:
                    for b in comb.bodies:
                        res_bodies.append(b.name)
                except Exception:
                    pass

                step_data["combine"] = {
                    "id": f"combine_{idx+1}",
                    "name": item.name,
                    "target_body": target_name,
                    "tool_bodies": tool_names,
                    "operation": op_map.get(comb.operation, "join"),
                    "keep_tools": comb.isKeepToolBodies,
                    "result_bodies": res_bodies
                }

            elif obj_type == 'MoveFeature':
                step_data["type"] = "move"
                mf = adsk.fusion.MoveFeature.cast(entity)
                t = mf.transform.translation
                body_names = []
                try:
                    body_names = [b.name for b in mf.bodies]
                except Exception:
                    pass

                step_data["move"] = {
                    "id": f"move_{idx+1}",
                    "name": item.name,
                    "bodies": body_names,
                    "translation": {"x": t.x * 10, "y": t.y * 10, "z": t.z * 10},
                    "copy": False,
                    "result_bodies": body_names
                }

            elif obj_type == 'CopyPasteBody':
                step_data["type"] = "move"
                cp = adsk.fusion.CopyPasteBody.cast(entity)
                src_bodies = []
                res_bodies = []
                try:
                    src_bodies = [b.name for b in cp.sourceBody]
                except Exception:
                    pass
                try:
                    for b in cp.bodies:
                        res_bodies.append(b.name)
                except Exception:
                    pass

                step_data["move"] = {
                    "id": f"copy_{idx+1}",
                    "name": item.name,
                    "bodies": src_bodies,
                    "translation": {"x": 0.0, "y": 0.0, "z": 0.0},
                    "copy": True,
                    "result_bodies": res_bodies
                }

                step_data["move"] = {
                    "id": f"copy_{idx+1}",
                    "name": item.name,
                    "bodies": src_bodies,
                    "translation": {"x": 0.0, "y": 0.0, "z": 0.0},
                    "copy": True
                }

            elif obj_type == 'MirrorFeature':
                step_data["type"] = "mirror"
                mf = adsk.fusion.MirrorFeature.cast(entity)
                pname = "XY"
                try:
                    if hasattr(mf.mirrorPlane, 'name'):
                        pname = mf.mirrorPlane.name
                except Exception:
                    pass

                step_data["mirror"] = {
                    "id": f"mirror_{idx+1}",
                    "name": item.name,
                    "plane": pname,
                    "target_bodies": []
                }

            elif obj_type == 'ConstructionPlane':
                step_data["type"] = "plane"
                cp = adsk.fusion.ConstructionPlane.cast(entity)
                o = cp.geometry.origin
                n = cp.geometry.normal
                step_data["plane"] = {
                    "id": f"plane_{idx+1}",
                    "name": item.name,
                    "origin": {"x": o.x * 10, "y": o.y * 10, "z": o.z * 10},
                    "normal": {"x": n.x, "y": n.y, "z": n.z}
                }

            elif obj_type == 'ThreadFeature':
                step_data["type"] = "thread"
                tf = adsk.fusion.ThreadFeature.cast(entity)
                desig = "ISO Metric"
                try:
                    desig = f"{tf.threadInfo.threadType} {tf.threadInfo.threadSize}"
                except Exception:
                    pass
                step_data["thread"] = {
                    "id": f"thread_{idx+1}",
                    "name": item.name,
                    "designation": desig,
                    "modeled": tf.isModeled,
                    "target_face": {"x": 0, "y": 0, "z": 0}
                }

            elif obj_type == 'Occurrence':
                step_data["type"] = "occurrence"
                occ = adsk.fusion.Occurrence.cast(entity)
                step_data["occurrence"] = {
                    "id": f"occ_{idx+1}",
                    "name": item.name,
                    "component": occ.component.name if occ.component else ""
                }

            elif obj_type == 'ConstructionAxis':
                step_data["type"] = "plane"
                step_data["plane"] = {
                    "id": f"axis_{idx+1}",
                    "name": item.name,
                    "origin": {"x": 0, "y": 0, "z": 0},
                    "normal": {"x": 0, "y": 0, "z": 1}
                }

            elif obj_type == 'RectangularPatternFeature':
                step_data["type"] = "move"
                step_data["move"] = {
                    "id": f"pattern_{idx+1}",
                    "name": item.name,
                    "bodies": [],
                    "translation": {"x": 0, "y": 0, "z": 0},
                    "copy": True
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
