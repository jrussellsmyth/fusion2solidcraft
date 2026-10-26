package cadir

// Point2D represents a 2D coordinate in sketch space (in millimeters).
type Point2D struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Point3D represents a 3D coordinate in model space (in millimeters).
type Point3D struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// CurveType specifies the geometric type of a sketch curve.
type CurveType string

const (
	CurveTypeLine   CurveType = "line"
	CurveTypeCircle CurveType = "circle"
	CurveTypeArc    CurveType = "arc"
	CurveTypeSpline CurveType = "spline"
)

// Curve represents a 2D sketch curve.
type Curve struct {
	ID           string    `json:"id"`
	Type         CurveType `json:"type"`
	Start        Point2D   `json:"start,omitempty"`
	End          Point2D   `json:"end,omitempty"`
	Center       Point2D   `json:"center,omitempty"`
	Radius       float64   `json:"radius,omitempty"`
	MidPoint     Point2D   `json:"mid_point,omitempty"`
	Construction bool      `json:"construction,omitempty"`
}

// Profile represents a closed 2D planar region within a sketch.
type Profile struct {
	Index    int       `json:"index"`
	AreaMM2  float64   `json:"area_mm2"`
	Centroid Point2D   `json:"centroid"`
	Curves   []string  `json:"curves,omitempty"`
	Holes    [][]string `json:"holes,omitempty"`
}

// Sketch represents a planar 2D sketch feature.
type Sketch struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Plane    string    `json:"plane"` // "XY", "XZ", "YZ", or named/face reference
	Curves   []Curve   `json:"curves"`
	Profiles []Profile `json:"profiles"`
}

// BooleanOp defines solid combination operations.
type BooleanOp string

const (
	OpNew       BooleanOp = "new"
	OpJoin      BooleanOp = "join"
	OpCut       BooleanOp = "cut"
	OpIntersect BooleanOp = "intersect"
)

// ExtrudeFeature represents a 3D linear extrusion.
type ExtrudeFeature struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	DistanceMM       float64   `json:"distance_mm"`
	Operation        BooleanOp `json:"operation"`
	ProfileCentroids []Point2D `json:"profile_centroids"`
	TaperAngleDeg    float64   `json:"taper_angle_deg,omitempty"`
}

// FilletFeature represents edge rounding.
type FilletFeature struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	RadiusMM    float64   `json:"radius_mm"`
	TargetEdges []Point3D `json:"target_edges"`
}

// ChamferFeature represents edge beveling.
type ChamferFeature struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DistanceMM  float64   `json:"distance_mm"`
	TargetEdges []Point3D `json:"target_edges"`
}

// AppearanceFeature represents color/material assignment.
type AppearanceFeature struct {
	ColorHex     string    `json:"color_hex"`
	TargetBodies []string  `json:"target_bodies,omitempty"`
	TargetFaces  []Point3D `json:"target_faces,omitempty"`
}

// VerificationStats holds intermediate physical geometry properties.
type VerificationStats struct {
	VolumeMM3 float64   `json:"volume_mm3"`
	AreaMM2   float64   `json:"area_mm2"`
	BBoxMin   Point3D   `json:"bbox_min"`
	BBoxMax   Point3D   `json:"bbox_max"`
	Centroid  Point3D   `json:"centroid"`
}

// TimelineStep represents a single step in the chronological modeling history.
type TimelineStep struct {
	Index       int                `json:"index"`
	Name        string             `json:"name"`
	Type        string             `json:"type"` // "sketch", "extrude", "fillet", "chamfer", "appearance"
	Sketch      *Sketch            `json:"sketch,omitempty"`
	Extrude     *ExtrudeFeature    `json:"extrude,omitempty"`
	Fillet      *FilletFeature     `json:"fillet,omitempty"`
	Chamfer     *ChamferFeature    `json:"chamfer,omitempty"`
	Appearance  *AppearanceFeature `json:"appearance,omitempty"`
	TargetStats *VerificationStats `json:"target_stats,omitempty"`
}

// Model represents a complete parametric CAD document.
type Model struct {
	Name     string         `json:"name"`
	Timeline []TimelineStep `json:"timeline"`
}
