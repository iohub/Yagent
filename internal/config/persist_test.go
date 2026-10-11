package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// decodeUseProvider 用真正的 TOML 解码器验证写出的文件语法有效，
// 并取出目标段的 use_provider 值（读写往返验证）。
func decodeUseProvider(t *testing.T, path, section string) string {
	t.Helper()
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		t.Fatalf("written config is not valid TOML: %v", err)
	}
	cur := raw
	for _, part := range strings.Split(section, ".") {
		next, ok := cur[part].(map[string]any)
		if !ok {
			t.Fatalf("section %q not found in decoded config (missing %q)", section, part)
		}
		cur = next
	}
	v, _ := cur["use_provider"].(string)
	return v
}

// writePersistConfig 写临时配置文件并返回其路径。
func writePersistConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

// TestPersistUseProvider 表驱动覆盖文本级精确更新的核心行为：
// 每个用例同时断言 ① 全文除目标行外逐字节保留；② BurntSushi 可正常解码且
// 值往返正确；③ 目录中无临时文件残留。
func TestPersistUseProvider(t *testing.T) {
	tests := []struct {
		name     string
		initial  string
		section  string
		provider string
		want     string // 期望的完整文件内容
	}{
		{
			name: "替换已有键_保留行内注释与等号空格",
			initial: `[global.llm]
use_provider = "openai"  # 主力模型

[app]
name = "yagent"
`,
			section:  "global.llm",
			provider: "deepseek_coder",
			want: `[global.llm]
use_provider = "deepseek_coder"  # 主力模型

[app]
name = "yagent"
`,
		},
		{
			name: "替换单引号旧值_保留缩进与注释",
			initial: `[agents.llm.codewriter]
  use_provider = 'localai'   # 写作助手
model = "x"
`,
			section:  "agents.llm.codewriter",
			provider: "qn_glm51",
			want: `[agents.llm.codewriter]
  use_provider = "qn_glm51"   # 写作助手
model = "x"
`,
		},
		{
			name: "键缺失_段内最后非空行后插入",
			initial: `[tools.llm]
use_provider = "openai"

[tools.llm.deepthinking]
max_tokens = 8192
# 用户注释

[app]
name = "yagent"
`,
			section:  "tools.llm.deepthinking",
			provider: "localai",
			want: `[tools.llm]
use_provider = "openai"

[tools.llm.deepthinking]
max_tokens = 8192
# 用户注释
use_provider = "localai"

[app]
name = "yagent"
`,
		},
		{
			name: "段缺失_末尾追加",
			initial: `[global.llm]
use_provider = "openai"
`,
			section:  "tools.llm.deepthinking",
			provider: "localai",
			want: `[global.llm]
use_provider = "openai"

[tools.llm.deepthinking]
use_provider = "localai"
`,
		},
		{
			name: "精确段匹配_更新[global.llm]不影响子表",
			initial: `[global.llm]
use_provider = "openai"

[global.llm.providers.ds]
model = "deepseek-v3"
`,
			section:  "global.llm",
			provider: "qn_glm51",
			want: `[global.llm]
use_provider = "qn_glm51"

[global.llm.providers.ds]
model = "deepseek-v3"
`,
		},
		{
			name: "精确段匹配_更新子表不影响父表",
			initial: `[global.llm]
use_provider = "openai"

[global.llm.providers.ds]
model = "deepseek-v3"
`,
			section:  "global.llm.providers.ds",
			provider: "qn_glm51",
			want: `[global.llm]
use_provider = "openai"

[global.llm.providers.ds]
model = "deepseek-v3"
use_provider = "qn_glm51"
`,
		},
		{
			name: "多行字符串内的假键与假段头不干扰",
			initial: `[global.llm]
use_provider = "openai"

[agents.llm.codewriter]
description = """
use_provider = "fake"

[not_a_real_table]
"""
`,
			section:  "agents.llm.codewriter",
			provider: "qn_glm51",
			want: `[global.llm]
use_provider = "openai"

[agents.llm.codewriter]
description = """
use_provider = "fake"

[not_a_real_table]
"""
use_provider = "qn_glm51"
`,
		},
		{
			name: "数组表头终止段范围",
			initial: `[tools.llm.deepthinking]
max_tokens = 8192

[[tools.llm.deepthinking.extra]]
name = "e1"
`,
			section:  "tools.llm.deepthinking",
			provider: "localai",
			want: `[tools.llm.deepthinking]
max_tokens = 8192
use_provider = "localai"

[[tools.llm.deepthinking.extra]]
name = "e1"
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writePersistConfig(t, tt.initial)
			if err := PersistUseProvider(path, tt.section, tt.provider); err != nil {
				t.Fatalf("PersistUseProvider: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got := string(data); got != tt.want {
				t.Errorf("file content mismatch\n--- got ---\n%q\n--- want ---\n%q", got, tt.want)
			}
			// 读写往返：TOML 解码有效且目标段值正确
			if v := decodeUseProvider(t, path, tt.section); v != tt.provider {
				t.Errorf("decoded use_provider = %q, want %q", v, tt.provider)
			}
			// 无临时文件残留
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatalf("read dir: %v", err)
			}
			for _, e := range entries {
				if e.Name() != "config.toml" {
					t.Errorf("temp file leaked: %s", e.Name())
				}
			}
		})
	}
}

// TestPersistUseProvider_NoTrailingNewline 源文件结尾无换行时，追加段前先补齐
// 换行，保证新段不会被粘到上一行。
func TestPersistUseProvider_NoTrailingNewline(t *testing.T) {
	path := writePersistConfig(t, "[global.llm]\nuse_provider = \"openai\"")
	if err := PersistUseProvider(path, "tools.llm.deepthinking", "localai"); err != nil {
		t.Fatalf("PersistUseProvider: %v", err)
	}
	data, _ := os.ReadFile(path)
	want := "[global.llm]\nuse_provider = \"openai\"\n\n[tools.llm.deepthinking]\nuse_provider = \"localai\"\n"
	if string(data) != want {
		t.Errorf("content mismatch\n--- got ---\n%q\n--- want ---\n%q", string(data), want)
	}
}

// TestPersistUseProvider_PreservesFileMode 原子写入后文件权限继承原文件。
func TestPersistUseProvider_PreservesFileMode(t *testing.T) {
	path := writePersistConfig(t, "[global.llm]\nuse_provider = \"openai\"\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := PersistUseProvider(path, "global.llm", "qn_glm51"); err != nil {
		t.Fatalf("PersistUseProvider: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode after persist = %o, want 600", got)
	}
}

// TestPersistUseProvider_Errors 参数与文件错误应有明确报错。
func TestPersistUseProvider_Errors(t *testing.T) {
	if err := PersistUseProvider(filepath.Join(t.TempDir(), "c.toml"), "", "p"); err == nil {
		t.Error("empty section should fail")
	}
	if err := PersistUseProvider(filepath.Join(t.TempDir(), "c.toml"), "global.llm", ""); err == nil {
		t.Error("empty provider should fail")
	}
	if err := PersistUseProvider(filepath.Join(t.TempDir(), "c.toml"), "global.llm", "p"); err == nil {
		t.Error("missing config file should fail")
	}
}
