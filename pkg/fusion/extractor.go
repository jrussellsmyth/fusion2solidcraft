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
    root = design.rootComponent

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
        item = design.timeline.item(i)
        if item.isGroup:
            try:
                g = adsk.fusion.TimelineGroup.cast(item)
                if g:
                    g.isCollapsed = False
            except Exception:
                pass
        collect(item)

    for idx, item in enumerate(flat_items):
        entity = item.entity
        step_data = {
            "index": idx,
            "name": item.name,
            "type": "unknown"
        }

        if hasattr(entity, 'objectType'):
            obj_type = entity.objectType.split('::')[-1]
            if obj_type in ('CombineFeature', 'RectangularPatternFeature', 'MoveFeature'):
                try:
                    item.rollTo(True)
                except Exception:
                    pass
            else:
                try:
                    item.rollTo(False)
                except Exception:
                    pass
            
            if obj_type == 'Sketch':
                step_data["type"] = "sketch"
                sketch = adsk.fusion.Sketch.cast(entity)
                
                plane_name = "XY"
                try:
                    ref_plane = sketch.referencePlane
                    if hasattr(ref_plane, 'name'):
                        candidate = ref_plane.name.replace(' Origin Plane', '').replace(' Plane', '')
                        if candidate in ['XY', 'XZ', 'YZ']:
                            plane_name = candidate
                except Exception:
                    pass
                if plane_name not in ['XY', 'XZ', 'YZ']:
                    try:
                        z = sketch.xDirection.crossProduct(sketch.yDirection)
                        if abs(z.z - 1.0) < 0.01 or abs(z.z + 1.0) < 0.01:
                            plane_name = "XY"
                        elif abs(z.y - 1.0) < 0.01 or abs(z.y + 1.0) < 0.01:
                            plane_name = "XZ"
                        elif abs(z.x - 1.0) < 0.01 or abs(z.x + 1.0) < 0.01:
                            plane_name = "YZ"
                    except Exception:
                        plane_name = "XY"
                
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

                for s_idx, spl in enumerate(sketch.sketchCurves.sketchFittedSplines):
                    pts = []
                    for pi in range(spl.fitPoints.count):
                        p = spl.fitPoints.item(pi).geometry
                        pts.append({"x": p.x * 10, "y": p.y * 10})
                    curves.append({
                        "id": f"s{s_idx+1}",
                        "type": "spline",
                        "points": pts,
                        "construction": spl.isConstruction
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

                sketch_plane = plane_name
                if sketch.parentComponent != root:
                    for occ in root.occurrences:
                        if occ.component == sketch.parentComponent:
                            t = occ.transform.translation
                            sketch_plane = {
                                "origin": [t.x * 10, t.y * 10, t.z * 10],
                                "x_dir": [1.0, 0.0, 0.0],
                                "y_dir": [0.0, 1.0, 0.0]
                            }
                            break

                step_data["sketch"] = {
                    "id": f"sketch_{idx+1}",
                    "name": item.name,
                    "plane": sketch_plane,
                    "curves": curves,
                    "profiles": profiles
                }

            elif obj_type == 'ExtrudeFeature':
                step_data["type"] = "extrude"
                ext = adsk.fusion.ExtrudeFeature.cast(entity)
                
                op_map = {0: "join", 1: "cut", 2: "intersect", 3: "new"}
                op_str = op_map.get(ext.operation, "new")
                
                dist_val = 0.0
                start_offset = 0.0
                if hasattr(ext, 'startExtent') and ext.startExtent:
                    se = ext.startExtent
                    try:
                        if hasattr(se, 'entity') and hasattr(se.entity, 'centroid'):
                            start_offset = se.entity.centroid.z * 10
                        elif hasattr(se, 'offset') and se.offset:
                            start_offset = se.offset.value * 10
                    except Exception:
                        pass
                if ext.extentOne:
                    if hasattr(ext.extentOne, 'distance') and ext.extentOne.distance:
                        dist_val = ext.extentOne.distance.value * 10
                    elif hasattr(ext.extentOne, 'entity'):
                        try:
                            to_ent = ext.extentOne.entity
                            if hasattr(to_ent, 'geometry') and hasattr(to_ent.geometry, 'origin'):
                                dist_val = to_ent.geometry.origin.z * 10
                            elif hasattr(to_ent, 'centroid'):
                                dist_val = to_ent.centroid.z * 10
                        except Exception:
                            pass
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

                participant_bodies = []
                try:
                    item.rollTo(True)
                    if hasattr(ext, 'participantBodies') and ext.participantBodies:
                        for b in ext.participantBodies:
                            participant_bodies.append(b.name)
                    item.rollTo(False)
                except Exception:
                    try:
                        item.rollTo(False)
                    except Exception:
                        pass

                sketch_name = ""
                centroids = []
                profile_infos = []
                try:
                    prof = ext.profile
                    if hasattr(prof, 'parentSketch') and prof.parentSketch:
                        sketch_name = prof.parentSketch.name
                    elif hasattr(prof, 'count') and prof.count > 0:
                        p0 = prof.item(0)
                        if hasattr(p0, 'parentSketch') and p0.parentSketch:
                            sketch_name = p0.parentSketch.name

                    p_list = []
                    if hasattr(prof, '__iter__'):
                        p_list = list(prof)
                    elif hasattr(prof, 'count'):
                        p_list = [prof.item(i) for i in range(prof.count)]
                    elif hasattr(prof, 'areaProperties'):
                        p_list = [prof]

                    for pi, p in enumerate(p_list):
                        if hasattr(p, 'areaProperties'):
                            props = p.areaProperties()
                            c = props.centroid
                            c_dict = {"x": c.x * 10, "y": c.y * 10}
                            centroids.append(c_dict)

                            min_x, min_y = 1e9, 1e9
                            max_x, max_y = -1e9, -1e9
                            if hasattr(p, 'profileLoops'):
                                for li in range(p.profileLoops.count):
                                    loop = p.profileLoops.item(li)
                                    if loop.isOuter:
                                        for ci in range(loop.profileCurves.count):
                                            pc = loop.profileCurves.item(ci)
                                            pts = []
                                            ent = getattr(pc, 'sketchEntity', None)
                                            if ent:
                                                if hasattr(ent, 'startSketchPoint') and ent.startSketchPoint:
                                                    pts.append(ent.startSketchPoint.geometry)
                                                if hasattr(ent, 'endSketchPoint') and ent.endSketchPoint:
                                                    pts.append(ent.endSketchPoint.geometry)
                                                if hasattr(ent, 'centerSketchPoint') and ent.centerSketchPoint:
                                                    cp = ent.centerSketchPoint.geometry
                                                    pts.append(cp)
                                                    if hasattr(ent, 'radius'):
                                                        r = ent.radius
                                                        pts.append(adsk.core.Point3D.create(cp.x - r, cp.y - r, 0))
                                                        pts.append(adsk.core.Point3D.create(cp.x + r, cp.y + r, 0))
                                                if hasattr(ent, 'fitPoints'):
                                                    for fpi in range(ent.fitPoints.count):
                                                        pts.append(ent.fitPoints.item(fpi).geometry)
                                            if not pts:
                                                geom = pc.geometry
                                                if hasattr(geom, 'startPoint'):
                                                    pts.append(geom.startPoint)
                                                if hasattr(geom, 'endPoint'):
                                                    pts.append(geom.endPoint)
                                                if hasattr(geom, 'evaluator'):
                                                    ev = geom.evaluator
                                                    p0 = ev.getParameterExtents()[1]
                                                    p1 = ev.getParameterExtents()[2]
                                                    pts.append(ev.getPointAtParameter((p0+p1)/2)[1])
                                            for pt in pts:
                                                min_x = min(min_x, pt.x * 10)
                                                min_y = min(min_y, pt.y * 10)
                                                max_x = max(max_x, pt.x * 10)
                                                max_y = max(max_y, pt.y * 10)

                            p_info = {
                                "index": pi,
                                "area_mm2": props.area * 100,
                                "centroid": c_dict
                            }
                            if min_x < 1e8:
                                p_info["bbox_min"] = {"x": min_x, "y": min_y}
                                p_info["bbox_max"] = {"x": max_x, "y": max_y}
                            profile_infos.append(p_info)
                except Exception:
                    pass

                face_target = None
                if not centroids:
                    try:
                        item.rollTo(True)
                        p_obj = ext.profile
                        if p_obj:
                            if hasattr(p_obj, 'objectType') and 'BRepFace' in p_obj.objectType:
                                face = adsk.fusion.BRepFace.cast(p_obj)
                                c = face.centroid
                                face_target = {"x": c.x * 10, "y": c.y * 10, "z": c.z * 10}
                            elif hasattr(p_obj, 'count'):
                                for j in range(p_obj.count):
                                    pj = p_obj.item(j)
                                    if hasattr(pj, 'objectType') and 'BRepFace' in pj.objectType:
                                        face = adsk.fusion.BRepFace.cast(pj)
                                        c = face.centroid
                                        face_target = {"x": c.x * 10, "y": c.y * 10, "z": c.z * 10}
                                        break
                        item.rollTo(False)
                    except Exception:
                        try:
                            item.rollTo(False)
                        except Exception:
                            pass

                if not centroids and not profile_infos and hasattr(ext, 'endFaces') and ext.endFaces:
                    try:
                        for efi in range(ext.endFaces.count):
                            ef = ext.endFaces.item(efi)
                            if ef.area * 100 < 50.0:
                                c = ef.centroid
                                centroids.append({"x": c.x * 10, "y": c.y * 10})
                                profile_infos.append({
                                    "index": efi,
                                    "area_mm2": ef.area * 100,
                                    "centroid": {"x": c.x * 10, "y": c.y * 10}
                                })
                    except Exception:
                        pass

                if sketch_name == "":
                    for prev_step in reversed(model_data["timeline"]):
                        if prev_step.get("type") == "sketch":
                            sketch_name = prev_step.get("sketch", {}).get("name", "")
                            if sketch_name:
                                break

                if not centroids and not face_target:
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
                    "start_offset_mm": start_offset,
                    "operation": op_str,
                    "profile_centroids": centroids,
                    "profiles": profile_infos,
                    "sketch_name": sketch_name,
                    "result_bodies": result_bodies,
                    "participant_bodies": participant_bodies,
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
                    elif hasattr(comb.toolBodies, '__iter__'):
                        tool_names = [t.name for t in comb.toolBodies]
                except Exception:
                    pass
                try:
                    item.rollTo(False)
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
                    if mf.inputEntities and mf.inputEntities.count > 0:
                        for j in range(mf.inputEntities.count):
                            ent = mf.inputEntities.item(j)
                            if hasattr(ent, 'name'):
                                body_names.append(ent.name)
                    elif mf.bodies and mf.bodies.count > 0:
                        body_names = [b.name for b in mf.bodies]
                except Exception:
                    pass

                tform = mf.transform
                r00 = tform.getCell(0, 0)
                r01 = tform.getCell(0, 1)
                r02 = tform.getCell(0, 2)
                r10 = tform.getCell(1, 0)
                r11 = tform.getCell(1, 1)
                r12 = tform.getCell(1, 2)
                r20 = tform.getCell(2, 0)
                r21 = tform.getCell(2, 1)
                r22 = tform.getCell(2, 2)

                tr = r00 + r11 + r22
                cos_theta = max(-1.0, min(1.0, (tr - 1.0) / 2.0))
                theta = math.acos(cos_theta)
                angle_deg = math.degrees(theta)
                axis_vec = {"x": 0.0, "y": 0.0, "z": 1.0}
                if abs(theta) > 1e-4:
                    rx = r21 - r12
                    ry = r02 - r20
                    rz = r10 - r01
                    l = math.sqrt(rx*rx + ry*ry + rz*rz)
                    if l > 1e-6:
                        axis_vec = {"x": rx/l, "y": ry/l, "z": rz/l}

                try:
                    item.rollTo(False)
                except Exception:
                    pass

                step_data["move"] = {
                    "id": f"move_{idx+1}",
                    "name": item.name,
                    "bodies": body_names,
                    "translation": {"x": t.x * 10, "y": t.y * 10, "z": t.z * 10},
                    "rotation_axis": axis_vec,
                    "angle_deg": angle_deg,
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

            elif obj_type == 'MirrorFeature':
                step_data["type"] = "mirror"
                mf = adsk.fusion.MirrorFeature.cast(entity)
                pname = "XY"
                try:
                    if hasattr(mf.mirrorPlane, 'name'):
                        pname = mf.mirrorPlane.name
                except Exception:
                    pass

                target_bodies = []
                try:
                    item.rollTo(True)
                    if hasattr(mf, 'inputEntities') and mf.inputEntities:
                        for j in range(mf.inputEntities.count):
                            ent = mf.inputEntities.item(j)
                            if hasattr(ent, 'name'):
                                target_bodies.append(ent.name)
                    item.rollTo(False)
                except Exception:
                    try:
                        item.rollTo(False)
                    except Exception:
                        pass

                step_data["mirror"] = {
                    "id": f"mirror_{idx+1}",
                    "name": item.name,
                    "plane": pname,
                    "target_bodies": target_bodies
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
                step_data["type"] = "pattern"
                feat = adsk.fusion.RectangularPatternFeature.cast(entity)
                target_bodies = []
                if feat and feat.inputEntities:
                    for e_idx in range(feat.inputEntities.count):
                        ent = feat.inputEntities.item(e_idx)
                        if hasattr(ent, 'name'):
                            target_bodies.append(ent.name)

                q1 = int(round(feat.quantityOne.value)) if feat and feat.quantityOne else 1
                d1_cm = feat.distanceOne.value if feat and feat.distanceOne else 0
                d1_mm = d1_cm * 10.0

                dir1_vec = [1.0, 0.0, 0.0]
                if feat and feat.directionOneEntity and hasattr(feat.directionOneEntity, 'geometry'):
                    geom = feat.directionOneEntity.geometry
                    if hasattr(geom, 'startPoint') and hasattr(geom, 'endPoint'):
                        vx = geom.endPoint.x - geom.startPoint.x
                        vy = geom.endPoint.y - geom.startPoint.y
                        vz = geom.endPoint.z - geom.startPoint.z
                        l = (vx*vx + vy*vy + vz*vz)**0.5
                        if l > 1e-6:
                            dir1_vec = [vx/l, vy/l, vz/l]

                if feat and feat.patternDistanceType == 0 and q1 > 1:
                    step_spacing = d1_mm / (q1 - 1)
                else:
                    step_spacing = d1_mm

                if step_spacing < 0:
                    dir1_vec = [-dir1_vec[0], -dir1_vec[1], -dir1_vec[2]]
                    step_spacing = -step_spacing

                try:
                    item.rollTo(False)
                except Exception:
                    pass

                res_bodies = [b.name for b in feat.bodies] if feat and feat.bodies else []

                step_data["pattern"] = {
                    "id": f"pattern_{idx+1}",
                    "name": item.name,
                    "bodies": target_bodies,
                    "dir1": {"x": dir1_vec[0], "y": dir1_vec[1], "z": dir1_vec[2]},
                    "count1": q1,
                    "spacing1": step_spacing,
                    "result_bodies": res_bodies
                }

        try:
            total_vol = 0.0
            total_area = 0.0
            min_pt = [1e9, 1e9, 1e9]
            max_pt = [-1e9, -1e9, -1e9]
            body_count = 0
            all_b = list(root.bRepBodies)
            for occ in root.occurrences:
                try:
                    all_b.extend(list(occ.bRepBodies))
                except Exception:
                    pass
            for b in all_b:
                if b.isValid:
                    body_count += 1
                    total_vol += b.volume * 1000.0
                    total_area += b.area * 100.0
                    bb = b.boundingBox
                    min_pt[0] = min(min_pt[0], bb.minPoint.x * 10)
                    min_pt[1] = min(min_pt[1], bb.minPoint.y * 10)
                    min_pt[2] = min(min_pt[2], bb.minPoint.z * 10)
                    max_pt[0] = max(max_pt[0], bb.maxPoint.x * 10)
                    max_pt[1] = max(max_pt[1], bb.maxPoint.y * 10)
                    max_pt[2] = max(max_pt[2], bb.maxPoint.z * 10)
            if body_count > 0:
                step_data["target_stats"] = {
                    "volume_mm3": total_vol,
                    "area_mm2": total_area,
                    "bbox_min": {"x": min_pt[0], "y": min_pt[1], "z": min_pt[2]},
                    "bbox_max": {"x": max_pt[0], "y": max_pt[1], "z": max_pt[2]}
                }
        except Exception:
            pass

        model_data["timeline"].append(step_data)

    try:
        design.timeline.moveToEnd()
    except Exception:
        pass

    with open('%s', 'w') as f:
        json.dump(model_data, f)
    print("EXTRACT_OK")
`, tempFile, tempFile)

	out, err := e.client.ExecuteScript(script, false)
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
