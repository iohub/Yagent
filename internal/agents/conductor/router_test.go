package conductor

import (
	"context"
	"errors"
	"testing"
)

// mockAgent implements AgentRunner for testing.
type mockAgent struct {
	name string
}

func (m *mockAgent) Name() string { return m.name }
func (m *mockAgent) Run(ctx context.Context, task string) (AgentRunnerResult, error) {
	return AgentRunnerResult{Text: m.name + " executed: " + task}, nil
}

// mockFailingAgent implements AgentRunner that fails.
type mockFailingAgent struct {
	name string
	err  error
}

func (m *mockFailingAgent) Name() string { return m.name }
func (m *mockFailingAgent) Run(ctx context.Context, task string) (AgentRunnerResult, error) {
	return AgentRunnerResult{}, m.err
}

func TestNewRouter(t *testing.T) {
	r := NewRouter()
	if r == nil {
		t.Fatal("NewRouter returned nil")
	}
	if r.AgentCount() != 0 {
		t.Errorf("initial agent count = %d, want 0", r.AgentCount())
	}
}

func TestRouterRegisterAndGet(t *testing.T) {
	r := NewRouter()
	agent := &mockAgent{name: "coder"}
	r.Register("coder", agent)

	got, err := r.GetAgent("coder")
	if err != nil {
		t.Fatalf("GetAgent failed: %v", err)
	}
	if got.Name() != "coder" {
		t.Errorf("agent name = %q, want 'coder'", got.Name())
	}
}

func TestRouterRegister_Overwrite(t *testing.T) {
	r := NewRouter()
	r.Register("agent", &mockAgent{name: "old"})
	r.Register("agent", &mockAgent{name: "new"})

	got, err := r.GetAgent("agent")
	if err != nil {
		t.Fatalf("GetAgent failed: %v", err)
	}
	if got.Name() != "new" {
		t.Errorf("agent name = %q, want 'new'", got.Name())
	}
}

func TestRouterGetAgent_NotFound(t *testing.T) {
	r := NewRouter()
	_, err := r.GetAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestRouterDispatch(t *testing.T) {
	r := NewRouter()
	r.Register("coder", &mockAgent{name: "coder"})

	result, err := r.Dispatch(context.Background(), "coder", "write tests")
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if result != "coder executed: write tests" {
		t.Errorf("result = %q, want 'coder executed: write tests'", result)
	}
}

func TestRouterDispatch_AgentNotFound(t *testing.T) {
	r := NewRouter()
	_, err := r.Dispatch(context.Background(), "nonexistent", "task")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestRouterDispatch_AgentError(t *testing.T) {
	r := NewRouter()
	r.Register("failing", &mockFailingAgent{
		name: "failing",
		err:  errors.New("execution failed"),
	})

	_, err := r.Dispatch(context.Background(), "failing", "task")
	if err == nil {
		t.Fatal("expected error from failing agent")
	}
}

func TestRouterListAgents(t *testing.T) {
	r := NewRouter()
	r.Register("a", &mockAgent{name: "a"})
	r.Register("b", &mockAgent{name: "b"})
	r.Register("c", &mockAgent{name: "c"})

	names := r.ListAgents()
	if len(names) != 3 {
		t.Fatalf("agent count = %d, want 3", len(names))
	}

	// All names should be present
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, n := range []string{"a", "b", "c"} {
		if !nameSet[n] {
			t.Errorf("agent %q not found in list", n)
		}
	}
}

func TestRouterRemoveAgent(t *testing.T) {
	r := NewRouter()
	r.Register("temp", &mockAgent{name: "temp"})
	if r.AgentCount() != 1 {
		t.Errorf("before remove: count = %d, want 1", r.AgentCount())
	}

	r.RemoveAgent("temp")
	if r.AgentCount() != 0 {
		t.Errorf("after remove: count = %d, want 0", r.AgentCount())
	}

	if r.HasAgent("temp") {
		t.Error("agent should not exist after removal")
	}
}

func TestRouterHasAgent(t *testing.T) {
	r := NewRouter()
	if r.HasAgent("nonexistent") {
		t.Error("HasAgent should return false for unregistered agent")
	}

	r.Register("exists", &mockAgent{name: "exists"})
	if !r.HasAgent("exists") {
		t.Error("HasAgent should return true for registered agent")
	}
}

func TestRouterRoute_SpecifiedAgent(t *testing.T) {
	r := NewRouter()
	r.Register("coder", &mockAgent{name: "coder"})

	step := Step{ID: "s1", AgentType: "coder", Task: "write code"}
	agent, err := r.Route(context.Background(), step)
	if err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if agent.Name() != "coder" {
		t.Errorf("routed to %q, want 'coder'", agent.Name())
	}
}

func TestRouterRegisterDefault(t *testing.T) {
	r := NewRouter()
	agent := &mockAgent{name: "default_agent"}
	r.RegisterDefault(agent)

	if !r.HasAgent("default_agent") {
		t.Error("default agent should be in the agent map")
	}
	if r.AgentCount() != 1 {
		t.Errorf("agent count = %d, want 1", r.AgentCount())
	}

	// Route with "auto" agent type should use default
	step := Step{ID: "s1", AgentType: "auto", Task: "task"}
	routed, err := r.Route(context.Background(), step)
	if err != nil {
		t.Fatalf("Route failed: %v", err)
	}
	if routed.Name() != "default_agent" {
		t.Errorf("routed to %q, want 'default_agent'", routed.Name())
	}
}

func TestRouterRouteToConductor(t *testing.T) {
	r := NewRouter()
	r.Register("conductor", &mockAgent{name: "conductor"})

	result, err := r.RouteToConductor(context.Background(), "self task")
	if err != nil {
		t.Fatalf("RouteToConductor failed: %v", err)
	}
	if result != "conductor executed: self task" {
		t.Errorf("result = %q, want 'conductor executed: self task'", result)
	}
}

func TestRouterRouteToConductor_NoConductor(t *testing.T) {
	r := NewRouter()
	_, err := r.RouteToConductor(context.Background(), "task")
	if err == nil {
		t.Fatal("expected error when conductor not registered")
	}
}
