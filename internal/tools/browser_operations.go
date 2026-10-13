package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"yagent/internal/browserctl"
)

const (
	defaultInspectLimit = 30
	maxInspectLimit     = 80
	defaultTextChars    = 2500
	maxTextChars        = 8000
	defaultWaitSeconds  = 10
	maxWaitSeconds      = browserctl.MaxWaitSeconds
	defaultClickWait    = 3
	maxClickWait        = 30
	defaultScrollDY     = -300
)

// BrowserOperationsTool 把上游 browser-control CLI 的子命令暴露成 yagent 工具。
//
// 与其它工具层一样，它只做三件事：参数 → argv、输出 → 紧凑文本、失败 → 可自诊的提示。
// 真正的进程与工作区生命周期在 internal/browserctl。
type BrowserOperationsTool struct {
	ctrl   browserctl.Controller
	cfg    browserctl.Config
	policy *browserctl.Policy
}

func NewBrowserOperationsTool(ctrl browserctl.Controller, cfg browserctl.Config) *BrowserOperationsTool {
	return &BrowserOperationsTool{
		ctrl:   ctrl,
		cfg:    cfg,
		policy: browserctl.NewPolicy(cfg.AllowedDomains, cfg.DeniedDomains),
	}
}

// browserToolSpec 用声明式表描述一个工具，避免在 Agent 的 lookupToolFunc
// switch 里逐个 case（那条路径漏一个 case 就是静默失效）。
type browserToolSpec struct {
	name string
	// plan 产出要执行的 CLI 调用序列（多数工具只有一个；状态与观察类会组合两条）。
	plan func(t *BrowserOperationsTool, p map[string]interface{}) ([][]string, error)
	// prepare 在拿到会话后可改写参数（如把产物路径收敛到会话工作区内）。
	prepare func(t *BrowserOperationsTool, p map[string]interface{}, sessionDir string) error
	render  func(t *BrowserOperationsTool, parts []*browserctl.Result, p map[string]interface{}) string
	// guardURLParams 列出需要过 URL 守卫的参数名。
	guardURLParams []string
	// tolerateTailFailure 表示后续命令失败不算整体失败（状态类工具：没有页面也要能报诊断）。
	tolerateTailFailure bool
}

var browserToolSpecs = []browserToolSpec{
	{
		name: "browser_status",
		plan: func(_ *BrowserOperationsTool, _ map[string]interface{}) ([][]string, error) {
			// doctor 给出端点/浏览器/daemon 状态，page-info 给出当前页面，合成一次调用
			return [][]string{{"doctor"}, {"page-info"}}, nil
		},
		render:              renderStatus,
		tolerateTailFailure: true,
	},
	{
		name: "browser_open",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			url, err := requireString(p, "url")
			if err != nil {
				return nil, err
			}
			return [][]string{{"open", url}}, nil
		},
		render:         renderAction,
		guardURLParams: []string{"url"},
	},
	{
		name: "browser_inspect",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			argv := []string{"inspect", "--json"}
			if q := paramString(p, "query"); q != "" {
				argv = append(argv, "-q", q)
			}
			argv = append(argv,
				"--limit", strconv.Itoa(paramInt(p, "limit", defaultInspectLimit, 1, maxInspectLimit)),
				"--text", strconv.Itoa(paramInt(p, "text_chars", defaultTextChars, 0, maxTextChars)),
			)
			return [][]string{{"page-info"}, argv}, nil
		},
		render: renderInspect,
	},
	{
		name: "browser_snapshot",
		plan: func(_ *BrowserOperationsTool, _ map[string]interface{}) ([][]string, error) {
			return [][]string{{"snapshot", "--json"}}, nil
		},
		render: renderSnapshot,
	},
	{
		name: "browser_click",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			target, err := requireString(p, "target")
			if err != nil {
				return nil, err
			}
			argv := []string{
				"click", target,
				"--wait", strconv.Itoa(paramInt(p, "wait", defaultClickWait, 0, maxClickWait)),
				"--button", paramEnum(p, "button", "left", []string{"left", "right", "middle"}),
				"--clicks", strconv.Itoa(paramInt(p, "clicks", 1, 1, 2)),
			}
			if frame := paramString(p, "frame"); frame != "" {
				argv = append(argv, "--frame", frame)
			}
			return [][]string{argv}, nil
		},
		render: renderAction,
	},
	{
		name: "browser_fill",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			target, err := requireString(p, "target")
			if err != nil {
				return nil, err
			}
			value, err := requireString(p, "value")
			if err != nil {
				return nil, err
			}
			argv := []string{"fill", target, value, "--wait", strconv.Itoa(paramInt(p, "wait", defaultClickWait, 0, maxClickWait))}
			if frame := paramString(p, "frame"); frame != "" {
				argv = append(argv, "--frame", frame)
			}
			return [][]string{argv}, nil
		},
		render: renderAction,
	},
	{
		name: "browser_type",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			text, err := requireString(p, "text")
			if err != nil {
				return nil, err
			}
			return [][]string{{"type", text}}, nil
		},
		render: renderAction,
	},
	{
		name: "browser_press",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			key, err := requireString(p, "key")
			if err != nil {
				return nil, err
			}
			return [][]string{{"press", key}}, nil
		},
		render: renderAction,
	},
	{
		name: "browser_scroll",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			argv := []string{
				"scroll",
				strconv.Itoa(paramInt(p, "dy", defaultScrollDY, -10000, 10000)),
				strconv.Itoa(paramInt(p, "dx", 0, -10000, 10000)),
			}
			if at := paramString(p, "at"); at != "" {
				argv = append(argv, "--at", at)
			}
			return [][]string{argv}, nil
		},
		render: renderAction,
	},
	{
		name: "browser_select",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			target, err := requireString(p, "target")
			if err != nil {
				return nil, err
			}
			value, err := requireString(p, "value")
			if err != nil {
				return nil, err
			}
			return [][]string{{"select", target, value}}, nil
		},
		render: renderAction,
	},
	{
		name: "browser_tabs",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			action := paramEnum(p, "action", "", []string{"list", "new", "switch", "close"})
			if action == "" {
				return nil, fmt.Errorf(`参数 "action" 必须是 list/new/switch/close 之一`)
			}
			switch action {
			case "list":
				return [][]string{{"tabs"}}, nil
			case "new":
				argv := []string{"new-tab"}
				if url := paramString(p, "url"); url != "" {
					argv = append(argv, url)
				}
				return [][]string{argv}, nil
			case "switch":
				target, err := requireString(p, "target")
				if err != nil {
					return nil, err
				}
				return [][]string{{"switch-tab", target}}, nil
			default: // close
				argv := []string{"close-tab"}
				if target := paramString(p, "target"); target != "" {
					argv = append(argv, target)
				}
				return [][]string{argv}, nil
			}
		},
		render:         renderAction,
		guardURLParams: []string{"url"},
	},
	{
		name: "browser_wait",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			seconds := strconv.Itoa(paramInt(p, "seconds", defaultWaitSeconds, 1, maxWaitSeconds))
			switch mode := paramEnum(p, "mode", "load", []string{"load", "networkidle", "element"}); mode {
			case "element":
				selector, err := requireString(p, "selector")
				if err != nil {
					return nil, err
				}
				argv := []string{"wait-element", selector, seconds}
				if paramBool(p, "visible", false) {
					argv = append(argv, "--visible")
				}
				return [][]string{argv}, nil
			case "networkidle":
				return [][]string{{"wait", "networkidle", seconds}}, nil
			default:
				return [][]string{{"wait", "load", seconds}}, nil
			}
		},
		render: renderAction,
	},
	{
		name: "browser_screenshot",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			argv := []string{"screenshot", paramString(p, "path")}
			if paramBool(p, "full", false) {
				argv = append(argv, "--full")
			}
			return [][]string{argv}, nil
		},
		prepare: prepareArtifactPath,
		render:  renderScreenshot,
	},
	{
		name: "browser_eval",
		plan: func(_ *BrowserOperationsTool, p map[string]interface{}) ([][]string, error) {
			js, err := requireString(p, "js")
			if err != nil {
				return nil, err
			}
			argv := []string{"eval", js}
			if frame := paramString(p, "frame"); frame != "" {
				argv = append(argv, "--frame", frame)
			}
			return [][]string{argv}, nil
		},
		render: renderRaw,
	},
}

// BrowserToolNames 返回工具表里的全部工具名（排序后，供一致性测试与文档使用）。
func BrowserToolNames() []string {
	names := make([]string, 0, len(browserToolSpecs))
	for _, s := range browserToolSpecs {
		names = append(names, s.name)
	}
	sort.Strings(names)
	return names
}

// BuildAdapters 依据 tools.json 的声明生成适配器。
// tools.json 是 schema 的单一事实来源；返回的错误列出两边的差异（声明了没实现 / 实现了没声明）。
func (t *BrowserOperationsTool) BuildAdapters(defs []ToolDefinition, guard *WorkspaceGuard) ([]*Adapter, []error) {
	byName := make(map[string]browserToolSpec, len(browserToolSpecs))
	for _, s := range browserToolSpecs {
		byName[s.name] = s
	}

	var errs []error
	declared := make(map[string]bool, len(browserToolSpecs))
	adapters := make([]*Adapter, 0, len(browserToolSpecs))

	for _, def := range defs {
		if !strings.HasPrefix(def.Name, "browser_") {
			continue
		}
		declared[def.Name] = true
		spec, ok := byName[def.Name]
		if !ok {
			errs = append(errs, fmt.Errorf("tools.json 声明了 %s，但 browserToolSpecs 未实现", def.Name))
			continue
		}
		s := spec
		fn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			return t.execute(ctx, s, params)
		}
		adapters = append(adapters, NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters))
	}
	for _, s := range browserToolSpecs {
		if !declared[s.name] {
			errs = append(errs, fmt.Errorf("%s 已在 browserToolSpecs 实现，但 tools.json 未声明", s.name))
		}
	}

	SetGuardOnAdapters(adapters, guard)
	return adapters, errs
}

func (t *BrowserOperationsTool) execute(ctx context.Context, spec browserToolSpec, params map[string]interface{}) (interface{}, error) {
	if ok, reason := t.ctrl.Probe(); !ok {
		return nil, fmt.Errorf("浏览器能力不可用：%s", reason)
	}
	for _, name := range spec.guardURLParams {
		if v := paramString(params, name); v != "" {
			if err := t.policy.CheckURL(v); err != nil {
				return nil, fmt.Errorf("%s: %w", spec.name, err)
			}
		}
	}

	// 一次委派 → 一个工作区：并在同一委派内保持同一个 current-tab 与 ref 状态
	delegationID := browserctl.DelegationIDFrom(ctx)
	if delegationID == "" {
		delegationID = "task"
	}
	session, err := t.ctrl.Acquire(ctx, delegationID)
	if err != nil {
		return nil, err
	}
	defer session.Release()

	if spec.prepare != nil {
		if err := spec.prepare(t, params, session.Dir()); err != nil {
			return nil, err
		}
	}

	commands, err := spec.plan(t, params)
	if err != nil {
		return nil, err
	}

	parts := make([]*browserctl.Result, 0, len(commands))
	for _, argv := range commands {
		res, err := session.Run(ctx, argv[0], argv[1:]...)
		if err != nil {
			return nil, err
		}
		parts = append(parts, res)
		if !res.OK() && !spec.tolerateTailFailure {
			break
		}
	}
	if len(parts) == 0 {
		return map[string]interface{}{"output": ""}, nil
	}

	out := spec.render(t, parts, params)
	if !parts[0].OK() {
		return nil, fmt.Errorf("%s", shapeBrowserFailure(parts[0], out))
	}
	if last := parts[len(parts)-1]; !last.OK() && !spec.tolerateTailFailure {
		return nil, fmt.Errorf("%s", shapeBrowserFailure(last, out))
	}
	return map[string]interface{}{"output": out}, nil
}

// ────────────────────────── 渲染 ──────────────────────────

func renderAction(t *BrowserOperationsTool, parts []*browserctl.Result, _ map[string]interface{}) string {
	if len(parts) == 0 {
		return "ok"
	}
	out := strings.TrimSpace(parts[len(parts)-1].Stdout)
	if out == "" {
		out = "ok"
	}
	return browserctl.Clip(out, t.budget())
}

func renderRaw(t *BrowserOperationsTool, parts []*browserctl.Result, _ map[string]interface{}) string {
	if len(parts) == 0 {
		return ""
	}
	return browserctl.Clip(parts[len(parts)-1].Stdout, t.budget())
}

func renderSnapshot(t *BrowserOperationsTool, parts []*browserctl.Result, _ map[string]interface{}) string {
	if len(parts) == 0 {
		return ""
	}
	return browserctl.RenderSnapshot(parts[len(parts)-1].Stdout, t.budget())
}

func renderInspect(t *BrowserOperationsTool, parts []*browserctl.Result, p map[string]interface{}) string {
	if len(parts) < 2 {
		return renderAction(t, parts, p)
	}
	textChars := paramInt(p, "text_chars", defaultTextChars, 0, maxTextChars)
	return browserctl.RenderInspect(parts[1].Stdout, t.budget(), textChars)
}

// renderStatus 把 doctor 文本与当前页面信息合成一段，省掉模型的一次额外调用。
func renderStatus(t *BrowserOperationsTool, parts []*browserctl.Result, _ map[string]interface{}) string {
	var sb strings.Builder
	if len(parts) > 0 {
		sb.WriteString(strings.TrimSpace(parts[0].Stdout))
	}
	if len(parts) > 1 && parts[1].OK() {
		page := strings.TrimSpace(browserctl.RenderInspect(parts[1].Stdout, t.budget()/2, 0))
		if page != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(page)
		}
	} else if len(parts) > 1 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("当前没有可用的页面（可用 browser_open 打开一个）")
	}
	if sb.Len() == 0 {
		return "browser-control 未返回诊断信息"
	}
	return browserctl.Clip(sb.String(), t.budget())
}

func renderScreenshot(_ *BrowserOperationsTool, _ []*browserctl.Result, p map[string]interface{}) string {
	path := paramString(p, "path")
	if path == "" {
		return "截图已保存"
	}
	return "截图已保存: " + path
}

func (t *BrowserOperationsTool) budget() int {
	if t.cfg.MaxOutputBytes > 0 {
		return t.cfg.MaxOutputBytes
	}
	return 24000
}

// ────────────────────────── 产物路径 ──────────────────────────

// prepareArtifactPath 把截图路径收敛到会话工作区内。
// 这是 Go 侧的硬约束：上游会原样写绝对路径，不能让它落到别处。
func prepareArtifactPath(_ *BrowserOperationsTool, p map[string]interface{}, sessionDir string) error {
	raw := paramString(p, "path")
	if raw == "" {
		raw = fmt.Sprintf("shot-%s.png", time.Now().Format("150405"))
	}

	var abs string
	if filepath.IsAbs(raw) {
		abs = filepath.Clean(raw)
	} else {
		if strings.Contains(raw, "..") {
			return fmt.Errorf("截图路径不允许包含 ..（仅允许会话工作区内的相对路径）")
		}
		abs = filepath.Join(sessionDir, filepath.Clean(raw))
	}

	rel, err := filepath.Rel(sessionDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("截图只能保存在会话工作区内，拒绝写入 %s", raw)
	}
	p["path"] = abs
	return nil
}

// ────────────────────────── 失败整形 ──────────────────────────

// shapeBrowserFailure 在上游原文后追加一句可行动的提示。
// 只追加不替换：上游的 stderr 与 trace 才是自诊信息。
func shapeBrowserFailure(res *browserctl.Result, rendered string) string {
	body := strings.TrimSpace(rendered)
	if body == "" || body == "ok" {
		body = strings.TrimSpace(res.Combined())
	}
	if body == "" {
		body = fmt.Sprintf("browser-control %s 失败（exit %d）", res.Command, res.ExitCode)
	}
	if hint := browserFailureHint(strings.Join([]string{res.Stdout, res.Stderr, res.Trace}, "\n")); hint != "" {
		body += "\n提示：" + hint
	}
	return body
}

func browserFailureHint(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "not found: @"):
		return "ref 已失效（页面结构已变化）。先调用 browser_snapshot 重新获取 ref，再重试。"
	case strings.Contains(lower, "element not found after"):
		return "元素可能尚未渲染或在 iframe 内。用 browser_inspect(query=...) 确认，或加大 wait 参数。"
	case strings.Contains(lower, "no cdp endpoint"):
		return "浏览器会话已断开。先调用 browser_status 确认状态。"
	case strings.Contains(lower, "chrome not found"):
		return "未找到 Chrome：安装 Chrome/Chromium，或用 [browser] chrome_path 指定可执行文件路径。"
	case strings.Contains(lower, "network idle timeout"):
		return "页面网络一直不空闲。改用 browser_wait(mode=\"element\", selector=...) 精确等待目标元素。"
	case strings.Contains(lower, "cdp timeout"), strings.Contains(lower, "timed out"):
		return "CDP 超时。缩小等待秒数，或先 browser_wait(mode=\"load\") 等页面稳定再操作。"
	case strings.Contains(lower, "page websocket not found"):
		return "没有可用的页面标签。用 browser_tabs(action=\"new\", url=...) 新建一个。"
	default:
		return ""
	}
}

// ────────────────────────── 参数助手 ──────────────────────────

func paramString(p map[string]interface{}, key string) string {
	v, _ := p[key].(string)
	return strings.TrimSpace(v)
}

func requireString(p map[string]interface{}, key string) (string, error) {
	v := paramString(p, key)
	if v == "" {
		return "", fmt.Errorf("参数 %q 必须是非空字符串", key)
	}
	return v, nil
}

func paramInt(p map[string]interface{}, key string, def, min, max int) int {
	var n int
	switch v := p[key].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return def
		}
		n = parsed
	default:
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func paramBool(p map[string]interface{}, key string, def bool) bool {
	switch v := p[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return def
}

func paramEnum(p map[string]interface{}, key, def string, allowed []string) string {
	v := strings.ToLower(paramString(p, key))
	for _, a := range allowed {
		if v == a {
			return a
		}
	}
	return def
}
