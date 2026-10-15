package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// 两份模板必须与 schema 同源：写了没人读的键，用户改完只会得到"我改了它但什么都没发生"。
// 这条测试同时覆盖内嵌模板（首次运行生成的就是它）与仓库里的 config/config.toml（人读的参考）。
func TestTemplatesDecodeCompletelyAndValidate(t *testing.T) {
	repoTemplate := filepath.Join("..", "..", "config", "config.toml")

	// 内嵌模板先落到临时文件，再走与真实启动相同的加载路径。
	embedded := filepath.Join(t.TempDir(), "config.toml")
	created, err := EnsureConfigExists(embedded)
	if err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}
	if !created {
		t.Fatal("模板没有生成，后面的断言全部会假通过")
	}

	cases := []string{embedded}
	if _, statErr := os.Stat(repoTemplate); statErr == nil {
		cases = append(cases, repoTemplate)
	} else {
		t.Logf("跳过仓库模板（没找到 %s）", repoTemplate)
	}

	for _, path := range cases {
		name := filepath.Base(path)
		if strings.Contains(path, "TempDir") || strings.HasSuffix(path, "config.toml") && len(cases) > 1 {
			name = name + "@" + filepath.Dir(path)
		}
		t.Run(name, func(t *testing.T) {
			md, err := toml.DecodeFile(path, &Config{})
			if err != nil {
				t.Fatalf("template does not decode: %v", err)
			}
			if undecoded := md.Undecoded(); len(undecoded) > 0 {
				var keys []string
				for _, k := range undecoded {
					keys = append(keys, strings.Join(k, "."))
				}
				t.Errorf("模板里有 schema 不认识的键（写了没人读）：%s", strings.Join(keys, ", "))
			}
			// 校验必须过：首启生成一份"启动即失败"的模板是最糟的首屏。
			if _, err := LoadFromFile(path); err != nil {
				t.Errorf("LoadFromFile(模板) 失败: %v", err)
			}
		})
	}
}

// 模板的密钥位必须是占位符：NeedsSetup 靠它把新用户领进引导页。
// 哪天真发布了一份带可用 key 的模板，这条会红 —— 那时要删的是引导判据，不是这条断言。
func TestTemplateShipsPlaceholderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := EnsureConfigExists(path); err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	provider, err := cfg.GetProvider(cfg.Global.LLM.UseProvider)
	if err != nil {
		t.Fatalf("生效 provider %q 取不到: %v", cfg.Global.LLM.UseProvider, err)
	}
	if !IsPlaceholderKey(provider.APIKey) {
		t.Errorf("模板的 api_key = %q，不是占位符；首启引导不会触发", provider.APIKey)
	}
}

// NeedsSetup 的四种判据都必须能响：少一种就等于某类新机器没人被领进引导页。
func TestNeedsSetupCoversEveryTrigger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := EnsureConfigExists(path); err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if reason, _ := NeedsSetup(cfg, path, false, nil); reason != SetupPlaceholderKey {
		t.Errorf("占位密钥的模板 = %v, want SetupPlaceholderKey", reason)
	}
	if reason, _ := NeedsSetup(cfg, path, true, nil); reason != SetupFreshInstall {
		t.Errorf("本次生成的模板 = %v, want SetupFreshInstall", reason)
	}
	if reason, detail := NeedsSetup(nil, path, false, os.ErrNotExist); reason != SetupBroken {
		t.Errorf("加载失败 = %v, want SetupBroken", reason)
	} else if detail != os.ErrNotExist.Error() {
		t.Errorf("detail = %q, want 原样带上加载阶段的错误文本（%q）", detail, os.ErrNotExist.Error())
	}

	// 填好密钥后必须不再打扰：首启判据反过来测，否则"每次都弹引导"没人发现。
	filled := strings.Replace(string(mustRead(t, path)), `api_key = "your-key"`, `api_key = "sk-real-value"`, 1)
	good := filepath.Join(t.TempDir(), "filled.toml")
	if err := os.WriteFile(good, []byte(filled), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	goodCfg, err := LoadFromFile(good)
	if err != nil {
		t.Fatalf("load filled: %v", err)
	}
	if reason, detail := NeedsSetup(goodCfg, good, false, nil); reason != SetupNotNeeded {
		t.Errorf("可用配置被判成 %v (%s)，会反复弹引导", reason, detail)
	}

	// api_key_env 指向已设置的变量 = 可用；未设置 = 加载阶段就失败并点名变量。
	t.Setenv("YAGENT_TEST_KEY_ENV", "sk-from-env")
	envCfg := filepath.Join(t.TempDir(), "env.toml")
	envBody := strings.Replace(filled, `api_key = "sk-real-value"`, `api_key_env = "YAGENT_TEST_KEY_ENV"`, 1)
	if err := os.WriteFile(envCfg, []byte(envBody), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	parsed, err := LoadFromFile(envCfg)
	if err != nil {
		t.Fatalf("api_key_env 配置加载失败: %v", err)
	}
	prov, err := parsed.GetProvider(parsed.Global.LLM.UseProvider)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if prov.APIKey != "sk-from-env" {
		t.Errorf("api_key_env 没有展开进 APIKey: %q", prov.APIKey)
	}
	if reason, _ := NeedsSetup(parsed, envCfg, false, nil); reason != SetupNotNeeded {
		t.Errorf("用环境变量供密钥时 = %v, want SetupNotNeeded", reason)
	}

	t.Setenv("YAGENT_TEST_KEY_ENV", "")
	if _, err := LoadFromFile(envCfg); err == nil {
		t.Error("api_key_env 指向未设置的变量却加载成功")
	} else if !strings.Contains(err.Error(), "YAGENT_TEST_KEY_ENV") {
		t.Errorf("err = %q, want 点名那个变量", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// 占位符表要覆盖模板与文档里真实出现过的写法 —— 漏一个，那台机器就静默走到第一次任务才炸。
func TestIsPlaceholderKeyCoversShippedPlaceholders(t *testing.T) {
	for _, s := range []string{"", "your-key", "sk-your-key", "sk-ant-your-key", "changeme", "REPLACE_ME", "dummy-key", "xxx", "XXXX"} {
		if !IsPlaceholderKey(s) {
			t.Errorf("IsPlaceholderKey(%q) = false, want true", s)
		}
	}
	// 反向同样致命：误判会把能用的人锁在引导页外面。
	for _, s := range []string{"sk-1a2b3c4d5e6f", "short-but-real", "excalidraw-key"} {
		if IsPlaceholderKey(s) {
			t.Errorf("IsPlaceholderKey(%q) = true, want false（真实密钥被误判）", s)
		}
	}
}
