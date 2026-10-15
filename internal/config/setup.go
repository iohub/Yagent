// 本文件服务"首次使用引导"（docs/config-trim-onboarding-design.md）：
// 手写 config.toml 是不友好的 —— provider 块要拼三段、密钥要放对、改完还未必知道有没有生效。
// 这里提供引导页需要的三样东西：写 provider / 写首启必答项 / 判断"这台机器需不需要被领进引导页"。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ProviderDraft 是引导页里一个 provider 的完整取值。
// APIKey 与 APIKeyEnv 互斥：写了变量名就不再落明文，WriteProvider 会删掉另一行。
type ProviderDraft struct {
	Name            string // [global.llm.providers.<Name>]
	Model           string
	APIBaseURL      string
	APIKey          string
	APIKeyEnv       string
	APIFormat       string // "" | "openai" | "anthropic"
	ReasoningEffort string // "" = 不发这个字段
	MaxTokens       int    // <=0 = 不写
	ContextWindow   int    // <=0 = 不写（TUI 上下文进度条随之消失）
	Temperature     float64
}

// providerNameRe 限定能安全拼进 TOML 表头的名字。
// 表名里出现点号或引号会造出嵌套表或畸形文件，那是"写完下次启动加载不了"级别的错误。
var providerNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// IsValidProviderName 校验 provider 名。
func IsValidProviderName(name string) error {
	if name == "" {
		return fmt.Errorf("config: provider name is empty")
	}
	if !providerNameRe.MatchString(name) {
		return fmt.Errorf("config: provider name %q must be 1-64 chars of A-Z a-z 0-9 _ -", name)
	}
	return nil
}

// WriteProvider 写入（或覆盖）一个 provider 块。
// 走 SetKeysInSection 的文本级替换，所以块外的注释与行序不受影响；
// 块是新建时按字段声明顺序追加到文件末尾。
func WriteProvider(path string, d ProviderDraft) error {
	if err := IsValidProviderName(d.Name); err != nil {
		return err
	}
	if strings.TrimSpace(d.Model) == "" {
		return fmt.Errorf("config: provider %q has no model", d.Name)
	}
	if strings.TrimSpace(d.APIBaseURL) == "" && d.APIKeyEnv == "" && d.APIKey == "" {
		return fmt.Errorf("config: provider %q needs api_base_url plus a key (or api_key_env)", d.Name)
	}

	section := "global.llm.providers." + d.Name
	kv := map[string]string{
		"model":        tomlBasicString(d.Model),
		"api_base_url": tomlBasicString(d.APIBaseURL),
	}
	if d.APIKeyEnv != "" {
		kv["api_key_env"] = tomlBasicString(d.APIKeyEnv)
	} else {
		kv["api_key"] = tomlBasicString(d.APIKey)
	}
	if d.APIFormat != "" && !strings.EqualFold(d.APIFormat, "openai") {
		kv["api_format"] = tomlBasicString(strings.ToLower(d.APIFormat))
	}
	if d.ReasoningEffort != "" {
		kv["reasoning_effort"] = tomlBasicString(d.ReasoningEffort)
	}
	if d.MaxTokens > 0 {
		kv["max_tokens"] = strconv.Itoa(d.MaxTokens)
	}
	if d.ContextWindow > 0 {
		kv["context_window"] = strconv.Itoa(d.ContextWindow)
	}
	// temperature 的 0 按"没配"处理（裸 float64 分不清写了 0.0 和没写，
	// 而显式发 temperature:0 会把服务端采样顶成贪心解码），所以 0 时不写这一行。
	if d.Temperature > 0 {
		kv["temperature"] = strconv.FormatFloat(d.Temperature, 'g', -1, 64)
	}

	// 声明顺序：map 无序，这里给一个固定顺序，保证新建块的行序可预期。
	for _, key := range providerKeyOrder(kv) {
		if err := SetKeyInSection(path, section, key, kv[key]); err != nil {
			return fmt.Errorf("config: write provider %q: %w", d.Name, err)
		}
	}

	// 互斥与"收回默认"都必须显式删行，否则旧的明文 key 会以"已经不生效的那一行"
	// 继续留在文件里。
	//
	// temperature 不在这张表里：它不是表单管理的字段，WriteProvider 只在
	// draft.Temperature > 0 时把它写成那个值，否则原样留着文件里的行。
	// "调用方没说 = 继承"是这里唯一安全的默认 —— 反过来（0 就删行）会让每个新增的
	// 可编辑字段都埋一颗"保存一次抹掉一个用户设置"的雷。
	drop := map[string]bool{
		"api_key":          d.APIKeyEnv != "",
		"api_key_env":      d.APIKeyEnv == "",
		"api_format":       d.APIFormat == "" || strings.EqualFold(d.APIFormat, "openai"),
		"reasoning_effort": d.ReasoningEffort == "",
		"max_tokens":       d.MaxTokens <= 0,
		"context_window":   d.ContextWindow <= 0,
	}
	for _, key := range providerKeyOrder(drop) {
		if !drop[key] {
			continue
		}
		if err := RemoveKeyInSection(path, section, key); err != nil {
			return fmt.Errorf("config: write provider %q: remove %s: %w", d.Name, key, err)
		}
	}
	return nil
}

// providerKeyOrder 让 map 迭代有确定顺序（同一份输入两次写出逐字节相同的文件）。
func providerKeyOrder[T any](m map[string]T) []string {
	order := []string{
		"model", "api_base_url", "api_key", "api_key_env", "api_format",
		"reasoning_effort", "max_tokens", "context_window", "temperature",
	}
	out := make([]string, 0, len(m))
	seen := map[string]bool{}
	for _, k := range order {
		if _, ok := m[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// DeleteProvider 删除一个 provider 块。它还被任何 use_provider / fallback_providers
// 引用时直接拒绝 —— 留下一个"加载即失败"的文件比拒绝一次删除更糟。
func DeleteProvider(path, name string) error {
	refs, err := ProviderReferences(path, name)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("config: provider %q is still referenced by: %s", name, strings.Join(refs, "; "))
	}
	return RemoveSection(path, "global.llm.providers."+name)
}

// DecodeRaw 把配置文件解成无类型 map。引导页与引用扫描都走这里而不是 LoadFromFile：
// "想删掉一个坏 provider"恰恰常发生在配置已经加载不了的时候。
func DecodeRaw(path string) (map[string]any, error) {
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}
	return raw, nil
}

// ProviderReferences 列出配置文件里引用该 provider 的位置。
func ProviderReferences(path, name string) ([]string, error) {
	raw, err := DecodeRaw(path)
	if err != nil {
		return nil, err
	}
	return ProviderRefs(raw, name), nil
}

// ProviderRefs 在已解码的 map 上找引用位置（人话，可直接显示给用户）。
// 导出是因为引导页也持有同一份 raw：引用判据只能有一处实现，
// 两份同形实现迟早对不上（AGENTS.md §11 坑 18 的老病）。
func ProviderRefs(raw map[string]any, name string) []string {
	var refs []string
	mark := func(where, got string) {
		if got == name {
			refs = append(refs, where)
		}
	}

	mark("global.llm.use_provider", stringAt(raw, "global", "llm", "use_provider"))
	mark("agents.llm.use_provider", stringAt(raw, "agents", "llm", "use_provider"))
	mark("tools.llm.use_provider", stringAt(raw, "tools", "llm", "use_provider"))

	for _, parent := range []string{"agents", "tools"} {
		if tbl, ok := raw[parent].(map[string]any); ok {
			if llmTbl, ok := tbl["llm"].(map[string]any); ok {
				for sub, v := range llmTbl {
					if entry, ok := v.(map[string]any); ok {
						mark(parent+".llm."+sub+".use_provider", stringAt2(entry, "use_provider"))
					}
				}
			}
		}
	}

	if providers, ok := pathAt(raw, "global", "llm", "providers").(map[string]any); ok {
		for pname, v := range providers {
			entry, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for _, fb := range anySlice(entry["fallback_providers"]) {
				switch f := fb.(type) {
				case string:
					if f == name {
						refs = append(refs, "global.llm.providers."+pname+".fallback_providers")
					}
				case map[string]any:
					if stringAt2(f, "provider") == name {
						refs = append(refs, "global.llm.providers."+pname+".fallback_providers")
					}
				}
			}
		}
	}
	sort.Strings(refs)
	return refs
}

func pathAt(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		next, ok := cur.(map[string]any)[k]
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func stringAt(m map[string]any, keys ...string) string {
	s, _ := pathAt(m, keys...).(string)
	return s
}

func stringAt2(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// FirstRunDraft 是引导页在 provider 之外会问的几项（决策记录 D3）。
// 只覆盖"首启真的需要决定"的键，其余全部保持模板默认。
type FirstRunDraft struct {
	UseProvider    string        // [global.llm] use_provider
	Lang           string        // [agent] lang；"" = 不注入语言指令
	YoloMode       bool          // [agent] yolo_mode
	FullYoloMode   bool          // [agent] full_yolo_mode
	RoutingMode    string        // [routing] mode：auto | fixed
	BrowserEnabled bool          // [browser] enabled
	LLMTimeout     time.Duration // [llm] timeout；<=0 = 不写
}

// WriteFirstRun 写首启必答项。逐项用文本级替换，注释与无关行不动。
func WriteFirstRun(path string, d FirstRunDraft) error {
	if d.UseProvider != "" {
		if err := PersistUseProvider(path, "global.llm", d.UseProvider); err != nil {
			return err
		}
	}
	if d.RoutingMode != "" {
		if err := PersistRoutingMode(path, d.RoutingMode); err != nil {
			return err
		}
	}
	if d.Lang != "" {
		if err := SetKeyInSection(path, "agent", "lang", tomlBasicString(d.Lang)); err != nil {
			return err
		}
	}
	if err := SetKeyInSection(path, "agent", "yolo_mode", strconv.FormatBool(d.YoloMode)); err != nil {
		return err
	}
	if err := SetKeyInSection(path, "agent", "full_yolo_mode", strconv.FormatBool(d.FullYoloMode)); err != nil {
		return err
	}
	// [browser] 的 enabled 没有缺省值可兜：写了这一段就必须写它（AGENTS.md §11 坑 13）。
	if err := SetKeyInSection(path, "browser", "enabled", strconv.FormatBool(d.BrowserEnabled)); err != nil {
		return err
	}
	if d.LLMTimeout > 0 {
		if err := PersistLLMTimeout(path, d.LLMTimeout); err != nil {
			return err
		}
	}
	return nil
}

// placeholderAPIKeys 是模板与文档里出现过的占位密钥。
// 判据要精确到"这些串"而不是长度启发式：真实密钥也可能很短，误判会把能用的人锁在门外。
var placeholderAPIKeys = map[string]bool{
	"":                true,
	"your-key":        true,
	"sk-your-key":     true,
	"your_api_key":    true,
	"api_key":         true,
	"changeme":        true,
	"replace_me":      true,
	"REPLACE_ME":      true,
	"sk-ant-your-key": true,
	"dummy-key":       true,
}

// IsPlaceholderKey 报告这个值是不是"还没填"。
// 模板自带 api_key = "your-key"，而 validate() 只查非空 —— 于是全新机器校验通过、
// TUI 正常启动，直到第一次提交任务才在 LLM 调用上炸（且错误只进日志文件）。
func IsPlaceholderKey(s string) bool {
	if placeholderAPIKeys[strings.TrimSpace(s)] {
		return true
	}
	t := strings.TrimSpace(s)
	// 全 x/X 的占位（"xxx"、"XXXXXXXX"）
	if t != "" && strings.Trim(t, "xX") == "" {
		return true
	}
	return strings.HasPrefix(t, "your-")
}

// SetupReason 是"为什么要进引导页"。
type SetupReason int

const (
	// SetupNotNeeded 配置看着可用。
	SetupNotNeeded SetupReason = iota
	// SetupFreshInstall 配置文件是本次启动才生成的。
	SetupFreshInstall
	// SetupPlaceholderKey 生效 provider 的密钥还是占位符。
	SetupPlaceholderKey
	// SetupBroken 加载或校验失败，详情在 Detail。
	SetupBroken
)

func (r SetupReason) String() string {
	switch r {
	case SetupFreshInstall:
		return "fresh-install"
	case SetupPlaceholderKey:
		return "placeholder-key"
	case SetupBroken:
		return "broken-config"
	default:
		return "none"
	}
}

// NeedsSetup 决定这次启动要不要把用户领进引导页。判据全部来自配置本身
// （没写、还是占位符、还是读不了），不引入"看过一次就不再问"的标记文件 ——
// 那是第二份事实，会和配置本身漂移。
//
// 注意 fileCreated 只能由 EnsureConfigExists 给出：模板已存在但密钥没填的情形
// 靠 SetupPlaceholderKey 接住，两条判据合起来才覆盖"全新机器"的两种走法
// （第一次启动就配好 / 生成后一直没管）。
func NeedsSetup(cfg *Config, path string, fileCreated bool, loadErr error) (SetupReason, string) {
	if loadErr != nil {
		return SetupBroken, loadErr.Error()
	}
	if fileCreated {
		return SetupFreshInstall, fmt.Sprintf(
			"first run — %s was just generated; its api_key is still the template placeholder", path)
	}
	if cfg == nil || cfg.Global.LLM == nil {
		return SetupBroken, "no LLM providers configured"
	}
	name := cfg.resolveEffectiveProviderName()
	provider, err := cfg.GetProvider(name)
	if err != nil {
		return SetupBroken, err.Error()
	}
	// 走环境变量提供的密钥不在文件里，IsPlaceholderKey 不适用；
	// 变量没设置会在 validate() 就失败，落到 SetupBroken。
	if provider.APIKeyEnv != "" {
		return SetupNotNeeded, ""
	}
	if IsPlaceholderKey(provider.APIKey) {
		return SetupPlaceholderKey, fmt.Sprintf("provider %q still has api_key = %q", name, provider.APIKey)
	}
	return SetupNotNeeded, ""
}

// ConfigDir 返回引导页要落盘的配置目录（~/.yagent/config），供"该写哪个文件"的展示用。
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config"
	}
	return filepath.Join(home, ".yagent", "config")
}
