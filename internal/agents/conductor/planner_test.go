package conductor

import (
	"context"
	"testing"
)

func TestNewPlanner(t *testing.T) {
	p := NewPlanner(0)
	if p.maxDepth != 5 {
		t.Errorf("default maxDepth = %d, want 5", p.maxDepth)
	}

	p2 := NewPlanner(10)
	if p2.maxDepth != 10 {
		t.Errorf("maxDepth = %d, want 10", p2.maxDepth)
	}
}

func TestPlannerDecompose(t *testing.T) {
	p := NewPlanner(5)
	plan, err := p.Decompose(context.Background(), "Implement a login function")
	if err != nil {
		t.Fatalf("Decompose failed: %v", err)
	}

	if plan.Task != "Implement a login function" {
		t.Errorf("Task = %q, want 'Implement a login function'", plan.Task)
	}

	if len(plan.Steps) != 1 {
		t.Errorf("Steps count = %d, want 1", len(plan.Steps))
	}

	if len(plan.DependencyOrder) != 1 {
		t.Errorf("DependencyOrder count = %d, want 1", len(plan.DependencyOrder))
	}

	step := plan.Steps[0]
	if step.ID != "step_1" {
		t.Errorf("Step.ID = %q, want 'step_1'", step.ID)
	}
	if !step.Retryable {
		t.Error("Step should be retryable")
	}
}

func TestPlannerDecompose_EmptyTask(t *testing.T) {
	p := NewPlanner(5)
	_, err := p.Decompose(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty task")
	}
}

func TestPlannerSchedule(t *testing.T) {
	p := NewPlanner(5)
	plan := &Plan{
		Task: "test",
		Steps: []Step{
			{ID: "step_a", Task: "A", DependsOn: []string{}},
			{ID: "step_b", Task: "B", DependsOn: []string{"step_a"}},
			{ID: "step_c", Task: "C", DependsOn: []string{"step_b"}},
		},
	}

	ordered := p.Schedule(plan)
	if len(ordered) != 3 {
		t.Fatalf("ordered steps = %d, want 3", len(ordered))
	}

	// Should be topological order: A, B, C
	if ordered[0].ID != "step_a" {
		t.Errorf("ordered[0] = %q, want 'step_a'", ordered[0].ID)
	}
	if ordered[1].ID != "step_b" {
		t.Errorf("ordered[1] = %q, want 'step_b'", ordered[1].ID)
	}
	if ordered[2].ID != "step_c" {
		t.Errorf("ordered[2] = %q, want 'step_c'", ordered[2].ID)
	}
}

func TestPlannerSchedule_ComplexDeps(t *testing.T) {
	p := NewPlanner(5)
	plan := &Plan{
		Task: "complex",
		Steps: []Step{
			{ID: "a", Task: "A", DependsOn: []string{}},
			{ID: "b", Task: "B", DependsOn: []string{"a"}},
			{ID: "c", Task: "C", DependsOn: []string{"a"}},
			{ID: "d", Task: "D", DependsOn: []string{"b", "c"}},
		},
	}

	ordered := p.Schedule(plan)
	if len(ordered) != 4 {
		t.Fatalf("ordered steps = %d, want 4", len(ordered))
	}

	// A must come first
	if ordered[0].ID != "a" {
		t.Errorf("ordered[0] = %q, want 'a'", ordered[0].ID)
	}
	// D must come last
	if ordered[3].ID != "d" {
		t.Errorf("ordered[3] = %q, want 'd'", ordered[3].ID)
	}
}

func TestPlannerReplan(t *testing.T) {
	p := NewPlanner(5)
	original := &Plan{
		Task: "test",
		Steps: []Step{
			{ID: "s1", Task: "Step 1", DependsOn: []string{}},
			{ID: "s2", Task: "Step 2", DependsOn: []string{"s1"}},
			{ID: "s3", Task: "Step 3", DependsOn: []string{"s2"}},
		},
	}

	failedStep := &Step{ID: "s2"}
	newPlan, err := p.Replan(context.Background(), original, failedStep)
	if err != nil {
		t.Fatalf("Replan failed: %v", err)
	}

	if len(newPlan.Steps) != 2 {
		t.Fatalf("new plan steps = %d, want 2 (s3 should remain)", len(newPlan.Steps))
	}

	if newPlan.Steps[0].ID != "s1" {
		t.Errorf("newPlan.Steps[0] = %q, want 's1'", newPlan.Steps[0].ID)
	}
	if newPlan.Steps[1].ID != "s3" {
		t.Errorf("newPlan.Steps[1] = %q, want 's3'", newPlan.Steps[1].ID)
	}

	// s3 should no longer depend on s2
	if len(newPlan.Steps[1].DependsOn) != 0 {
		t.Errorf("s3 DependsOn = %v, want empty", newPlan.Steps[1].DependsOn)
	}
}

func TestPlannerReplan_NilPlan(t *testing.T) {
	p := NewPlanner(5)
	_, err := p.Replan(context.Background(), nil, &Step{ID: "s1"})
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
}

func TestShouldParallelize(t *testing.T) {
	p := NewPlanner(5)

	// No dependencies - should parallelize
	t.Run("NoDeps", func(t *testing.T) {
		steps := []Step{
			{ID: "a", DependsOn: []string{}},
			{ID: "b", DependsOn: []string{}},
		}
		if !p.ShouldParallelize(steps) {
			t.Error("should parallelize steps without deps")
		}
	})

	// With dependencies - should not parallelize
	t.Run("WithDeps", func(t *testing.T) {
		steps := []Step{
			{ID: "a", DependsOn: []string{}},
			{ID: "b", DependsOn: []string{"a"}},
		}
		if p.ShouldParallelize(steps) {
			t.Error("should NOT parallelize steps with deps")
		}
	})

	// Single step
	t.Run("Single", func(t *testing.T) {
		steps := []Step{{ID: "a"}}
		if p.ShouldParallelize(steps) {
			t.Error("should NOT parallelize single step")
		}
	})

	// Empty
	t.Run("Empty", func(t *testing.T) {
		if p.ShouldParallelize(nil) {
			t.Error("should NOT parallelize empty steps")
		}
	})
}

func TestTopologicalSort(t *testing.T) {
	steps := []Step{
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "a", DependsOn: []string{}},
		{ID: "c", DependsOn: []string{"b"}},
	}

	order := topologicalSort(steps)
	if len(order) != 3 {
		t.Fatalf("order length = %d, want 3", len(order))
	}

	// a must come before b, b before c
	pos := make(map[string]int)
	for i, id := range order {
		pos[id] = i
	}

	if pos["a"] > pos["b"] {
		t.Error("a should come before b")
	}
	if pos["b"] > pos["c"] {
		t.Error("b should come before c")
	}
}
