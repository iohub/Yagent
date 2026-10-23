package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"yagent/internal/mcp"
	"yagent/internal/toolspec"
)

// 本文件把"运行期发现的外部 MCP 工具"翻译成 toolspec.Spec。
// 它不进全局注册表：动态规格只经 Deps.ExternalSpecs 到达 Surface.rebuild()，
// 于是 schemas.json 的 1:1 校验、golden 探针、Meta 的工具池都不受第三方能力污染。

// MCPCaller 是动态规格执行器需要的全部能力。由 *mcp.Manager 天然实现。
type MCPCaller interface {
	Call(ctx context.Context, server, tool string, args map[string]interface{}) (*mcp.ToolCallResult, error)
}

// ExternalInput 是构造动态规格的全部输入。装配层填好后交给 BuildExternalSpecs，
// 本函数不读配置、不碰 Manager（除 Caller 与 Health 两个窄钩子）。
type ExternalInput struct {
	Tools  []mcp.ServerTool
	Caller MCPCaller
	// Config 按 server 名给出白名单/黑名单/allow_write/curated 等策略。
	Config map[string]mcp.ServerConfig
	// Health 报告某个 server 此刻是否可用（不可用时返回能读的原因）。
	Health func(server string) (ready bool, reason string)
	// Static 判定名字是否已被静态注册表占用。nil ⇒ 用 toolspec.Get。
	Static func(name string) bool
}

// ExternalAccount 是"发现了多少、注册了多少、剩下的为什么没进"的一行账。
// 每个拒收理由都必须可区分：把四类原因合并成一句"被过滤了"等于没记账。
type ExternalAccount struct {
	Total      int
	Registered int
	Excluded   int
	ByReason   map[string][]string
}

// accountReason 记一条拒收。
func (a *ExternalAccount) accountReason(reason, name string) {
	if a.ByReason == nil {
		a.ByReason = make(map[string][]string)
	}
	a.ByReason[reason] = append(a.ByReason[reason], name)
}

// String 渲染一行账（启动日志与 /mcp 面板共用这一份实现）。
func (a ExternalAccount) String() string {
	if a.Total == 0 {
		return "mcp tools: none discovered"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mcp tools: %d discovered, %d registered, %d excluded",
		a.Total, a.Registered, a.Excluded)
	reasons := make([]string, 0, len(a.ByReason))
	for reason := range a.ByReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		names := append([]string(nil), a.ByReason[reason]...)
		sort.Strings(names)
		fmt.Fprintf(&b, "; %s: %s", reason, strings.Join(names, ", "))
	}
	return b.String()
}

// 拒收理由。名字会出现在日志与面板里，所以用可读的短横线写法。
const (
	reasonCurated        = "curated"
	reasonNotInWhitelist = "not-in-whitelist"
	reasonExcluded       = "excluded"
	reasonOverMax        = "over-max"
	reasonNameCollision  = "name-collision"
	reasonInvalidSpec    = "invalid-spec"
	reasonWriteNotAllow  = "write-not-allowed"
)

// BuildExternalSpecs 把一次发现翻译成动态规格，并逐条记账。
func BuildExternalSpecs(in ExternalInput) ([]*toolspec.Spec, ExternalAccount) {
	account := ExternalAccount{Total: len(in.Tools)}
	if in.Static == nil {
		in.Static = func(name string) bool {
			_, ok := toolspec.Get(name)
			return ok
		}
	}

	type candidate struct {
		spec *toolspec.Spec
	}
	perServer := map[string][]candidate{}
	seen := make(map[string]bool, len(in.Tools))

	for _, st := range in.Tools {
		cfg := in.Config[st.Server]
		tool := st.Tool

		switch {
		case cfg.Curated[tool.Name]:
			account.accountReason(reasonCurated, tool.Name)
			continue
		case len(cfg.Tools) > 0 && !containsString(cfg.Tools, tool.Name):
			account.accountReason(reasonNotInWhitelist, tool.Name)
			continue
		case containsString(cfg.Exclude, tool.Name):
			account.accountReason(reasonExcluded, tool.Name)
			continue
		}

		effect := externalEffect(tool, cfg.ReadOnly)
		if effect == toolspec.EffectExternalWrite && !cfg.AllowWrite {
			account.accountReason(reasonWriteNotAllow, tool.Name)
			continue
		}

		name := ExternalToolName(st.Server, tool.Name)
		if in.Static(name) || seen[name] {
			account.accountReason(reasonNameCollision, tool.Name)
			continue
		}

		spec := externalSpec(st.Server, tool, name, effect, in)
		if problems := toolspec.ValidateDynamic([]*toolspec.Spec{spec}); len(problems) > 0 {
			account.accountReason(reasonInvalidSpec, tool.Name)
			continue
		}
		seen[name] = true
		perServer[st.Server] = append(perServer[st.Server], candidate{spec: spec})
	}

	// 每个 server 的工具数上限：按最终名字排序保留前 N 条（确定性），其余记账。
	servers := make([]string, 0, len(perServer))
	for server := range perServer {
		servers = append(servers, server)
	}
	sort.Strings(servers)

	var specs []*toolspec.Spec
	for _, server := range servers {
		list := perServer[server]
		sort.Slice(list, func(i, j int) bool { return list[i].spec.Name < list[j].spec.Name })
		limit := in.Config[server].MaxTools
		if limit <= 0 {
			limit = mcp.DefaultMaxToolsPerServer()
		}
		for i, c := range list {
			if i >= limit {
				account.accountReason(reasonOverMax, c.spec.Name)
				continue
			}
			specs = append(specs, c.spec)
		}
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	account.Registered = len(specs)
	account.Excluded = account.Total - account.Registered
	return specs, account
}

// externalEffect 判定外部工具的能力族。
//
// 优先级严格如下，安全默认在最后一条：
//   - readOnlyHint=true 或名字在 server.ReadOnly 名单里 ⇒ 只读（可并发、免授权）；
//   - readOnlyHint=false 或 destructiveHint=true ⇒ 写；
//   - 什么都没声明 ⇒ **按写处理**。判错的代价是模型多问一次授权；
//     判对的代价是第三方 server 拿到一个不受守卫的写入口。
func externalEffect(tool mcp.DiscoveredTool, readOnlyList []string) toolspec.Effect {
	if containsString(readOnlyList, tool.Name) {
		return toolspec.EffectExternalRead
	}
	if ann := tool.Annotations; ann != nil {
		if ann.ReadOnlyHint != nil {
			if *ann.ReadOnlyHint {
				return toolspec.EffectExternalRead
			}
			return toolspec.EffectExternalWrite
		}
		if ann.DestructiveHint != nil && *ann.DestructiveHint {
			return toolspec.EffectExternalWrite
		}
	}
	return toolspec.EffectExternalWrite
}

// externalSpec 组装一条动态规格。除能力族之外字段一律固定：
// 一律 Cold（走既有 deferred 目录与 load_tool）、TruncFirst（大体积可重取）、
// DisplayCompact（TUI 不展开第三方结果正文）。
func externalSpec(server string, tool mcp.DiscoveredTool, name string, effect toolspec.Effect, in ExternalInput) *toolspec.Spec {
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		description = fmt.Sprintf("%s tool from MCP server %q.", tool.Name, server)
	}
	spec := &toolspec.Spec{
		Name:        name,
		Effect:      effect,
		Frequency:   toolspec.Cold,
		Mutates:     effect == toolspec.EffectExternalWrite,
		Serial:      effect == toolspec.EffectExternalWrite,
		NeedsAuth:   effect == toolspec.EffectExternalWrite,
		Trunc:       toolspec.TruncFirst,
		Display:     toolspec.DisplayCompact,
		ResultPaths: toolspec.ResultPathsJSON,
		Brief:       truncateRunes(toolspec.FirstSentence(description), maxExternalBriefRunes),
		Inline: &toolspec.Schema{
			Name:        name,
			Description: fmt.Sprintf("%s (via MCP server %q)", description, server),
			Parameters:  externalParameters(tool.InputSchema),
		},
		Available: func(*toolspec.Deps) (bool, string) {
			if in.Health == nil {
				return true, ""
			}
			return in.Health(server)
		},
	}
	spec.Build = buildExternalTool(spec, server, tool.Name, in.Caller)
	return spec
}

// externalParameters 规范化 tools/list 的 inputSchema。
// 缺省补成空 object，而不是 nil：nil 在 ValidateDynamic 里是"声明了一个无参工具"
// 的硬错误，而"这个 server 没写 schema"本身不该让整个工具消失。
func externalParameters(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	if _, ok := schema["type"]; !ok {
		out := make(map[string]interface{}, len(schema)+1)
		for k, v := range schema {
			out[k] = v
		}
		out["type"] = "object"
		return out
	}
	return schema
}

func buildExternalTool(spec *toolspec.Spec, server, toolName string, caller MCPCaller) func(*toolspec.Deps) (toolspec.ToolFunc, error) {
	return func(*toolspec.Deps) (toolspec.ToolFunc, error) {
		if caller == nil {
			return nil, fmt.Errorf("MCP caller 未装配")
		}
		return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			res, err := caller.Call(ctx, server, toolName, params)
			if err != nil {
				return nil, err
			}
			if res == nil {
				return nil, fmt.Errorf("MCP 工具 %q 返回空结果", spec.Name)
			}
			if res.IsError {
				return nil, fmt.Errorf("MCP 工具 %q 返回错误: %s", spec.Name, renderMCPContent(res))
			}
			return renderMCPContent(res), nil
		}, nil
	}
}

// renderMCPContent 把 content 块拼成文本。非文本块显式渲染成占位，
// 不静默丢 —— 模型看不到图，但至少知道"这里有东西"。
func renderMCPContent(res *mcp.ToolCallResult) string {
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
			continue
		}
		parts = append(parts, "[unsupported content type: "+c.Type+"]")
	}
	return strings.Join(parts, "\n")
}

// maxExternalBriefRunes 与 toolspec 里 maxBriefRunes 同值（40）。
// 不在 toolspec 导出那个常数：它是校验阈值，外部构造侧只需要"同样长"这个事实。
const maxExternalBriefRunes = 40

// truncateRunes 按 rune 截断：按字节切会把中文切成半个字，目录里就是乱码。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// ExternalToolName 是外部工具的模型可见名字：mcp__<server>__<tool>。
//
// 前缀让"这是外部工具"在日志、TUI 与 rollout 里一眼可辨；`__` 分隔符与静态名
// （全是单段 snake_case）不可能撞。超 64 字符时截到 55 + "-" + 名字哈希前 8 hex：
// 确定性、可复现，与 projectid.Compute 同风格。
func ExternalToolName(server, tool string) string {
	safeServer := sanitizeNamePart(server)
	safeTool := sanitizeNamePart(tool)
	full := "mcp__" + safeServer + "__" + safeTool
	if len(full) <= 64 {
		return full
	}
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	return full[:55] + "-" + hex.EncodeToString(sum[:4])
}

// sanitizeNamePart 把 provider 不接受字符换成下划线。
// provider 的 function name 只允许 [a-zA-Z0-9_-]，而 server 名与工具名来自第三方。
func sanitizeNamePart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
