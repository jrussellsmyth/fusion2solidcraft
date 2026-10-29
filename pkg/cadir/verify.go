package cadir

import (
	"fmt"
	"math"
)

// ComparisonResult summarizes the verification delta between Fusion and SolidCraft.
type ComparisonResult struct {
	StepIndex     int     `json:"step_index"`
	StepName      string  `json:"step_name"`
	Passed        bool    `json:"passed"`
	VolumeDiffPct float64 `json:"volume_diff_pct"`
	AreaDiffPct   float64 `json:"area_diff_pct"`
	BBoxMaxDelta  float64 `json:"bbox_max_delta_mm"`
	Message       string  `json:"message"`
}

// CompareStats checks if SolidCraft measurements match Fusion expectations.
func CompareStats(step TimelineStep, actual VerificationStats, tolerancePct float64) ComparisonResult {
	if step.TargetStats == nil {
		return ComparisonResult{
			StepIndex: step.Index,
			StepName:  step.Name,
			Passed:    true,
			Message:   "no target stats to compare",
		}
	}
	expected := *step.TargetStats

	volDiff := 0.0
	if expected.VolumeMM3 > 0 {
		volDiff = math.Abs(actual.VolumeMM3-expected.VolumeMM3) / expected.VolumeMM3 * 100.0
	}

	areaDiff := 0.0
	if expected.AreaMM2 > 0 {
		areaDiff = math.Abs(actual.AreaMM2-expected.AreaMM2) / expected.AreaMM2 * 100.0
	}

	d1 := math.Abs(actual.BBoxMin.X - expected.BBoxMin.X)
	d2 := math.Abs(actual.BBoxMin.Y - expected.BBoxMin.Y)
	d3 := math.Abs(actual.BBoxMin.Z - expected.BBoxMin.Z)
	d4 := math.Abs(actual.BBoxMax.X - expected.BBoxMax.X)
	d5 := math.Abs(actual.BBoxMax.Y - expected.BBoxMax.Y)
	d6 := math.Abs(actual.BBoxMax.Z - expected.BBoxMax.Z)

	bboxMaxDelta := math.Max(d1, math.Max(d2, math.Max(d3, math.Max(d4, math.Max(d5, d6)))))

	passed := volDiff <= tolerancePct && bboxMaxDelta <= 0.05
	msg := fmt.Sprintf("volume: %.2f vs %.2f mm3 (%.4f%% diff), bbox delta: %.4f mm",
		actual.VolumeMM3, expected.VolumeMM3, volDiff, bboxMaxDelta)

	return ComparisonResult{
		StepIndex:     step.Index,
		StepName:      step.Name,
		Passed:        passed,
		VolumeDiffPct: volDiff,
		AreaDiffPct:   areaDiff,
		BBoxMaxDelta:  bboxMaxDelta,
		Message:       msg,
	}
}
