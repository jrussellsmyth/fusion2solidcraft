package cadir

import (
	"fmt"
	"math"
	"sort"
)

const (
	maxMatchRelVolDiff = 0.05 // relative volume difference tolerated for a match
	maxMatchCenterDist = 1.0  // bbox-center distance (mm) tolerated for a match
)

func bboxCenter(b BodySnapshot) Point3D {
	return Point3D{
		X: (b.BBoxMin.X + b.BBoxMax.X) / 2,
		Y: (b.BBoxMin.Y + b.BBoxMax.Y) / 2,
		Z: (b.BBoxMin.Z + b.BBoxMax.Z) / 2,
	}
}

// MatchBodies greedily pairs actual bodies with expected snapshots by geometry:
// relative volume difference <= 5% and bbox-center distance <= 1 mm qualify a
// pair; qualifying pairs are consumed best-score first. Returns actual index
// -> expected index. Bodies left unmatched are simply absent from the result.
func MatchBodies(actual, expected []BodySnapshot) map[int]int {
	type candidate struct {
		ai, ei int
		score  float64
	}
	var cands []candidate
	for ai, a := range actual {
		for ei, ex := range expected {
			rel := math.Abs(a.VolumeMM3-ex.VolumeMM3) / math.Max(math.Abs(ex.VolumeMM3), 1e-9)
			d := Distance3D(bboxCenter(a), bboxCenter(ex))
			if rel > maxMatchRelVolDiff || d > maxMatchCenterDist {
				continue
			}
			cands = append(cands, candidate{ai: ai, ei: ei, score: rel + d})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score < cands[j].score })
	matches := make(map[int]int)
	usedActual := make(map[int]bool, len(actual))
	usedExpected := make(map[int]bool, len(expected))
	for _, c := range cands {
		if usedActual[c.ai] || usedExpected[c.ei] {
			continue
		}
		usedActual[c.ai] = true
		usedExpected[c.ei] = true
		matches[c.ai] = c.ei
	}
	return matches
}

// PlanRenames returns the ordered set of renames (from, to) that transforms
// the current body names into the desired assignment without ever assigning a
// name that is still live. desired maps a current body name to its target
// name; identities and unknown sources are ignored. Renames whose target name
// is occupied by a body that is never renamed away are unresolvable and
// dropped (the caller keeps those bodies under their current names). Rename
// cycles are broken with temporary names ("__rename_tmp_N").
func PlanRenames(current []string, desired map[string]string) [][2]string {
	live := make(map[string]bool, len(current))
	for _, n := range current {
		live[n] = true
	}
	pending := make(map[string]string, len(desired))
	for from, to := range desired {
		if from != to && live[from] {
			pending[from] = to
		}
	}
	// Drop unresolvable renames: a rename is feasible when its target is not
	// an existing body, or the target's owner is itself renamed away by a
	// feasible rename (transitively; cycles among renames are feasible).
	feasible := func(start string) bool {
		seen := make(map[string]bool, len(pending))
		cur := start
		for steps := 0; steps <= len(pending); steps++ {
			if seen[cur] {
				return true // cycle: every member renames away; temp names order them
			}
			seen[cur] = true
			to := pending[cur]
			if !live[to] {
				return true // target already free
			}
			next, ok := pending[to]
			if !ok {
				return false // target occupied by a body that never moves
			}
			cur = next
		}
		return true
	}
	for from := range pending {
		if !feasible(from) {
			delete(pending, from)
		}
	}
	var plan [][2]string
	tmpCount := 0
	for pass := 0; len(pending) > 0 && pass <= 2*len(pending)+8; pass++ {
		progressed := false
		for from, to := range pending {
			if !live[to] {
				plan = append(plan, [2]string{from, to})
				delete(pending, from)
				live[from] = false
				live[to] = true
				progressed = true
			}
		}
		if !progressed && len(pending) > 0 {
			// Cycle: park one body under a temp name to break it.
			for from, to := range pending {
				tmp := fmt.Sprintf("__rename_tmp_%d", tmpCount)
				tmpCount++
				plan = append(plan, [2]string{from, tmp})
				live[from] = false
				live[tmp] = true
				delete(pending, from)
				pending[tmp] = to
				progressed = true
				break
			}
		}
		if !progressed {
			break // defensive: should not happen after feasibility filtering
		}
	}
	return plan
}
