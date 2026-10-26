package solidcraft

// Command represents a single atomic command sent to SolidCraft.
type Command struct {
	ID     string      `json:"id"`
	Params interface{} `json:"params,omitempty"`
}

// BatchRequest represents a batch of commands executed sequentially in SolidCraft.
type BatchRequest struct {
	Commands []Command `json:"commands"`
}

// SketchCreateParams parameters for sketch.create.
type SketchCreateParams struct {
	Plane string `json:"plane"` // "XY", "XZ", "YZ", or named plane
}

// SketchLineParams parameters for sketch.line.
type SketchLineParams struct {
	Points       [][]float64 `json:"points"` // [[x0, y0], [x1, y1]]
	Construction bool        `json:"construction,omitempty"`
}

// SketchCircleCenterParams parameters for sketch.circle.center.
type SketchCircleCenterParams struct {
	Center       []float64 `json:"center"` // [cx, cy]
	Radius       float64   `json:"radius"`
	Construction bool      `json:"construction,omitempty"`
}

// SketchArcCenterPointParams parameters for sketch.arc.center_point.
type SketchArcCenterPointParams struct {
	Center       []float64 `json:"center"` // [cx, cy]
	Start        []float64 `json:"start"`  // [sx, sy]
	End          []float64 `json:"end"`    // [ex, ey] CCW from start
	Construction bool      `json:"construction,omitempty"`
}

// SolidExtrudeParams parameters for solid.extrude.
type SolidExtrudeParams struct {
	Distance  float64     `json:"distance"`
	Operation string      `json:"operation,omitempty"` // "new", "join", "cut", "intersect"
	Profiles  interface{} `json:"profiles,omitempty"`  // [0, 1] or "all"
}

// SolidFilletParams parameters for solid.fillet.
type SolidFilletParams struct {
	Radius float64     `json:"radius"`
	Edges  [][]float64 `json:"edges"` // [[x, y, z] pointOnEdge]
}

// SolidChamferParams parameters for solid.chamfer.
type SolidChamferParams struct {
	Distance float64     `json:"distance"`
	Edges    [][]float64 `json:"edges"` // [[x, y, z] pointOnEdge]
}

// AppearanceAssignParams parameters for appearance.assign.
type AppearanceAssignParams struct {
	Bodies []string    `json:"bodies,omitempty"`
	Faces  [][]float64 `json:"faces,omitempty"` // [[x, y, z] points on faces]
	Color  string      `json:"color,omitempty"` // "#rrggbb"
}
