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
	Name  string      `json:"name,omitempty"`
	Plane interface{} `json:"plane"` // "XY", "XZ", "YZ", or {"origin": [x,y,z], ...}
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

// SketchSplineFitPointParams parameters for sketch.spline.fit_point.
type SketchSplineFitPointParams struct {
	Points       [][]float64 `json:"points"`
	Construction bool        `json:"construction,omitempty"`
}

// SolidExtrudeParams parameters for solid.extrude.
type SolidExtrudeParams struct {
	Name      string      `json:"name,omitempty"`
	BodyName  string      `json:"body_name,omitempty"`
	Sketch    string      `json:"sketch,omitempty"`
	Face      []float64   `json:"face,omitempty"`
	Distance  float64     `json:"distance"`
	Operation string      `json:"operation,omitempty"` // "new", "join", "cut", "intersect"
	Profiles  interface{} `json:"profiles,omitempty"`  // [0, 1] or "all"
	Targets   []string    `json:"targets,omitempty"`   // explicit target bodies
}

// BodyRenameParams parameters for body.rename.
type BodyRenameParams struct {
	Body string `json:"body"`
	Name string `json:"name"`
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

// SolidCombineParams parameters for solid.combine.
type SolidCombineParams struct {
	Target    string   `json:"target"`
	Tools     []string `json:"tools"`
	Operation string   `json:"operation,omitempty"` // "join", "cut", "intersect"
	KeepTools bool     `json:"keep_tools,omitempty"`
}

// SolidMoveParams parameters for solid.move.
type SolidMoveParams struct {
	Bodies    []string  `json:"bodies"`
	Translate []float64 `json:"translate,omitempty"` // [x, y, z]
	Axis      []float64 `json:"axis,omitempty"`
	Angle     float64   `json:"angle,omitempty"`
	Copy      bool      `json:"copy,omitempty"`
}

// SolidMirrorParams parameters for solid.mirror.
type SolidMirrorParams struct {
	Bodies []string    `json:"bodies,omitempty"`
	Plane  interface{} `json:"plane"` // "XY", "XZ", "YZ", or {"origin": [x,y,z], "normal": [x,y,z]}
}

// SolidPatternRectangularParams parameters for solid.pattern.rectangular.
type SolidPatternRectangularParams struct {
	Bodies   []string  `json:"bodies"`
	Dir1     []float64 `json:"dir1"`
	Count1   int       `json:"count1"`
	Spacing1 float64   `json:"spacing1"`
	Dir2     []float64 `json:"dir2,omitempty"`
	Count2   int       `json:"count2,omitempty"`
	Spacing2 float64   `json:"spacing2,omitempty"`
}

// AppearanceAssignParams parameters for appearance.assign.
type AppearanceAssignParams struct {
	Bodies []string    `json:"bodies,omitempty"`
	Faces  [][]float64 `json:"faces,omitempty"` // [[x, y, z] points on faces]
	Color  string      `json:"color,omitempty"` // "#rrggbb"
}
