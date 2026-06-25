package conductor

import (
	"errors"
	"testing"
)

func TestNewConductorAgent(t *testing.T) {
	ca := NewConductorAgent()
	if ca == nil {
		t.Fatal("NewConductorAgent returned nil")
	}
	if ca.Name() != "Conductor" {
		t.Errorf("Name() = %q, want 'Conductor'", ca.Name())
	}
}

func TestNewConductorAgent_WithOptions(t *testing.T) {
	ca := NewConductorAgent(
		WithMaxSteps(50),
		WithMetaRetryCount(5),
		WithMetricsEnabled(false),
		WithPlannerMaxDepth(10),
	)

	cfg := ca.GetConfig()
	if cfg.MaxSteps != 50 {
		t.Errorf("MaxSteps = %d, want 50", cfg.MaxSteps)
	}
	if cfg.MetaRetryCount != 5 {
		t.Errorf("MetaRetryCount = %d, want 5", cfg.MetaRetryCount)
	}
	if cfg.MetricsEnabled {
		t.Error("MetricsEnabled should be false")
	}
	if cfg.PlannerMaxDepth != 10 {
		t.Errorf("PlannerMaxDepth = %d, want 10", cfg.PlannerMaxDepth)
	}
}

func TestConductorAgent_SubComponents(t *testing.T) {
	ca := NewConductorAgent()

	if ca.Router() == nil {
		t.Error("Router() returned nil")
	}
	if ca.MemoryManager() == nil {
		t.Error("MemoryManager() returned nil")
	}
	if ca.RecoveryHandler() == nil {
		t.Error("RecoveryHandler() returned nil")
	}
	if ca.MetricsCollector() == nil {
		t.Error("MetricsCollector() returned nil")
	}
}

func TestConductorAgent_GetPrompt(t *testing.T) {
	ca := NewConductorAgent()
	prompt := ca.GetPrompt()
	if prompt == "" {
		t.Error("GetPrompt() returned empty string")
	}
}

func TestConductorAgent_RegisterAgent(t *testing.T) {
	ca := NewConductorAgent()
	agent := &mockAgent{name: "test_agent"}
	ca.RegisterAgent("test_agent", agent)

	got, err := ca.Router().GetAgent("test_agent")
	if err != nil {
		t.Fatalf("GetAgent failed: %v", err)
	}
	if got.Name() != "test_agent" {
		t.Errorf("agent name = %q, want 'test_agent'", got.Name())
	}
}

func TestConductorAgent_RegisterDefaultAgent(t *testing.T) {
	ca := NewConductorAgent()
	agent := &mockAgent{name: "default"}
	ca.RegisterDefaultAgent(agent)

	if !ca.Router().HasAgent("default") {
		t.Error("default agent should be registered")
	}
}

func TestConductorAgent_Snapshot(t *testing.T) {
	ca := NewConductorAgent()
	ca.RegisterAgent("coder", &mockAgent{name: "coder"})
	ca.RegisterAgent("reviewer", &mockAgent{name: "reviewer"})

	snap := ca.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}

	if snap["name"] != "Conductor" {
		t.Errorf("snapshot name = %v, want 'Conductor'", snap["name"])
	}

	agentNames, ok := snap["agent_names"].([]string)
	if !ok {
		t.Fatal("agent_names not found in snapshot")
	}
	if len(agentNames) != 2 {
		t.Errorf("agent count = %d, want 2", len(agentNames))
	}

	metricsSnap, ok := snap["metrics"].(MetricsSnapshot)
	if !ok {
		t.Fatalf("metrics not found in snapshot, got %T", snap["metrics"])
	}
	if metricsSnap.TaskCount != 0 {
		t.Errorf("initial TaskCount = %d, want 0", metricsSnap.TaskCount)
	}
}

func TestConductorAgent_Run(t *testing.T) {
	ca := NewConductorAgent()
	ca.RegisterAgent("coder", &mockAgent{name: "coder"})

	result, err := ca.Run(func(router *Router, memory *MemoryManager, recovery *RecoveryHandler, metrics *MetricsCollector, prompt string) (string, error) {
		return "execution successful", nil
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result != "execution successful" {
		t.Errorf("result = %q, want 'execution successful'", result)
	}

	// Metrics should have recorded the task
	snap := ca.Snapshot()
	metricsSnap := snap["metrics"].(MetricsSnapshot)
	if metricsSnap.TaskCount != 1 {
		t.Errorf("TaskCount = %d, want 1", metricsSnap.TaskCount)
	}
}

func TestConductorAgent_Run_Error(t *testing.T) {
	ca := NewConductorAgent()
	_, err := ca.Run(func(router *Router, memory *MemoryManager, recovery *RecoveryHandler, metrics *MetricsCollector, prompt string) (string, error) {
		return "", errors.New("execution failed")
	})
	if err == nil {
		t.Fatal("expected error from Run")
	}
}

func TestConductorAgent_DefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxSteps != 30 {
		t.Errorf("default MaxSteps = %d, want 30", cfg.MaxSteps)
	}
	if cfg.MetaRetryCount != 3 {
		t.Errorf("default MetaRetryCount = %d, want 3", cfg.MetaRetryCount)
	}
	if !cfg.MetricsEnabled {
		t.Error("default MetricsEnabled should be true")
	}
	if cfg.PlannerMaxDepth != 5 {
		t.Errorf("default PlannerMaxDepth = %d, want 5", cfg.PlannerMaxDepth)
	}
}

// TestError is a simple error type for testing.
type TestError string

func (e TestError) Error() string { return string(e) }
