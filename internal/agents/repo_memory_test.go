package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/memory"
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

	// 只靠 -race 的话这个测试在本地跑等于什么都不检查：
	// 写入方最后一条 Save 与这里之间由 done channel 建立 happens-before，
	// 所以终态必须可预期，且不能出现读到半个内容的撕裂状态。
	const want = "## Architecture\n- Content"
	if got := store.Get(); got != want {
		t.Errorf("final content = %q, want %q", got, want)
	}
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
	section := func(title, item string, n int) string {
		var sb strings.Builder
		sb.WriteString("## " + title + "\n")
		for i := 0; i < n; i++ {
			sb.WriteString(item)
		}
		sb.WriteString("\n")
		return sb.String()
	}
	longContent := section("Architecture", "- Item with some text to make lines longer. ", 150) +
		section("Patterns", "- Another item with text to fill budget. ", 150) +
		section("Conventions", "- Yet another convention line worth keeping. ", 150)

	// 前置条件：fixture 必须真的超预算，否则本测试会像历史上那样静默通过
	if got := EstimateTokens(longContent); got <= MaxMemoryTokens {
		t.Fatalf("fixture must exceed MaxMemoryTokens=%d to exercise truncation, got %d", MaxMemoryTokens, got)
	}

	result := EnforceTokenBudget(longContent)

	if got := EstimateTokens(result); got > MaxMemoryTokens {
		t.Fatalf("result still over budget: %d tokens, max %d", got, MaxMemoryTokens)
	}
	if len(result) >= len(longContent) {
		t.Fatal("result was not truncated at all")
	}
	// 只能整段丢弃：结果必须是原内容的前缀，且切点必须落在下一个 "## " 之前
	if !strings.HasPrefix(longContent, result) {
		t.Fatalf("budget enforcement must drop whole tail sections, not rewrite the head")
	}
	if rest := longContent[len(result):]; !strings.HasPrefix(rest, "## ") {
		t.Errorf("cut must land on a section boundary, remainder starts with %q", firstLineOf(rest))
	}
	if strings.Count(result, "## ") >= strings.Count(longContent, "## ") {
		t.Error("no section was dropped")
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ============================================================================
// Test EstimateTokens
// ============================================================================

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // -1 = 只要求 > 0
	}{
		{"empty is zero", "", 0},
		{"ascii", "Hello, world!", -1},
		{"cjk not swallowed", "仓库记忆整理", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateTokens(tc.input)
			if tc.want == 0 {
				if got != 0 {
					t.Errorf("EstimateTokens(%q) = %d, want 0", tc.input, got)
				}
				return
			}
			if got <= 0 {
				t.Errorf("EstimateTokens(%q) = %d, want > 0", tc.input, got)
			}
		})
	}

	t.Run("longer text is never cheaper", func(t *testing.T) {
		short := EstimateTokens("one two three")
		long := EstimateTokens("one two three four five six seven eight nine ten")
		if long <= short {
			t.Errorf("long=%d must exceed short=%d", long, short)
		}
	})
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
