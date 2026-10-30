package cadir

import (
	"reflect"
	"testing"
)

func snapBody(name string, vol float64, cx, cy, cz float64) BodySnapshot {
	return BodySnapshot{
		Name:      name,
		VolumeMM3: vol,
		BBoxMin:   Point3D{X: cx - 10, Y: cy - 10, Z: cz - 10},
		BBoxMax:   Point3D{X: cx + 10, Y: cy + 10, Z: cz + 10},
	}
}

func TestMatchBodies_PairsByVolumeAndCenter(t *testing.T) {
	actual := []BodySnapshot{
		snapBody("Body1", 1000, 0, 0, 0),
		snapBody("Body2", 500, 100, 0, 0),
	}
	expected := []BodySnapshot{
		snapBody("Lid", 500, 100, 0, 0),
		snapBody("Main", 1000, 0, 0, 0),
	}
	m := MatchBodies(actual, expected)
	if m[0] != 1 || m[1] != 0 {
		t.Fatalf("expected 0->1, 1->0; got %v", m)
	}
}

func TestMatchBodies_RejectsVolumeMismatch(t *testing.T) {
	actual := []BodySnapshot{snapBody("Body1", 2000, 0, 0, 0)}
	expected := []BodySnapshot{snapBody("Main", 1000, 0, 0, 0)}
	if m := MatchBodies(actual, expected); len(m) != 0 {
		t.Fatalf("expected no match for 2x volume difference, got %v", m)
	}
}

func TestMatchBodies_NoDoubleAssignment(t *testing.T) {
	actual := []BodySnapshot{snapBody("A", 1000, 0, 0, 0)}
	expected := []BodySnapshot{
		snapBody("X", 1000, 0, 0, 0),
		snapBody("Y", 1000, 0, 0.5, 0),
	}
	m := MatchBodies(actual, expected)
	if len(m) != 1 {
		t.Fatalf("one actual body must match at most one expected, got %v", m)
	}
}

func TestPlanRenames_NoCollision(t *testing.T) {
	plan := PlanRenames([]string{"A", "B"}, map[string]string{"A": "C"})
	if !reflect.DeepEqual(plan, [][2]string{{"A", "C"}}) {
		t.Fatalf("unexpected plan %v", plan)
	}
}

func TestPlanRenames_ChainOrdersAwayFirst(t *testing.T) {
	// C is a brand-new name (not an existing body), so B→C frees B for A.
	plan := PlanRenames([]string{"A", "B"}, map[string]string{"A": "B", "B": "C"})
	want := [][2]string{{"B", "C"}, {"A", "B"}}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("expected %v, got %v", want, plan)
	}
}

func TestPlanRenames_DropsUnresolvableTarget(t *testing.T) {
	// C exists and is not being renamed away, so no body can take its name.
	plan := PlanRenames([]string{"A", "C"}, map[string]string{"A": "C"})
	if len(plan) != 0 {
		t.Fatalf("unresolvable rename must be dropped, got %v", plan)
	}
}

func TestPlanRenames_DropsDeadEndChain(t *testing.T) {
	// C exists and is never renamed away, so B->C and transitively A->B are
	// unresolvable and must be dropped (empty plan), deterministically.
	plan := PlanRenames([]string{"A", "B", "C"}, map[string]string{"A": "B", "B": "C"})
	if len(plan) != 0 {
		t.Fatalf("dead-end chain must be dropped, got %v", plan)
	}
}

func TestPlanRenames_SwapUsesTempName(t *testing.T) {
	plan := PlanRenames([]string{"A", "B"}, map[string]string{"A": "B", "B": "A"})
	if len(plan) != 3 {
		t.Fatalf("expected 3 moves for a swap (one via temp), got %v", plan)
	}
	// Validate by simulation: applying the plan never assigns a live name,
	// and the two bodies end up swapped.
	lives := map[string]bool{"A": true, "B": true}
	currentName := map[string]string{"A": "A", "B": "B"} // identity -> current name
	for _, mv := range plan {
		if lives[mv[1]] {
			t.Fatalf("rename %v assigns a still-live name", mv)
		}
		owner := ""
		for id, nm := range currentName {
			if nm == mv[0] {
				owner = id
			}
		}
		if owner == "" {
			t.Fatalf("rename %v names an unknown body", mv)
		}
		currentName[owner] = mv[1]
		lives[mv[0]] = false
		lives[mv[1]] = true
	}
	if currentName["A"] != "B" || currentName["B"] != "A" {
		t.Fatalf("swap not completed: %v", currentName)
	}
}
