// Provider 端点预设（表单屏 `p`）。
//
// 这些 URL 是"省事"，不是权威：网关地址会改，路径也会改。所以预设只填空字段
// （绝不覆盖用户已经写过的值，见 applyPreset），而探测屏才是"这个端点认不认这把 key"
// 的唯一事实来源。想加一档就加一行，别加"智能推断" —— 猜错的 base_url 比让用户多打一遍
// 更难排查。
//
// internal/models 的目录里没有 base_url（只有 limit.context 与 benchmark），
// 所以这份表只能是手写的，不要把它伪装成从目录推导。
package tui

// endpointPreset 是一家 OpenAI-compatible（或 Anthropic 原生）网关的最小填写信息。
// Model 是建议值：填进 model 行仍然可改，目录命中与否由 catalogHint 实况反馈。
type endpointPreset struct {
	Name      string
	BaseURL   string
	APIFormat string
	Model     string
	Note      string
}

// endpointPresets 与 docs 里"可用 provider"那份名单一致
// （internal/config/default_config.toml 的注释头也列了同一批）。
var endpointPresets = []endpointPreset{
	{
		Name:    "DashScope / Bailian (Alibaba)",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Model:   "qwen3-coder-plus",
		Note:    "api_key_env = DASHSCOPE_API_KEY is the usual setup",
	},
	{
		Name:    "DeepSeek",
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-chat",
		Note:    "reasoning_effort is ignored by deepseek-chat; use deepseek-reasoner",
	},
	{
		Name:    "Moonshot",
		BaseURL: "https://api.moonshot.cn/v1",
		Model:   "kimi-k2-0915-preview",
	},
	{
		Name:    "Z.ai (GLM)",
		BaseURL: "https://api.z.ai/api/paas/v4",
		Model:   "glm-4.6",
	},
	{
		Name:    "SiliconFlow",
		BaseURL: "https://api.siliconflow.cn/v1",
		Model:   "deepseek-ai/DeepSeek-V3",
	},
	{
		Name:    "MiMo (Xiaomi)",
		BaseURL: "https://api.xiaomimimo.com/v1",
		Model:   "mimo-v2-flash",
	},
	{
		Name:    "OpenRouter",
		BaseURL: "https://openrouter.ai/api/v1",
		Model:   "anthropic/claude-sonnet-4.5",
		Note:    "model ids are vendor/model; one key covers all upstreams",
	},
	{
		Name:    "Mistral",
		BaseURL: "https://api.mistral.ai/v1",
		Model:   "mistral-large-latest",
	},
	{
		Name:    "Local server (vLLM / llama.cpp / Ollama)",
		BaseURL: "http://127.0.0.1:8000/v1",
		Model:   "Qwen3-30B-A3B",
		Note:    "any key works — the server does not check it",
	},
	{
		Name:      "Anthropic (native Messages + Extended Thinking)",
		BaseURL:   "https://api.anthropic.com",
		APIFormat: "anthropic",
		Model:     "claude-sonnet-4-5",
		Note:      "reasoning_effort maps to thinking.budget_tokens",
	},
}

// presetTargets 列出预设会写哪些行 —— 空字段才写，这条规则只在这里定义一次。
func (w *setupWizard) applyPreset(p endpointPreset) {
	setIfEmpty := func(rowKey, value string) {
		i := formRowIndexByKey(rowKey)
		if i < 0 || i >= len(w.inputs) || w.inputs[i].Value() != "" {
			return
		}
		w.inputs[i].SetValue(value)
	}
	setIfEmpty("api_base_url", p.BaseURL)
	setIfEmpty("model", p.Model)
	if p.APIFormat != "" {
		setIfEmpty("api_format", p.APIFormat)
	}
	w.errMsg = ""
	w.note = "preset " + p.Name + " filled the empty fields only"
	w.cursor = 0
	w.presetOpen = false
	// 预设改了 model，目录那一行的实况反馈与 context_window 预填要跟上。
	w.applyCatalogPrefill()
}
