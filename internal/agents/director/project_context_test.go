// project_context_test.go — ComputeProjectID 的行为固化（以实现为准）。
//
// 这里原本还有三条 ProjectContextLoader 用例（首次加载 / 缓存命中 / 空目录）：那个加载器负责把
// 工作区根的 YAGENT.md/CLAUDE.md/AGENTS.md 注进 Director 的 system prompt，那条注入已整体删除，
// 用例随之删除。留下来的 projectID 仍是 rollout / artifact / 蓝图共用的主键，取值规则必须继续被
// 钉住 —— 改坏它会让同一个仓库在三个地方落进三个不同目录。

package director

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

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
