// project_context_test.go — ProjectContextLoader 组件单测（P0-1 Phase 2a）。
//
// 独立于 DirectorAgent 可测：用 t.TempDir() 构造文件环境，projectPathFn 闭包注入。
// 断言只锁定"关键特征"（与 director_characterization_test.go 组 4 同源的锁定行为）：
//   - 按序尝试读取 YAGENT.md、CLAUDE.md、AGENTS.md，不存在的文件忽略；
//   - Content 按序拼接为 "### <file>\n```\n<content>\n```\n" 格式；
//   - 二次 Load 命中缓存（返回同一指针），即使文件系统已变化；
//   - 无任何文件时返回空结果，且空结果同样被缓存；
//   - ComputeProjectID 对特殊字符路径、空路径、root 路径、超长路径的处理。

package director

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCtxFile 在指定路径写入文件，失败即 Fatal。
func writeCtxFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestProjectContextLoaderLoadFirstCall 固化首次加载行为：
// 按序读取存在的文件（YAGENT.md 优先于 AGENTS.md），Content 带格式化标题。
func TestProjectContextLoaderLoadFirstCall(t *testing.T) {
	dir := t.TempDir()
	writeCtxFile(t, filepath.Join(dir, "YAGENT.md"), "YAGENT-CONTENT")
	writeCtxFile(t, filepath.Join(dir, "AGENTS.md"), "AGENTS-CONTENT")

	loader := NewProjectContextLoader(func() string { return dir })
	res := loader.Load()

	if res == nil {
		t.Fatal("Load() should not return nil")
	}
	if len(res.LoadedFiles) != 2 {
		t.Fatalf("len(LoadedFiles) = %d, want 2", len(res.LoadedFiles))
	}
	if res.LoadedFiles[0].FileName != "YAGENT.md" || res.LoadedFiles[0].Content != "YAGENT-CONTENT" {
		t.Errorf("LoadedFiles[0] = %+v, want YAGENT.md with its content", res.LoadedFiles[0])
	}
	if res.LoadedFiles[1].FileName != "AGENTS.md" || res.LoadedFiles[1].Content != "AGENTS-CONTENT" {
		t.Errorf("LoadedFiles[1] = %+v, want AGENTS.md with its content", res.LoadedFiles[1])
	}
	if !strings.Contains(res.Content, "### YAGENT.md") || !strings.Contains(res.Content, "YAGENT-CONTENT") {
		t.Errorf("Content should be formatted with file headings, got %q", res.Content)
	}
}

// TestProjectContextLoaderCacheHit 固化缓存行为：
// 二次 Load 返回缓存（同一指针），文件系统变化不影响已缓存结果。
func TestProjectContextLoaderCacheHit(t *testing.T) {
	dir := t.TempDir()
	writeCtxFile(t, filepath.Join(dir, "YAGENT.md"), "YAGENT-CONTENT")

	loader := NewProjectContextLoader(func() string { return dir })
	first := loader.Load()

	// 缓存命中后修改文件系统：新增文件不应出现在二次结果中
	writeCtxFile(t, filepath.Join(dir, "CLAUDE.md"), "CLAUDE-CONTENT")
	second := loader.Load()

	if first != second {
		t.Error("second Load() should return the cached result (same pointer)")
	}
	if len(second.LoadedFiles) != 1 {
		t.Errorf("cache should prevent re-reading, LoadedFiles = %d, want 1", len(second.LoadedFiles))
	}
	if !strings.Contains(second.Content, "YAGENT-CONTENT") || strings.Contains(second.Content, "CLAUDE-CONTENT") {
		t.Errorf("cached Content should only contain first-load files, got %q", second.Content)
	}
}

// TestProjectContextLoaderEmptyDir 固化空目录行为：空结果同样被缓存（同一指针）。
func TestProjectContextLoaderEmptyDir(t *testing.T) {
	loader := NewProjectContextLoader(func() string { return t.TempDir() })
	first := loader.Load()
	if len(first.LoadedFiles) != 0 {
		t.Errorf("LoadedFiles = %d, want 0 for empty dir", len(first.LoadedFiles))
	}
	if first.Content != "" {
		t.Errorf("Content = %q, want empty for empty dir", first.Content)
	}
	second := loader.Load()
	if first != second {
		t.Error("empty result should also be cached (same pointer)")
	}
}

// TestComputeProjectID 固化 projectID 计算行为（以实现为准）：
//   - 空路径 → "default"；
//   - "/" 与 "." → "root_" 前缀；
//   - 非字母数字字符替换为下划线；
//   - 后缀为 projectPath 的 sha256 前 8 个 hex 字符（确定性、路径敏感）；
//   - base 超过 100 字符时截断至 100。
func TestComputeProjectID(t *testing.T) {
	t.Run("empty path yields default", func(t *testing.T) {
		if got := ComputeProjectID(""); got != "default" {
			t.Errorf("ComputeProjectID(\"\") = %q, want \"default\"", got)
		}
	})

	t.Run("root and dot paths yield root prefix", func(t *testing.T) {
		for _, p := range []string{"/", "."} {
			got := ComputeProjectID(p)
			if !strings.HasPrefix(got, "root_") {
				t.Errorf("ComputeProjectID(%q) = %q, want prefix \"root_\"", p, got)
			}
		}
	})

	t.Run("sanitizes special characters to underscores", func(t *testing.T) {
		got := ComputeProjectID("/home/user/my project.v2")
		if !strings.HasPrefix(got, "my_project_v2_") {
			t.Errorf("ComputeProjectID = %q, want prefix \"my_project_v2_\"", got)
		}
	})

	t.Run("deterministic with sha256-based suffix", func(t *testing.T) {
		path := "/home/user/myproj"
		got1 := ComputeProjectID(path)
		got2 := ComputeProjectID(path)
		if got1 != got2 {
			t.Errorf("ComputeProjectID should be deterministic, got %q vs %q", got1, got2)
		}
		h := sha256.Sum256([]byte(path))
		wantHash := hex.EncodeToString(h[:])[:8]
		if want := "myproj_" + wantHash; got1 != want {
			t.Errorf("ComputeProjectID = %q, want %q", got1, want)
		}
	})

	t.Run("different paths yield different ids", func(t *testing.T) {
		if ComputeProjectID("/home/a") == ComputeProjectID("/home/b") {
			t.Error("different paths should yield different project IDs")
		}
	})

	t.Run("truncates long base to 100 chars", func(t *testing.T) {
		path := "/x/" + strings.Repeat("a", 150)
		got := ComputeProjectID(path)
		// 截断后：100 个字符 + "_" + 8 位哈希 = 109
		if len(got) != 109 {
			t.Errorf("len(ComputeProjectID) = %d, want 109", len(got))
		}
		if !strings.HasPrefix(got, strings.Repeat("a", 100)+"_") {
			t.Errorf("ComputeProjectID should start with 100 'a's followed by underscore, got %q", got)
		}
	})
}
