package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这一族测试的唯一主题：**引导页写出来的文件必须和人手写的一样**。
// 写回是文本级替换，所以每条断言都是"整份文件的逐字节形状"，而不是"解出来差不多"——
// 用户注释与行序是这份配置的自我说明方式，弄丢它们比弄丢一个值更糟。

const wizardBase = `# 我的配置
# （两行说明，别弄丢）
[global.llm]
use_provider = "main"   # 主力

[global.llm.providers.main]
model = "m-main"
api_base_url = "https://a.example/v1"
api_key = "k-main"
temperature = 0.3
max_tokens = 8192

[global.llm.providers.other]
model = "m-other"
api_base_url = "https://b.example/v1"
api_key = "k-other"

[agent]
lang = ""
`

func writeWizardConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// 编辑一个已有 provider：只允许动它自己那几行，块外的注释逐字节保留。
func TestWriteProvider_EditsOnlyTargetBlock(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)

	err := WriteProvider(path, ProviderDraft{
		Name:        "main",
		Model:       "m-main-v2",
		APIBaseURL:  "https://a.example/v1",
		APIKey:      "k-new",
		MaxTokens:   8192,
		Temperature: 0.3,
	})
	if err != nil {
		t.Fatalf("WriteProvider: %v", err)
	}

	got := string(mustFile(t, path))
	for _, want := range []string{
		"# 我的配置",
		"# （两行说明，别弄丢）",
		"use_provider = \"main\"   # 主力", // 行内注释与等号两侧空格都要留着
		"model = \"m-main-v2\"",
		"api_key = \"k-new\"",
		"[global.llm.providers.other]",
		"api_key = \"k-other\"", // 别的块一个字节都不能变
	} {
		if !strings.Contains(got, want) {
			t.Errorf("结果缺 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "k-main") {
		t.Errorf("旧密钥还留在文件里：\n%s", got)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("写出来的文件加载不了: %v", err)
	}
	if p := cfg.Global.LLM.Providers["main"]; p.Model != "m-main-v2" || p.APIKey != "k-new" || p.MaxTokens != 8192 || p.Temperature != 0.3 {
		t.Errorf("往返不一致: %+v", p)
	}
}

// 表单不问 temperature，但文件里已有的值不能被"保存"动作抹掉。
func TestWriteProvider_PreservesUntouchedTemperature(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)
	if err := WriteProvider(path, ProviderDraft{
		Name: "main", Model: "m-main", APIBaseURL: "https://a.example/v1", APIKey: "k",
	}); err != nil {
		t.Fatalf("WriteProvider: %v", err)
	}
	got := string(mustFile(t, path))
	if !strings.Contains(got, "temperature = 0.3") {
		t.Errorf("没在表单里的 temperature 被删掉了：\n%s", got)
	}
}

// 新建 provider 追加到文件末尾，并且块外内容不动。
func TestWriteProvider_AppendsNewBlock(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)
	if err := WriteProvider(path, ProviderDraft{
		Name: "extra", Model: "m-extra", APIBaseURL: "https://c.example/v1", APIKey: "sk-1",
	}); err != nil {
		t.Fatalf("WriteProvider: %v", err)
	}
	got := string(mustFile(t, path))
	if !strings.HasPrefix(got, "# 我的配置") {
		t.Errorf("文件头被改动：\n%s", got)
	}
	if !strings.Contains(got, "[global.llm.providers.extra]") {
		t.Fatalf("新块没写进去：\n%s", got)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := cfg.Global.LLM.Providers["extra"]; !ok {
		t.Error("解不出 extra")
	}
}

// api_key 与 api_key_env 互斥：切换来源时另一行必须消失，
// 否则明文密钥会以"已经不生效的那一行"的形式继续留在文件里。
func TestWriteProvider_KeySourceIsExclusive(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)
	t.Setenv("YAGENT_TEST_KEY", "sk-from-env")

	if err := WriteProvider(path, ProviderDraft{
		Name: "main", Model: "m", APIBaseURL: "https://a/v1", APIKeyEnv: "YAGENT_TEST_KEY",
	}); err != nil {
		t.Fatalf("WriteProvider(api_key_env): %v", err)
	}
	block := providerBlock(t, path, "main")
	if strings.Contains(block, "api_key = ") || !strings.Contains(block, `api_key_env = "YAGENT_TEST_KEY"`) {
		t.Errorf("切换成环境变量后该块应只剩 api_key_env：\n%s", block)
	}

	// 切回明文：api_key_env 行也必须消失。
	if err := WriteProvider(path, ProviderDraft{
		Name: "main", Model: "m", APIBaseURL: "https://a/v1", APIKey: "sk-literal",
	}); err != nil {
		t.Fatalf("WriteProvider(api_key): %v", err)
	}
	block = providerBlock(t, path, "main")
	if strings.Contains(block, "api_key_env") {
		t.Errorf("切回明文后 api_key_env 还留着：\n%s", block)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Global.LLM.Providers["main"].APIKey != "sk-literal" {
		t.Errorf("APIKey = %q, want sk-literal", cfg.Global.LLM.Providers["main"].APIKey)
	}
}

// providerBlock 截出 [global.llm.providers.<name>] 段，供逐段断言用
// （整文件断言会被别的 provider 块里的同名键骗到）。
func providerBlock(t *testing.T, path, name string) string {
	t.Helper()
	data := mustFile(t, path)
	header := "[global.llm.providers." + name + "]"
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("段 %q 不在文件里：\n%s", header, data)
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// 删除：无人引用才允许动文件。
func TestDeleteProvider_RefusesWhileReferenced(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)

	if err := DeleteProvider(path, "main"); err == nil {
		t.Fatal("main 还是 use_provider 的目标，删除必须被拒绝")
	} else if !strings.Contains(err.Error(), "global.llm.use_provider") {
		t.Errorf("err = %q, want 点名引用位置", err)
	}
	if !strings.Contains(string(mustFile(t, path)), "[global.llm.providers.main]") {
		t.Error("被拒绝的删除仍然改了文件")
	}

	if err := DeleteProvider(path, "other"); err != nil {
		t.Fatalf("删一个没人引用的 provider 失败: %v", err)
	}
	if strings.Contains(string(mustFile(t, path)), "providers.other") {
		t.Errorf("块没删干净：\n%s", mustFile(t, path))
	}
	if _, err := LoadFromFile(path); err != nil {
		t.Errorf("删完加载不了: %v", err)
	}
	// 再删一次必须是 no-op 而不是报错：调用方按"确保不存在"用它。
	if err := DeleteProvider(path, "other"); err != nil {
		t.Errorf("重复删除 = %v, want nil", err)
	}
}

// 被 fallback_providers 引用的 provider 同样不能删。
func TestProviderRefs_SeesFallbackChain(t *testing.T) {
	path := writeWizardConfig(t, wizardBase+`
[global.llm.providers.third]
model = "m3"
api_base_url = "https://c/v1"
api_key = "k3"
fallback_providers = ["other", { provider = "gone", weight = 2 }]
`)
	refs, err := ProviderReferences(path, "other")
	if err != nil {
		t.Fatalf("ProviderReferences: %v", err)
	}
	if len(refs) != 1 || refs[0] != "global.llm.providers.third.fallback_providers" {
		t.Errorf("refs = %v, want 那条 fallback 链", refs)
	}
	// 表级 use_provider（agents.llm / tools.llm）也要算引用。
	path2 := writeWizardConfig(t, wizardBase+"[agents.llm]\nuse_provider = \"other\"\n")
	refs2, err := ProviderReferences(path2, "other")
	if err != nil {
		t.Fatalf("ProviderReferences: %v", err)
	}
	if len(refs2) != 1 || refs2[0] != "agents.llm.use_provider" {
		t.Errorf("refs = %v, want agents.llm.use_provider", refs2)
	}
}

// 名字校验：能安全拼进 TOML 表头的才放行。
func TestIsValidProviderName(t *testing.T) {
	// "providers" 是合法名字：它解出来是 providers.providers 这个键，结构上没有歧义。
	for _, ok := range []string{"a", "my_provider", "deepseek_v4_pro", "X-9", "providers"} {
		if err := IsValidProviderName(ok); err != nil {
			t.Errorf("IsValidProviderName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "a.b", "a b", `a"b`, "a[0]", strings.Repeat("x", 65)} {
		if err := IsValidProviderName(bad); err == nil {
			t.Errorf("IsValidProviderName(%q) = nil, want 拒绝（点号/引号/方括号会造出畸形表名）", bad)
		}
	}
}

// 首启必答项：写进去的每一项都必须能被下次启动读回。
func TestWriteFirstRun_RoundTrips(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)
	err := WriteFirstRun(path, FirstRunDraft{
		UseProvider:    "other",
		Lang:           "Chinese",
		YoloMode:       true,
		RoutingMode:    "fixed",
		BrowserEnabled: false,
		LLMTimeout:     5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("WriteFirstRun: %v", err)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("写出来的文件加载不了: %v\n%s", err, mustFile(t, path))
	}
	if cfg.Global.LLM.UseProvider != "other" {
		t.Errorf("use_provider = %q", cfg.Global.LLM.UseProvider)
	}
	if cfg.Agent.SpeakLang != "Chinese" {
		t.Errorf("lang = %q", cfg.Agent.SpeakLang)
	}
	if !cfg.Agent.YoloMode || cfg.Agent.FullYoloMode {
		t.Errorf("yolo = (%v,%v), want (true,false)", cfg.Agent.YoloMode, cfg.Agent.FullYoloMode)
	}
	if cfg.Routing.IsAuto() {
		t.Error("routing mode 没写成 fixed")
	}
	if cfg.Browser.Enabled {
		t.Error("browser enabled 没写成 false")
	}
	if cfg.LLM.Timeout != 5*time.Minute {
		t.Errorf("llm timeout = %v, want 5m", cfg.LLM.Timeout)
	}
}

// lang 留空 = 不写这一行（模板里已经有 lang = ""，重复写只是噪声）。
func TestWriteFirstRun_SkipsEmptyLang(t *testing.T) {
	path := writeWizardConfig(t, `[global.llm]
use_provider = "main"

[global.llm.providers.main]
model = "m"
api_base_url = "https://a/v1"
api_key = "k"

[agent]
lang = ""
`)
	before := string(mustFile(t, path))
	if err := WriteFirstRun(path, FirstRunDraft{RoutingMode: "auto", BrowserEnabled: true}); err != nil {
		t.Fatalf("WriteFirstRun: %v", err)
	}
	after := string(mustFile(t, path))
	if strings.Count(after, "lang = ") != 1 {
		t.Errorf("lang 被重复写了：\n%s", after)
	}
	if !strings.Contains(before, `lang = ""`) {
		t.Fatal("夹具本身不该含 lang 之外的东西")
	}
}

// 权限：写回继承原文件权限，而模板首建是 0600，所以密钥文件的权限一路收窄。
func TestSetKeyInSection_KeepsFileMode(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := PersistUseProvider(path, "global.llm", "other"); err != nil {
		t.Fatalf("PersistUseProvider: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Errorf("权限 = %o, want 继承 640", st.Mode().Perm())
	}
}

// 值渲染错误要在写盘之前挡掉：空右值会写出语法上就坏的文件。
func TestSetKeyInSection_RejectsEmptyValue(t *testing.T) {
	path := writeWizardConfig(t, wizardBase)
	if err := SetKeyInSection(path, "agent", "lang", ""); err == nil {
		t.Error("空值应当被拒绝（它会写出 `lang = ` 这种坏行）")
	}
	if err := SetKeyInSection(path, "", "lang", "\"x\""); err == nil {
		t.Error("空段名应当被拒绝")
	}
}

func mustFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
