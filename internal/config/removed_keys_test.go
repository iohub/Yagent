package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// 移除登记表必须能响。这里守三件事：
// ① 表里每条键都有一个能触发它的夹具（否则"删掉的键会被点名"只是句承诺）；
// ② 那些夹具确实出现在报告里；
// ③ 合法键永远不会被误报（拿两份模板当反例）。

// TestRemovedKeysAreNamed 逐条验证登记表与夹具互为镜像。
func TestRemovedKeysAreNamed(t *testing.T) {
	fixtures := map[string]string{
		"app.enable_streaming":                    "[app]\nenable_streaming = true\n",
		"keywords.default_path":                   "[keywords]\ndefault_path = \"x\"\n",
		"keywords.hot_reload":                     "[keywords]\nhot_reload = true\n",
		"keywords.dict":                           "[[keywords.dict]]\nname = \"custom\"\n",
		"agent.meta_max_steps":                    "[agent]\nmeta_max_steps = 5\n",
		"enhanced_commander.enable":               "[enhanced_commander]\nenable = true\n",
		"enhanced_commander.max_delegation_depth": "[enhanced_commander]\nmax_delegation_depth = 3\n",
		"git_checkpoint.auto_checkpoint":          "[git_checkpoint]\nauto_checkpoint = true\n",
		"git_checkpoint.checkpoint_interval":      "[git_checkpoint]\ncheckpoint_interval = 2\n",
		"git_checkpoint.max_checkpoints":          "[git_checkpoint]\nmax_checkpoints = 9\n",
		"tools.deferred.hot":                      "[tools.deferred]\nhot = [\"run_bash\"]\n",
		"tools.deferred.cold":                     "[tools.deferred]\ncold = [\"print_dir_tree\"]\n",
		"browser.enable_skills":                   "[browser]\nenable_skills = true\n",
		"codeseek.knowledge":                      "[codeseek.knowledge]\nenabled = false\ninjection_max_tokens = 1\n",
	}

	for path := range fixtures {
		if _, ok := removedKeyHints[path]; !ok {
			t.Errorf("夹具 %q 不在移除登记表里（要么补表，要么删夹具）", path)
		}
	}
	for path := range removedKeyHints {
		if _, ok := fixtures[path]; !ok {
			t.Errorf("移除登记表里的 %q 没有能触发它的夹具 —— 这条承诺没人验证", path)
		}
	}

	for path, body := range fixtures {
		path, body := path, body
		t.Run(strings.ReplaceAll(path, ".", "_"), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(file, []byte(twoProviders+"\n"+body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			md, err := toml.DecodeFile(file, &Config{})
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			report := strings.Join(removedKeyReports(md), "\n")
			if !strings.Contains(report, path) {
				t.Errorf("报告没点名 %q：\n%s", path, report)
			}
		})
	}
}

// provider 段里的死键按第四段（键名）匹配，因为 provider 名是用户自定义的。
func TestRemovedProviderKeysAreNamed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	body := twoProviders + `
[global.llm.providers.bedrock]
model = "m"
api_base_url = "https://x/v1"
api_key = "k"
aws_region = "us-west-2"
aws_profile = "p"
model_provider = "anthropic"
`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	md, err := toml.DecodeFile(file, &Config{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	report := strings.Join(removedKeyReports(md), "\n")
	for _, key := range []string{"aws_region", "aws_profile", "model_provider"} {
		if !strings.Contains(report, "providers.bedrock."+key) {
			t.Errorf("报告没点名 %s：\n%s", key, report)
		}
	}

	// Bedrock 的名字不再豁免必填项：缺 api_key 必须直接失败。
	broken := filepath.Join(t.TempDir(), "broken.toml")
	if err := os.WriteFile(broken, []byte(`[global.llm]
use_provider = "bedrock"

[global.llm.providers.bedrock]
model = "anthropic.claude-v2"
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadFromFile(broken); err == nil {
		t.Error("不带 api_key 的 bedrock 配置通过了校验 —— 豁免没删干净")
	}
}

// 两份模板不能出现在移除报告里：误报会让每次启动都刷一条"你有已删除的键"。
func TestTemplatesTriggerNoRemovedKeyWarnings(t *testing.T) {
	embedded := filepath.Join(t.TempDir(), "config.toml")
	if _, err := EnsureConfigExists(embedded); err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}
	for _, path := range []string{embedded, filepath.Join("..", "..", "config", "config.toml")} {
		if _, err := os.Stat(path); err != nil {
			t.Logf("跳过 %s（%v）", path, err)
			continue
		}
		md, err := toml.DecodeFile(path, &Config{})
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if lines := removedKeyReports(md); len(lines) > 0 {
			t.Errorf("%s 触发了移除告警：%s", path, strings.Join(lines, "; "))
		}
	}
}
