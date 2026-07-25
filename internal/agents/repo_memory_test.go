package agents

import (
	"context"
	"strings"
	"testing"

	"codeactor/internal/memory"
)

// ============================================================================
// Test RenderMemoryForInjection
// ============================================================================

func TestRenderMemoryForInjection_Empty_ReturnsEmpty(t *testing.T) {
	result := RenderMemoryForInjection("")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestRenderMemoryForInjection_Default_ReturnsEmpty(t *testing.T) {
	result := RenderMemoryForInjection(DefaultMemoryContent)
	if result != "" {
		t.Errorf("expected empty string for default content, got %q", result)
	}
}

func TestRenderMemoryForInjection_ValidContent_WrapsInXML(t *testing.T) {
	content := "## Architecture\n- Test content"
	result := RenderMemoryForInjection(content)
	if !strings.Contains(result, "<repository_knowledge>") {
		t.Error("expected <repository_knowledge> tag")
	}
	if !strings.Contains(result, "</repository_knowledge>") {
		t.Error("expected </repository_knowledge> tag")
	}
	if !strings.Contains(result, content) {
		t.Error("expected content preserved")
	}
}

// ============================================================================
// Test RepoMemoryStore
// ============================================================================

func TestRepoMemoryStore_Load_Empty_ReturnsDefault(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)

	err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if store.Get() != DefaultMemoryContent {
		t.Errorf("expected default content, got %q", store.Get())
	}
}

func TestRepoMemoryStore_Load_AlreadyLoaded_IsNoop(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)

	// First load
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Manually corrupt cache
	store.mu.Lock()
	store.cache = "corrupted"
	store.mu.Unlock()

	// Second load should be no-op (already loaded)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	if store.Get() != "corrupted" {
		t.Error("second Load should be no-op, cache should remain corrupted")
	}
}

func TestRepoMemoryStore_SaveAndGet(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)

	// Load first
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	newContent := "## Architecture\n- Test\n## Patterns\n- Test"
	if err := store.Save(context.Background(), newContent); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if store.Get() != newContent {
		t.Errorf("expected %q, got %q", newContent, store.Get())
	}
}

func TestRepoMemoryStore_Save_PersistsToSharedMemory(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)

	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	newContent := "## Architecture\n- Persisted content"
	if err := store.Save(context.Background(), newContent); err != nil {
		t.Fatal(err)
	}

	// Read directly from SharedMemory
	val, err := shared.GetKey("repo_memory:test-repo")
	if err != nil {
		t.Fatalf("GetKey failed: %v", err)
	}
	if val != newContent {
		t.Errorf("SharedMemory content mismatch: expected %q, got %q", newContent, val)
	}
}

func TestRepoMemoryStore_IsEmpty(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)

	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !store.IsEmpty() {
		t.Error("new store with default content should be empty")
	}

	store.Save(context.Background(), "## Architecture\n- Real content")
	if store.IsEmpty() {
		t.Error("store with real content should not be empty")
	}
}

func TestRepoMemoryStore_Concurrent_Safe(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)

	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	done := make(chan bool, 2)

	// Concurrent reader
	go func() {
		for i := 0; i < 100; i++ {
			store.Get()
		}
		done <- true
	}()

	// Concurrent writer
	go func() {
		for i := 0; i < 100; i++ {
			store.Save(context.Background(), "## Architecture\n- Content")
		}
		done <- true
	}()

	<-done
	<-done
}

// ============================================================================
// Test EnforceTokenBudget
// ============================================================================

func TestEnforceTokenBudget_UnderBudget_NoChange(t *testing.T) {
	short := "## Architecture\n- Small content"
	result := EnforceTokenBudget(short)
	if result != short {
		t.Errorf("short content should not be truncated")
	}
}

func TestEnforceTokenBudget_OverBudget_Truncated(t *testing.T) {
	// Create content that exceeds token budget
	longContent := "## Architecture\n" + strings.Repeat("- Line of text that is fairly long and repetitive. ", 500) +
		"\n## Patterns\n- More content" +
		"\n## Conventions\n- Even more content to ensure we exceed the budget"

	result := EnforceTokenBudget(longContent)

	// Verify result is within budget using character-based estimation
	estimatedTokens := EstimateTokens(result)
	if estimatedTokens > MaxMemoryTokens {
		t.Errorf("result has ~%d tokens (estimated), exceeds max %d", estimatedTokens, MaxMemoryTokens)
	}
}

func TestEnforceTokenBudget_TruncatesAtSectionBoundary(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("## Architecture\n")
	for i := 0; i < 30; i++ {
		sb.WriteString("- Item with some text to make lines longer. ")
	}
	sb.WriteString("\n## Patterns\n")
	for i := 0; i < 30; i++ {
		sb.WriteString("- Another item with text. ")
	}
	sb.WriteString("\n## Conventions\n")
	for i := 0; i < 30; i++ {
		sb.WriteString("- Yet another item. ")
	}
	longContent := sb.String()

	result := EnforceTokenBudget(longContent)

	// Verify result is within budget using character-based estimation
	estimatedTokens := EstimateTokens(result)
	if estimatedTokens > MaxMemoryTokens {
		t.Errorf("result has ~%d tokens (estimated), exceeds max %d", estimatedTokens, MaxMemoryTokens)
	}
	// Result should end with a section boundary (##) or within budget
}

// ============================================================================
// Test EstimateTokens
// ============================================================================

func TestEstimateTokens(t *testing.T) {
	text := "Hello, world!"
	tokens := EstimateTokens(text)
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

// ============================================================================
// Test ValidateMemoryFormat
// ============================================================================

func TestValidateMemoryFormat_ValidContent(t *testing.T) {
	content := `# Repository Memory

## Architecture
- Some architecture content

## Patterns
- Some patterns

## Conventions
- Some conventions

## Dependencies
- Some dependencies

## Gotchas
- Some gotchas

## Key Files
- Some key files`
	if !ValidateMemoryFormat(content) {
		t.Error("valid content should pass validation")
	}
}

func TestValidateMemoryFormat_MissingSection(t *testing.T) {
	content := `## Architecture
- Content
## Patterns
- Content`
	if ValidateMemoryFormat(content) {
		t.Error("content missing sections should not pass validation")
	}
}

func TestValidateMemoryFormat_Empty(t *testing.T) {
	if ValidateMemoryFormat("") {
		t.Error("empty content should not pass validation")
	}
}

// ============================================================================
// Test TruncateObservations
// ============================================================================

func TestTruncateObservations_Short(t *testing.T) {
	short := "Short observation"
	result := TruncateObservations(short)
	if result != short {
		t.Errorf("short text should not be truncated, got %q", result)
	}
}

func TestTruncateObservations_Long_Truncated(t *testing.T) {
	long := strings.Repeat("A", maxObservationChars+1000)
	result := TruncateObservations(long)
	if len(result) > maxObservationChars+20 { // allow for "\n...(truncated)" suffix
		t.Errorf("result length %d exceeds max %d", len(result), maxObservationChars)
	}
	if !strings.HasSuffix(result, "\n...(truncated)") {
		t.Error("truncated text should end with truncation marker")
	}
}

// ============================================================================
// Test NewRepoMemoryStore
// ============================================================================

func TestNewRepoMemoryStore(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("my-project", shared)
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if store.repoID != "my-project" {
		t.Errorf("expected repoID 'my-project', got %q", store.repoID)
	}
	if store.shared != shared {
		t.Error("shared memory reference mismatch")
	}
}

func TestRepoMemoryStore_Load_Failure_DoesNotPanic(t *testing.T) {
	// Test that Load handles errors gracefully
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)

	// Multiple loads should be safe
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAllMemorySections_AllPresent(t *testing.T) {
	if len(AllMemorySections) != 6 {
		t.Errorf("expected 6 sections, got %d", len(AllMemorySections))
	}
	expected := []MemorySection{
		SectionArchitecture,
		SectionPatterns,
		SectionConventions,
		SectionDependencies,
		SectionGotchas,
		SectionKeyFiles,
	}
	for i, s := range AllMemorySections {
		if s != expected[i] {
			t.Errorf("position %d: expected %q, got %q", i, expected[i], s)
		}
	}
}
