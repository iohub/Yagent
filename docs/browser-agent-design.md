# Browser-Agent 重构设计方案（基于 keon/browser-control）

> 本文件是 **Browser-Agent 重建的实施规范**，读者是执行改动的 Coding-Agent。
> 上游契约部分（第 2 节）已从 `keon/browser-control@main` 的源码逐文件核对，实施时**以本文为准**；
> 标注「未核实」的条目必须先用真实二进制验证再依赖。
> 核对时间：2026-10-13。仓库状态：`internal/agents/browser_agent.go` 是占位 stub，
> `delegate_browser` 委派链路（Director + Coding 两侧）已存在且指向该 stub。

---

## 0. 阅读须知

1. 本文的范围是**把 stub 填成真实现**，不是重搭委派链路。`delegate_browser` 的注册点已存在，只改描述与行为。
2. 第 12 节「禁止事项」是硬约束，优先级高于任何局部偏好。
3. 按第 11 节分期实施，**每期结束必须满足该期验收标准再进入下一期**；不要跨期提前实现。
4. 涉及既有注册表（`tools.json`、`knownReadOnlyTools`、`dangerousTools`、`pathParamNames`、
   `nonParallelToolNames`、`toolTruncationPriority`）的改动必须**同时**改代码与测试，漏一个就是静默失效。
5. 不要 `go mod tidy`：`go.mod` 里的 `go-rod/rod` 是历史遗留，属未决项，与本方案无关。
6. 验证只用 `go build` / `go vet` / `go test`，**不要启动长驻进程**（TUI、http server、Chrome 都不行）。
   需要真实 Chrome 的测试一律用环境变量门控（见第 10 节）。

---

## 1. 目标与非目标

### 目标

- Browser-Agent 能完成"打开页面 → 观察 → 交互 → 验证 → 汇报"的完整闭环。
- Go 侧**不引入任何浏览器/CDP 依赖**：通过 `exec` 调用外部 Rust CLI `browser-control`。
- 浏览器进程与工作区状态**不污染用户仓库**，可被 ctx 取消，崩溃可回收。
- 能力缺失（无二进制 / 无 Chrome）时优雅降级：`delegate_browser` 的描述自动变成"不可用 + 原因 + 安装指引"，
  而不是让 Director 反复委派一个必然报错的工具。

### 非目标（本期不做）

- 不做浏览器内的视觉/坐标模型推理（无 screenshot → LLM 视觉回路）。
- 不做云端 browser provider（Browser Use / Steel / Browserbase）——P2 之后再说。
- 不 fork、不 vendor 上游源码；缺失能力用 `eval` 补。
- 不把 browser 工具直接给 Director（违反强制委派原则，且快照会污染 Director 上下文）。

---

## 2. 上游 CLI 契约（必须内联，实施时不要联网查）

项目：`keon/browser-control`，crate 名 `browser-control-cli`（安装的命令是 `browser-control`），
MIT，Rust，纯 CDP，无 LLM、无 MCP、无常驻 server。我们核对的是 `main` 分支，版本 `0.1.1`。

### 2.1 我们用到的命令

| 命令 | 形态 | 输出 |
|---|---|---|
| `--help` | — | 用法（**不要用 `--version`**：本机安装的这版 clap 未声明 version，`--version` 直接报 `unexpected argument`，见 2.5 第 6 条） |
| `launch [url] [--port N] [--headless]` | 启动 Chrome 并写状态（**spawn 完立刻返回，不等端口**） | 一行 `BROWSER_CONTROL_CDP_URL=http://127.0.0.1:{port}` |
| `doctor` | 只读诊断 | 多行 `[ok  ]/[FAIL] label — detail`；CDP 不可达时**退出码非 0** |
| `status` | 只读 | `cdp: ok` + `Browser.getVersion` 的 JSON |
| `stop` | 杀掉自己启动的 Chrome | `stopped {pid}` 或 `no recorded browser-control browser` |
| `open <url>` | 当前 tab 导航 + 等 load | `ok` |
| `page-info` | 只读 | JSON `{url,title,readyState,w,h,sx,sy,pw,ph}`；**有原生对话框时返回 `{dialog:...}`** |
| `inspect [--json] [-q query] [--limit 50] [--text 2500]` | 只读，富观察 | JSON `{url,title,readyState,query,text,headings,elements[]}` |
| `snapshot [--json]` | 只读，刷新 ref | JSON `{url,title,elements[]}` |
| `text` | 只读 | `document.body.innerText` 原文 |
| `click <target> [--frame F] [--button left] [--clicks 1] [--wait 0]` | 交互 | `ok` |
| `fill <target> <text> [--frame F] [--wait 0]` | 交互 | `ok` |
| `type <text>` | 交互（`Input.insertText`） | `ok` |
| `press <key>` | 交互，支持 `ctrl+a`/`cmd+shift+t` 组合 | `ok` |
| `scroll [dy] [dx] [--at x,y]` | 交互；`--at` 走 wheel 事件（内层容器），无 `--at` 是 `window.scrollBy` | `ok` |
| `select <target> <value>` | 交互（下拉） | `ok` |
| `wait <load\|networkidle> [seconds=10]` | 等待 | `ok`，超时非 0 |
| `wait-element <selector> [seconds=10] [--visible]` | 等待 | `ok`，超时非 0 |
| `screenshot [path] [--full]` | 写文件（相对 CWD） | 打印写入的路径 |
| `eval <js> [--frame F]` | 逃生口；顶层 `return` 自动 IIFE 包裹 | JS 求值结果（字符串直接打印，其余 JSON） |
| `tabs` / `current-tab` / `new-tab [url]` / `switch-tab <t>` / `close-tab [t]` | 标签页 | `tabs` 是 `序号 targetId "title" "url"` 行；其余 JSON |
| `frames` / `cookies` | 只读 | JSON |
| `events` / `network` / `console` / `daemon-state` | 只读诊断（**依赖 daemon，仅 Unix**） | JSON |
| `dialog [accept\|dismiss]` | 处理原生对话框（**依赖 daemon**） | 未核实（按退出码判断成败） |
| `cdp <method> [json-params]` | 原始 CDP；`Browser.*`/`Target.*` 不 attach 到 page | JSON |
| `upload <selector> <path...>` | 本地文件上传给网页 | 未核实 |
| `pdf [path]` | 写文件 | 打印路径 |
| `download-path <path>` | 设置下载目录（`Browser.setDownloadBehavior`） | `ok` |
| `http-get <url>` | 纯 HTTP GET，不经浏览器 | 响应体 |

### 2.2 我们**不用**的命令（含原因）

| 命令 | 原因 |
|---|---|
| `reload` | **上游文档与代码不一致**：README 称"minimal lifecycle reset"，但 `src/main.rs` 里 `Cmd::Stop \| Cmd::Reload => lifecycle::stop()`，实际会杀掉 Chrome。需要刷新用 `eval 'location.reload()'` |
| `run <script>` / `--from-stdin` | 任意代码执行通道（按扩展名 exec bash/python3/node/bun）。Browser-Agent 本来没有 `run_bash`，不开这个洞 |
| `cloud-*` / `local-profiles` / `sync-profile` | 云端与 profile 同步属 P2+ |
| `init` | 会往工作区拷 skills 语料并写 `.browser-control/AGENTS.md`；我们只需要状态目录，由 Go 侧 `MkdirAll` 创建即可。P2 做 domain-skills 注入时再评估 |

### 2.3 ref 机制（最关键的契约）

`snapshot`/`inspect` 注入的 JS（`src/js.rs`）做三件事：

```js
const q = 'a,button,input,textarea,select,[role],[onclick],[tabindex],summary,[contenteditable=true]';
elements = [...document.querySelectorAll(q)].filter(可见).slice(0, 200)   // 上限 200 个
e.setAttribute('data-browser-control-ref', `e${i+1}`)                    // ref 落在 DOM 属性上
```

- 动作命令解析 `@e3` → `document.querySelector('[data-browser-control-ref="e3"]')`。
- 元素标签取 `innerText|value|placeholder|ariaLabel|title`，空白归一后**截断 100 字符**。
- `kind` 取值：`link`/`textbox`/`button`/`select`/`role属性值`/`element`。
- 三条推论，必须写进 prompt：
  1. ref 只存活于**当前 document**：导航或 SPA 重渲染后全部作废；
  2. 编号依赖快照顺序，重新 snapshot 会**重排**，跨快照引用不可复用；
  3. ref 找不到时上游报错形如 `not found: @e3`（`ACTION_JS` 抛错）——这是我们要做错误整形的信号。
- `click` 的实现是 `element_center` + 真实鼠标事件（**不是** `el.click()`）；
  `fill` 用原生 value setter + 派发 `input`/`change`（React 受控组件友好）；`type` 走 `Input.insertText`。
- target 三种风格：`@e3`（ref）、`#submit`（CSS）、`100,200`（坐标）。
- 上游**没有** `hover`、**没有** `back`。需要时用 `eval`：
  `eval 'document.querySelector("[data-browser-control-ref=\"e3\"]").dispatchEvent(new MouseEvent("mouseover",{bubbles:true}))'`、
  `eval 'history.back()'`。

### 2.4 状态目录与环境变量（集成的硬约束）

**所有状态写在进程 CWD 的相对路径 `.browser-control/`，上游没有任何环境变量可以改这个根路径**
（`src/workspace.rs` 里全是 `Path::new(".browser-control/...")`）：

```
.browser-control/current-tab                 # 当前 tab 指针，多命令共享
.browser-control/chrome.pid                  # launch 启动的 Chrome pid
.browser-control/chrome.port
.browser-control/chrome.user-data-dir
.browser-control/profile/                    # launch 时作为 --user-data-dir（无覆盖 flag）
.browser-control/daemon.sock                 # 事件守护进程 IPC（Unix socket only）
.browser-control/traces/<毫秒时间戳>/         # 任何命令失败自动落 error.txt + daemon 状态
```

→ **结论：Go 侧必须为每次调用设置 `cmd.Dir`**，否则用户仓库里会长出 `.browser-control/`。

环境变量（Go 侧按需注入，全部走 `cmd.Env`，不要改进程环境）：

| 变量 | 用途 | 我们的取值 |
|---|---|---|
| `BROWSER_CONTROL_CDP_URL` / `BROWSER_CONTROL_CDP_WS`（别名 `BU_CDP_URL`/`BU_CDP_WS`） | CDP 端点 | **必须显式注入**，见 2.5 第 2 条 |
| `BROWSER_CONTROL_TIMEOUT` | 上游内部 CDP 往返超时（秒），**默认仅 15** | `[browser] cdp_timeout`，且 ≥ 本次最大 wait + 5s |
| `BROWSER_CONTROL_CHROME` | Chrome 可执行文件 | `[browser] chrome_path`（空则上游自动探测） |
| `BROWSER_CONTROL_NO_TRACE` | 关闭失败 trace | 不设（我们要 trace 做自诊） |
| `BROWSER_CONTROL_DEBUG` | stderr 调试日志 | 仅 `YAGENT_BROWSER_DEBUG=1` 时设 |
| `BROWSER_CONTROL_ASSET_DIR` | skills 语料根 | P2 用 |

### 2.5 上游的 5 个坑（每条都有对策，实施时不要"顺手绕过"）

1. **CDP 端点兜底会串号**：`src/cdp.rs:386 ws_url()` 在无 env 时会探测 devtools active port，
   再**硬扫 9222/9223**。若不注入 env，可能连到用户自己的调试 Chrome 或另一个 yagent 任务的页面。
   → 对策：永远显式注入 `BROWSER_CONTROL_CDP_URL`；自启端口从 `[browser] cdp_port_base`（默认 9333）递增找空闲。
2. **内部超时默认 15s**：`wait load 30` 会在 15s 就失败。→ 对策：注入 `BROWSER_CONTROL_TIMEOUT`。
3. **`launch` 后 Chrome 是孤儿进程**：CLI spawn 完立即退出，子进程被 reparent 到 init；
   yagent 崩溃就留下 Chrome。`stop` 有归属校验（`pid_looks_owned`：命令行含 `--remote-debugging-port=`
   且 `--user-data-dir=` 匹配记录值），不会误杀用户 Chrome，可以安全复用。
   → 对策：`Yagent.Close()`（`internal/app/app.go:710`）里显式 stop + 启动时 reap 遗留 workspace。
4. **daemon 仅 Unix socket**（`src/daemon.rs:190` "daemon IPC currently requires Unix sockets"）。
   影响面：`events`/`network`/`console`/`daemon-state`/`dialog` 在 Windows 不可用；
   `wait networkidle` 有直连 CDP 回退（`src/main.rs` 里先试 daemon 再试 `c.wait_network_idle`），
   `cdp`/`eval` 也有直连回退。→ 对策：Windows 上把这些工具的 description 标注为不可用，不要静默失败。
5. **每次命令失败都写一个 trace 目录**（`write_failure_trace`）。→ 对策：按 `[browser] workspace_ttl` 清理。
6. **`launch` 不等端口就绪**（真实 E2E 复现）：spawn 完就返回，Chrome 调试端口要稍后才 bind，
   紧跟的第一条命令会报 `DevToolsActivePort not found`。
   → 对策：`Manager.makeReadyLocked` 拿端点后轮询 `status` 直到应答（上限 20s，300ms 间隔），就绪后才交回会话。
7. **本机安装的这版没有 `--version`**：clap 未声明 version，调用直接 `unexpected argument` 退出 2。
   → 对策：不做运行时版本校验（原设计的 `expectedCLIVersion` 已删除），契约以本文档为准。

### 2.6 输出样例（Go 侧解析用）

```
# launch
BROWSER_CONTROL_CDP_URL=http://127.0.0.1:9333

# doctor（退出码 0）
browser-control doctor
  [ok  ] cdp endpoint — ws://127.0.0.1:9333/devtools/browser/abc
  [ok  ] browser — Chrome/141.0.0.0
  [ -- ] launched chrome — none recorded (external/attached browser)
  [ok  ] daemon — fresh heartbeat
  [ok  ] workspace — .browser-control

# snapshot --json（pretty JSON）
{"url":"https://example.com","title":"Example Domain",
 "elements":[{"ref":"e1","kind":"link","text":"More information...","x":8,"y":200,"w":120,"h":16}]}

# snapshot（文本模式，output.rs:print_snapshot）
url: https://example.com
title: Example Domain

@e1 link "More information..."

# inspect --json 比 snapshot 多：readyState / query / text / headings，elements 多 href 字段
# tabs
0 1A2B3C "Example Domain" "https://example.com"

# 交互命令统一输出 ok；screenshot/pdf 输出写入的路径
```

---

## 3. 架构与包划分

```
Director ─delegate_browser─┐                    Coding ─delegate_browser─┐
                           ▼                                                │
  internal/agents/browser_agent.go    BrowserAgent.Run → RunAgentLoop（模板抄 devops.go）
                           │
                           ▼
  internal/tools/browser_operations.go   BrowserOperationsTool：params → argv → 渲染 → 错误整形
                           │                （14 个 Execute* 方法 → tools.Adapter）
                           ▼
  internal/browserctl/      Manager（能力探测 + Chrome 归属） / Session（工作区 + 串行 + env）
                           ▼
                    browser-control (Rust CLI) ──CDP──▶ Chrome
```

| 新增/改动文件 | 职责 | 预算 |
|---|---|---|
| `internal/browserctl/config.go` | `Config` 结构 + 从 `config.BrowserConfig` 构造 + 默认值兜底 | ≤120 行 |
| `internal/browserctl/binary.go` | 二进制解析：`binary_path` → `~/.yagent/bin/browser-control` → `$PATH`；`--version` 契约校验 | ≤100 行 |
| `internal/browserctl/manager.go` | `Manager`：能力探测、Chrome 进程级归属（launch/attach）、idle TTL sweeper、`Close()` | ≤250 行 |
| `internal/browserctl/session.go` | `Session`：工作区目录、`cmd.Dir`/`cmd.Env` 构造、同工作区串行、dialog 自动处理、输出预算 | ≤250 行 |
| `internal/browserctl/policy.go` | URL 守卫（allow/deny + `file://` 硬禁）、`Network.setBlockedURLs` 策略装载 | ≤150 行 |
| `internal/browserctl/render.go` | `inspect`/`snapshot`/`tabs`/`doctor` 的 JSON → 紧凑文本渲染（token 预算） | ≤200 行 |
| `internal/browserctl/*_test.go` | 表驱动单测（argv、env、Dir、渲染、守卫、预算、错误整形），不需要真 Chrome | — |
| `internal/tools/browser_operations.go` | `BrowserOperationsTool` + 声明式工具表 + 14 个 `Execute*` | ≤400 行 |
| `internal/tools/browser_operations_test.go` | 与 `tools.json` 的一致性测试 + 参数映射测试（用 fake Session） | — |
| `internal/agents/browser_agent.go` | 从 stub 改成真实现（`Run` + `lookupBrowserToolFunc`） | ≤200 行 |
| `internal/agents/browser.prompt.md` | System Prompt（新增，`//go:embed`） | — |
| `internal/agents/ports.go` | `BrowserControl = browserctl.Controller` 类型别名（契约见 4.1） | +6 行 |
| `internal/agents/tools.json` | 14 条 `browser_*` 声明 + 改回 `delegate_browser` 真描述 | +约 300 行 |
| `internal/config/config.go` | `BrowserConfig` struct + `Config.Browser` 字段 + 默认值填充 | +60 行 |
| `internal/config/default_config.toml`、`config/config.toml` | `[browser]` 段（注释模板） | +30 行 |
| `internal/app/app.go` | 构造 Manager、扩 `BrowserAgentParams`、`Close()` 里回收 Chrome | +40 行 |
| `internal/compression/priority.go` | 观察类工具进优先级 0 | +2 行 |
| `internal/agents/executor.go` | `nonParallelToolNames` 增补 | +3 行 |
| `internal/tools/workspace_guard.go` | `dangerousTools` / `pathParamNames` 增补 | +6 行 |
| `AGENTS.md` | §3 模块地图、§4 Agent 表、§5 工具表、§7 配置、§11 坑 | 同步更新 |

依赖方向（不得违反，`scripts/check_no_agents_import.sh` 只约束 `director/`，但风格一致）：
`agents → tools → browserctl → config`。`browserctl` **不得** import `internal/agents` 或 `internal/tools`。

---

## 4. 执行层 `internal/browserctl`

### 4.1 类型骨架

```go
// result.go
type Result struct {
    Stdout   string // 已按 MaxOutputBytes 截断
    Stderr   string // 已截断
    ExitCode int
    Trace    string // 失败时 .browser-control/traces/<最新>/error.txt 摘要（可空）
    Truncated bool
}

// manager.go
type Manager struct { /* cfg, bin, avail, reason, chrome 归属, sweeper */ }

func NewManager(cfg Config) *Manager
// Probe 解析二进制并校验版本；不启动 Chrome。返回 (可用, 不可用原因)。
func (m *Manager) Probe() (bool, string)
// Acquire 取得一个工作区会话；首次调用时按需 launch/attach Chrome。
// delegationID 用于工作区目录名，必须已做过路径安全化（见 4.7）。
func (m *Manager) Acquire(ctx context.Context, delegationID string) (*Session, error)
// Close 停掉自己启动的 Chrome 并停止 sweeper。必须在 Yagent.Close() 调用。
func (m *Manager) Close(ctx context.Context) error

// session.go
type Session struct { /* mgr, dir, mu, env, lastUse */ }

func (s *Session) Dir() string
// Run 执行一个子命令。同 Session 内串行；自动注入 Dir/Env/超时；
// 检测 dialog 并按策略处理；失败时附 trace 摘要。
func (s *Session) Run(ctx context.Context, command string, args ...string) (*Result, error)
// Release 标记工作区空闲（不杀 Chrome）。
func (s *Session) Release()
```

`ports.go` 里只放一个类型别名（`BrowserControl = browserctl.Controller`）：
契约本身由提供方 `browserctl` 声明（`interfaces.go` 的 `Controller` / `Runner`），
工具层直接使用 `browserctl.Runner`。两处各自声明接口会漂移，别名零成本且不会引入依赖环。

```go
// browserctl/interfaces.go
type Runner interface {
	Run(ctx context.Context, command string, args ...string) (*Result, error)
	Dir() string
	Release()
}
type Controller interface {
	Probe() (bool, string)
	Acquire(ctx context.Context, delegationID string) (Runner, error)
}
```

### 4.2 `Session.Run` 的六条硬规则

1. `cmd.Dir = s.dir`（**必须**，见 2.4）。目录不存在时先 `os.MkdirAll(dir, 0o700)`。
2. `cmd.Env = append(os.Environ(), 注入项...)`，注入项固定顺序（利于测试断言）：
   `BROWSER_CONTROL_CDP_URL`、`BROWSER_CONTROL_TIMEOUT`、`BROWSER_CONTROL_CHROME`（非空时）、
   `BROWSER_CONTROL_DEBUG`（仅调试时）。
3. `exec.CommandContext(ctx, bin, append([]string{command}, args...)...)`；
   **argv 由 Go 构造，绝不做 shell 拼接**（不经过 `sh -c`）。
4. 超时 = `min(cfg.CommandTimeout, executor 的 defaultToolTimeout 180s)`，且用 `context.WithTimeout`
   包一层（父 ctx 取消要能立刻传导）。超时后必须 `cmd.Process.Kill()` 兜底（`CommandContext` 只发 Kill，
   但要确认 Chrome 不会被连带杀掉——Chrome 不是我们的子进程，安全）。
5. 同 `Session` 内加 `sync.Mutex` 串行（`current-tab` 与 ref 是磁盘共享状态）。
6. stdout/stderr 分别限 `MaxOutputBytes`，超限截断并置 `Truncated=true`。

### 4.3 dialog 自动处理（在 `Run` 内）

`page-info` 在有原生 alert/confirm/prompt 时返回 `{"dialog": ...}` 而不是挂死，但 `dialog accept|dismiss`
本身是 daemon-backed、以 `BROWSER_CONTROL_TIMEOUT` 为界。规则：

- 每次 `Run` 返回后，若 stdout/stderr 里检测到 `"dialog"` 键（或命令因对话框失败），
  按 `[browser] dialog_policy` 处理：`accept`（默认）/`dismiss` → 自动执行 `dialog <action>` 后**重试原命令一次**；
  `ask` → 不自动处理，把情况返回给工具层，由 `ask_user_for_help` 决定。
- 自动处理必须在结果里留痕（追加一行 `[dialog auto-accepted]`），不能让 LLM 以为页面自己恢复了。

### 4.4 错误整形（在工具层做，规则集中一处）

| 上游信号 | 整形后的工具结果 |
|---|---|
| `not found: @eN` | 原文 + `\n提示：ref 已失效（页面已变化）。先调用 browser_snapshot 重新获取 ref，再重试。` |
| `element not found after Ns` | 原文 + `\n提示：元素可能在 iframe 内或尚未渲染。用 browser_inspect(query=...) 确认，或加 wait 参数。` |
| `no CDP endpoint; set BROWSER_CONTROL_CDP_WS/URL...` | 转成"浏览器会话已断开"，并触发一次重新 Acquire（限一次，避免死循环） |
| `Chrome not found; set BROWSER_CONTROL_CHROME` | 转成能力不可用文案 + 配置指引（`[browser] chrome_path`） |
| 退出码非 0 且有 trace | 原文 + `\ntrace: <error.txt 前 800 字符>` |
| `cdp timeout waiting for ...` | 原文 + 建议（缩小 wait、检查 networkidle） |

整形只做**追加提示**，不要吞掉上游原文（自诊信息比整洁更重要）。

### 4.5 URL 与安全策略（`policy.go`）

- `CheckURL(raw string) error`：
  - 硬禁 `file://`（无条件，配置不可放开——否则 Browser 变成绕过 WorkspaceGuard 读本地文件的通道）；
  - 硬禁 `chrome://`、`devtools://`、`about:`（`about:blank` 例外，仅内部使用）；
  - `denied_domains` 命中即拒（默认含 `*.local`、`localhost`、`127.0.0.1`、`192.168.0.0/16`、`10.0.0.0/8`、`169.254.0.0/16`）；
  - `allowed_domains` 非空时只允许命中项（支持 `*.example.com` 前缀通配）。
- 会话就绪后装一次浏览器侧策略（纵深防御，参数级检查拦不住页面内跳转）：
  `cdp Network.setBlockedURLs '{"urls":["file://*","*.local/*","169.254.169.254/*"]}'`。
  **未核实**该设置在跨 target/导航后是否持续生效——实施时必须用真实 Chrome 验证；
  若不持久，退化为"每次 mutating 动作后比对 `page-info` 的 host 是否越界，越界则中止并报告"。
- 每次 `open`/`new-tab` 之后，用返回的 `page-info` host 做一次越界复检（成本一次 CDP 往返）。

### 4.6 输出渲染（`render.go`）

一律请求 `--json`，Go 侧重渲染，目的是省 token 而不是好看：

- 丢弃 `x`/`y`/`w`/`h`（LLM 用不上，坐标点击极少）；
- 元素文本截断到 60 字符；
- 同 `kind` + 同 `text` + 同 `href` 的重复项去重（导航栏/列表页常见），保留首个 ref；
- 按 `MaxOutputBytes` 截断，尾部追加：`… 已截断（共 N 个元素）。用 browser_inspect(query="关键词", limit=20) 收窄。`
- 渲染格式对齐上游文本模式，便于 LLM 复用已有习惯：

```
url: https://example.com
title: Example Domain
readyState: complete

text:
<前 text_chars 字符>

headings:
- 一级标题
- 二级标题

elements:
@e1 link "More information..." -> https://www.iana.org/domains/example
@e2 textbox "Search"
```

### 4.7 生命周期

```
Yagent.Init
  └ browserctl.NewManager(cfg) → Probe()（只解析二进制 + --version，不启动 Chrome）
      不可用 → 记录 reason，delegate_browser 描述降级（见 7.3）

Acquire(delegationID)
  ├ 工作区 = [browser] workspace_root（默认 ~/.yagent/data/browser/{delegationID}）
  │   delegationID 必须清洗：只留 [A-Za-z0-9_-]，长度 ≤32，防路径穿越
  ├ Chrome 未就绪：
  │   · mode=attach 或 cfg.CDPURL 非空 → 直接用该 URL，不 launch
  │   · 否则 → 选空闲端口（从 cdp_port_base 递增，跳过 9222/9223）
  │            → 在**该工作区**执行 launch [--headless] --port N
  │            → 解析 stdout 的 BROWSER_CONTROL_CDP_URL，进程级缓存（Manager.chrome）
  │            → 装 Network.setBlockedURLs 策略
  ├ Chrome 已就绪 → 复用缓存的 CDP URL（各工作区共享同一 Chrome，tab 指针各自独立）
  └ 返回 Session

Release()            → 只标记 lastUse，不杀 Chrome（下个委派复用）
idle_ttl（默认 15m） → sweeper 触发 stop（在 owner 工作区执行，归属校验由上游保证）
Yagent.Close()       → Manager.Close()：stop + 停 sweeper
启动时 reap          → 扫描 workspace_root 下各 .browser-control/chrome.pid，
                       进程已死则删 pid 文件；进程活着且归属校验通过则 stop（清理上次崩溃遗留）
workspace_ttl（72h） → 清理过期工作区目录（含 traces、截图）
```

`max_chrome`（默认 1）：进程内 Chrome 上限；达到上限时新委派 attach 现有 Chrome，不再 launch。

---

## 5. 工具层

### 5.1 P0 工具规格（14 个）

命名统一 `browser_` 前缀。**全部不标 readOnly**（见 12.3）。

| 工具 | 上游命令 | 参数（类型/必填/默认） | dangerous |
|---|---|---|---|
| `browser_status` | `doctor` + `page-info` | — | 否 |
| `browser_open` | `open` | `url` string 必填 | 否（走 URL 守卫） |
| `browser_inspect` | `page-info` + `inspect --json` | `query` string 可选；`limit` int 可选默认 30（上限 80）；`text_chars` int 可选默认 2500（上限 8000） | 否 |
| `browser_snapshot` | `snapshot --json` | — | 否 |
| `browser_click` | `click` | `target` string 必填；`wait` int 可选**默认 3**；`button` enum left/right/middle 默认 left；`clicks` int 默认 1；`frame` string 可选 | 否 |
| `browser_fill` | `fill` | `target` string 必填；`value` string 必填；`wait` int 默认 3；`frame` 可选 | 否 |
| `browser_type` | `type` | `text` string 必填 | 否 |
| `browser_press` | `press` | `key` string 必填（如 `Enter`、`ctrl+a`、`cmd+shift+t`） | 否 |
| `browser_scroll` | `scroll` | `dy` int 默认 -300；`dx` int 默认 0；`at` string 可选 `x,y` | 否 |
| `browser_select` | `select` | `target` string 必填；`value` string 必填 | 否 |
| `browser_tabs` | `tabs`/`new-tab`/`switch-tab`/`close-tab` | `action` enum list/new/switch/close 必填；`target` string（switch/close 用，索引或 targetId）；`url` string（new 用，走 URL 守卫） | 否 |
| `browser_wait` | `wait`/`wait-element` | `mode` enum load/networkidle/element 默认 load；`seconds` int 默认 10（上限 60）；`selector` string（mode=element 必填）；`visible` bool 默认 false | 否 |
| `browser_screenshot` | `screenshot` | `path` string 可选（默认 `shot-<step>.png`，**相对会话工作区**）；`full` bool 默认 false | **是** |
| `browser_eval` | `eval` | `js` string 必填；`frame` string 可选 | **是** |

P1 追加：`browser_devtools`（`source` enum events/network/console，**仅 Unix**）、`browser_upload`
（`target` + `paths[]`，paths 必须过工作区边界检查）、`browser_pdf`、`browser_download_path`、`browser_dialog`。
P2 追加：`browser_cdp`（`method` 白名单 + `params`）。

### 5.2 `tools.json` 增补

保持既有风格（`{"name","description","parameters"}`，`parameters` 是 JSON Schema）。
description 用英文、≤200 字符、动词开头、**写清前置条件**（这是提高成功率最便宜的手段）。示例：

```json
{
  "name": "browser_click",
  "description": "Click an element on the current page. Target must be a @ref from a recent browser_snapshot, a CSS selector, or \"x,y\" coordinates. Refs expire after navigation or SPA re-render: re-run browser_snapshot first.",
  "parameters": {
    "type": "object",
    "properties": {
      "target": {"type": "string", "description": "@e3, #submit, or 100,200"},
      "wait": {"type": "integer", "description": "Seconds to poll for the element before clicking (default 3)"},
      "button": {"type": "string", "enum": ["left", "right", "middle"]},
      "clicks": {"type": "integer", "description": "2 for double click"},
      "frame": {"type": "string", "description": "Optional iframe target id or URL substring"}
    },
    "required": ["target"]
  }
}
```

同时改 `delegate_browser` 的描述（`internal/agents/tools.json:443`）——把"占位/永远返回未实现错误"
换成真实能力说明：何时该委派（需要 JS 渲染、登录态、交互、截图、验证前端行为），
何时不该（纯静态文档抓取优先用只读工具/`run_bash curl`），以及**任务描述必须自包含**
（目标 URL、要做什么、要返回什么证据）。

### 5.3 适配器构建（避开 AGENTS.md 坑 5）

`internal/tools/browser_operations.go` 用一张声明式表生成适配器，**不要**在 Agent 的
`lookupToolFunc` switch 里逐个 case（那条路径漏一个 case 就静默失效）：

```go
type browserToolSpec struct {
    name     string
    build    func(p map[string]interface{}) (command string, args []string, err error)
    render   func(r *browserctl.Result) (string, error) // nil = 原样返回 Stdout
    guardURL bool                                       // 需要 URL 守卫
}

var browserToolSpecs = []browserToolSpec{ /* 14 行 */ }

// BuildAdapters 按 tools.json 的声明生成 Adapter；tools.json 缺失该名字则跳过并返回错误列表。
func (t *BrowserOperationsTool) BuildAdapters(defs []tools.ToolDefinition, guard *tools.WorkspaceGuard) ([]*tools.Adapter, []error)
```

**必须**配套一条测试：`tools.json` 里每个 `browser_*` 名字都在 `browserToolSpecs` 里有对应行，
且每行的 `name` 都能在 `tools.json` 找到（双向断言）。这条测试就是防"声明了但没注册"的闸门。

`BrowserOperationsTool` 结构（对齐 `SystemOperationsTool` 的风格）：

```go
type BrowserOperationsTool struct {
    sessions SessionProvider // 窄接口：Acquire(ctx, delegationID) (BrowserSession, error)
    policy   *browserctl.Policy
    cfg      browserctl.Config
}
```

会话获取：每次 `Execute*` 调用时从 ctx 取 delegationID（`blackboard.BoardIDFrom(ctx)`，
为空则用 `Run` 开始时生成并塞进 ctx 的 ID），`Acquire` 拿到 Session 后 `defer Release()`。
**不要**把 Session 存在工具结构体里（Director 与 Coding 可并发委派）。

---

## 6. Agent 层

### 6.1 `browser_agent.go`（从 stub 改成真实现）

结构照 `devops.go` 抄（它有 guard + 压缩配置 + `FinalizeResult`，比 `chat.go` 更贴近需求）：

```go
//go:embed browser.prompt.md
var browserPrompt string

type BrowserAgent struct {
    BaseAgent
    promptFmt PromptFormatter
    Adapters  []*tools.Adapter
    maxSteps  int
    ecCfg     config.EnhancedCommanderConfig
    browser   BrowserControl // 用于能力探测（降级描述）与 delegationID 传递
}

type BrowserAgentParams struct {
    Formatter          PromptFormatter
    Publisher          EventBus
    Env                Env
    Flow               FlowToolSet
    Thinker            Thinker          // P1
    MicroAgent         MicroAgentRunner  // P1
    DeepThinker        DeepThinker       // P1
    Guard              *tools.WorkspaceGuard
    Browser            BrowserControl
    EnhancedCommanderCfg config.EnhancedCommanderConfig
    LLM                llm.Engine
    MaxSteps           int
}

func (a *BrowserAgent) Name() string { return "Browser-Agent" }

func (a *BrowserAgent) Run(ctx context.Context, input string) (AgentResult, error) {
    // 1) 能力探测：不可用则直接返回明确错误（不要进循环空转）
    // 2) 生成 delegationID 并写入 ctx（供工具层 Acquire）
    // 3) cfg := DefaultExecutorConfig()，逐字段赋值（照 devops.go:110-126）
    //    额外：cfg.UltimateThinkLink / cfg.UltimateKeepPlans 与 Coding 对齐（长循环需要 ultimate 压缩）
    // 4) RunAgentLoop → FinalizeResult(a.Name(), input, result)
}
```

`Run` 里的能力探测失败要返回**可行动**的错误文本（含 `reason` 与 `[browser] binary_path` 指引），
因为这段文本会经 `applyEnhancedCommander` 进 Director 上下文。

### 6.2 `browser.prompt.md` 必须包含的内容

1. 角色与边界：只做浏览器任务；**没有文件写权限、没有 shell**；产物只能落在会话工作区。
2. `<thought>` 块契约（与 Director/Coding 一致，格式见 AGENTS.md §10.2）：每轮恰好一个块、置于工具调用前、
   `goal`/`step`/`reason`/`plan` 四键、至多一条 `[>]`。**解析失败是静默的**，格式必须严格。
3. 核心循环（写成硬规则）：
   - 先 `browser_inspect`（要文本和候选链接）；**要动作前**才 `browser_snapshot` 取 ref；
   - 动作优先用 `@ref`；导航或 SPA 变化后 ref 全废，必须重新 snapshot；
   - 每次动作后验证（`browser_inspect` / `browser_status`），不凭记忆判断成功；
   - 卡住时依次尝试：加 `wait` → `browser_inspect(query=…)` → `browser_eval` → `browser_devtools`（console/network）；
   - 长页面分段滚动，不要一次拉全量。
4. 成本纪律：优先 `inspect` 带 `query`+`limit`；同一页面不要连续 snapshot 两次；不要重复截图。
5. 禁止事项：不猜密码、不提交凭据、不访问 `file://`、不下载执行文件；
   登录/验证码/支付确认 → `ask_user_for_help`（说明需要用户做什么）。
6. 结果契约（见 6.3）+ 证据要求。
7. 上游缺失能力的替代写法：`hover`/`back` 用 `browser_eval`（给出 2.3 里的两段代码）。

### 6.3 结果契约

`agent_exit` 的文本必须**自包含**（Coding 侧 `coding.go:195` 把 `result.Text` 原样返回给调用方）：

```
结论：完成 / 部分完成 / 失败（原因）
证据：最终 URL、页面标题、关键原文引用（≤10 行）
产物：截图/PDF 的绝对路径（若有）
未尽事项：需要用户介入的点（登录、验证码、人工确认）
```

Director 侧的 blackboard 便签与 artifact 落盘由既有代码处理（`director.go:402-403`），不要新写。

---

## 7. 接线清单（逐条，含容易漏的点）

### 7.1 `internal/app/app.go`

- `:420-424` 扩 `BrowserAgentParams`：补 `Formatter: &gctx`、`Env: env`、`Flow: gctx.FlowOps`、
  `Guard: guard`、`Browser: browserMgr`、`EnhancedCommanderCfg: ca.config.EnhancedCommander`；
  P1 再补 `Thinker`/`MicroAgent`/`DeepThinker`。
- Manager 构造放在 codeseek 解析之后（`:127-170` 附近），复用同一套"配置路径 → `~/.yagent/bin` → PATH"思路，
  但**不走 `embedbin`**（见 12.1）。构造后调 `Probe()`，结果记 `slog`。
- `:448` Coding 的 `Browser: browserAgent` 与 `:474` Director 的 `BrowserAgent` 不用改。
- `Close()`（`:710`）里加 `browserMgr.Close(context.Background())`，放在 consolidationWorker 停止之后。
- `Manager` 需要挂到 `Yagent` 结构体字段上（`sync.Once` 内构造，`Close` 里使用）。

### 7.2 注册表增补（漏一个就静默失效或行为错误）

| 文件 | 改动 |
|---|---|
| `internal/tools/adapter.go:76` `knownReadOnlyTools` | **不加任何 browser 工具**（见 12.3） |
| `internal/tools/workspace_guard.go:42` `dangerousTools` | P0 只加 `browser_eval`，并在 `Check` 的 switch 里加 `case "browser_eval"` → **按 JS 敏感特征判定**（cookie/存储/网络请求/表单提交/动态执行才弹窗）；良性逃生口（`location.reload()`、`history.back()`、派发 mouseover）不打断流程。P1 再加 `browser_pdf`、`browser_upload`、`browser_download_path` |
| `internal/tools/workspace_guard.go:51` `pathParamNames` | **`browser_screenshot` 不进这张表**：截图落在 `~/.yagent/data/browser/{delegation}` 内，本就在项目工作区之外，登记它等于每次截图都问用户一次；路径收敛由工具层 `prepareArtifactPath` 硬约束（拒绝越界与 `..`）。P1 给 `browser_upload` 的 `paths` 登记（那是"本地文件外传"面，需要工作区边界检查） |
| `internal/agents/executor.go:74` `nonParallelToolNames` | 加 `browser_eval`、`browser_open`、`browser_tabs`（会改当前 tab 的都不能并行） |
| `internal/compression/priority.go:9` 优先级 0 | 加 `browser_snapshot`、`browser_inspect`、`browser_eval`、`browser_status`（陈旧观察结果最该先丢；现在它们落在 default=1，会先截 `read_file`，方向是反的。原计划的 `browser_devtools`/`browser_text` 两个名字并不存在，已按实际 14 个工具名校正） |
| `internal/agents/tools.json` | 14 条 `browser_*` + 改 `delegate_browser` 描述 |
| `internal/config/config.go:195` 附近 | `Config` 加 `Browser BrowserConfig \`toml:"browser"\``；新增 `BrowserConfig` struct；默认值填充函数里补零值兜底（照 `:405-409` 的 `BrowserMaxSteps` 写法） |

### 7.3 `delegate_browser` 描述按能力动态降级

两处构造点都要改：

- Director：`internal/agents/director.go:387-404`，description 目前是硬编码字面量。
  改成 `browserDelegateDescription(available bool, reason string) string`（放在 `director.go` 或 `browser_agent.go`，
  两处共用；注意 `director/` 子包不得 import `internal/agents`，所以放 `internal/agents` 根包）。
- Coding：`internal/agents/coding.go:176-200` 的 `newBrowserDelegateAdapter` 用的是 `def.Description`，
  不可用时替换为同一函数生成的降级文案。
- 降级文案要点：明确"浏览器能力当前不可用：<reason>"、给出修复动作（安装 `browser-control` /
  配 `[browser] binary_path` / 装 Chrome）、并要求改用其他手段或如实告知用户。
- 同时 `browser_agent.go` 的 `Run` 在不可用时**立即返回错误**，不进循环。

### 7.4 需要核对的枚举点（我没逐一验证，实施时确认）

- `internal/tui/styles.go`：Agent 前景色/标签枚举是否需要为 `Browser-Agent` 补条目。
- `internal/tui/tui_update_commands.go:135`：`validAgents` 已含 `browser`，确认无需改动。
- `internal/tui/i18n.go`：若新增面向用户的浏览器文案，中英文都要补。
- `internal/logging`：委派日志目录按 agent 名分（`delegate-*`），确认 `Browser-Agent` 名字不会产生非法路径。
- `internal/llm/router.go:56`：`browser` 基线当前是 `TierBalanced`。建议改 `TierHigh`
  （浏览器任务大量是长页面理解），但这是**可调项**，改了要在 `docs/config-guide.md` 同步。

---

## 8. 配置

`internal/config/config.go` 新增：

```go
type BrowserConfig struct {
    Enabled        bool          `toml:"enabled"`
    BinaryPath     string        `toml:"binary_path"`
    ChromePath     string        `toml:"chrome_path"`
    Mode           string        `toml:"mode"`            // headless | headful | attach
    CDPURL         string        `toml:"cdp_url"`         // attach 用
    CDPPortBase    int           `toml:"cdp_port_base"`
    CDPTimeout     time.Duration `toml:"cdp_timeout"`
    CommandTimeout time.Duration `toml:"command_timeout"`
    IdleTTL        time.Duration `toml:"idle_ttl"`
    MaxChrome      int           `toml:"max_chrome"`
    WorkspaceRoot  string        `toml:"workspace_root"`
    WorkspaceTTL   time.Duration `toml:"workspace_ttl"`
    MaxOutputBytes int           `toml:"max_output_bytes"`
    DialogPolicy   string        `toml:"dialog_policy"`   // accept | dismiss | ask
    AllowedDomains []string      `toml:"allowed_domains"`
    DeniedDomains  []string      `toml:"denied_domains"`
    ProfileDir     string        `toml:"profile_dir"`
    EnableSkills   bool          `toml:"enable_skills"`
}
```

`default_config.toml` / `config/config.toml` 里加注释模板段（放在 `[codeseek]` 之后，风格对齐既有段）：

```toml
[browser]
enabled = true
binary_path = ""          # 空 → ~/.yagent/bin/browser-control → $PATH；都找不到则浏览器能力关闭
chrome_path = ""          # 空 → 由 browser-control 自动探测 Chrome/Chromium
mode = "headless"         # headless | headful | attach（attach 复用已登录的 Chrome）
cdp_url = ""              # mode=attach 时必填，如 http://127.0.0.1:9222
cdp_port_base = 9333      # 自启端口起点，递增找空闲（刻意避开 9222/9223，上游会兜底硬扫这两个端口）
cdp_timeout = "65s"       # → BROWSER_CONTROL_TIMEOUT；须覆盖最长 wait（60s），上游出厂默认仅 15s
command_timeout = "75s"   # 单次调用上限；须 > cdp_timeout，否则外层先杀进程，模型看不到上游失败原因
idle_ttl = "15m"          # 空闲多久后关掉自己启动的 Chrome
max_chrome = 1
workspace_root = ""       # 空 → ~/.yagent/data/browser/{delegation}
workspace_ttl = "72h"
max_output_bytes = 24000
dialog_policy = "accept"  # accept | dismiss | ask
allowed_domains = []      # 空 = 不限公网
denied_domains = ["file://", "*.local", "localhost", "127.0.0.1", "192.168.0.0/16", "10.0.0.0/8", "169.254.0.0/16"]
profile_dir = ""          # 需要跨任务复用登录态时指定（会 symlink 进工作区）
enable_skills = false     # P2：注入 domain-skills 语料
```

注意：`persist.go` 是**文本级精确改段**（`:21`/`:107`/`:187`），新增表只要在 struct 与模板里存在即可安全写回，
**不要**新增整文件重编码的持久化函数（会丢用户注释）。

另：老用户的 `~/.yagent/config/config.toml` 里可能已经有 `[browser]` 段——那是被删掉的旧浏览器栈留下的
（`headless = true` / `browser_path = ...`），新 schema 不认识这些键，TOML 会把 `enabled` 读成 `false`
而静默关掉新能力（2026-10-13 的真实故障）。实现上要求段内**至少有一个被新 schema 认识的键**才算"用户显式配置"
（`browserSectionConfigured`），只剩失效键的段落按整表缺省处理；失效键用 `slog.Warn` 点名提示改写。
表存在时逐项兜零值（`applyDefaults`），并对两个超时做下限兜底（`MinBrowserCDPTimeout` / `MinBrowserCommandTimeout`）。

---

## 9. 上下文与成本治理

1. 观察类工具结果进压缩优先级 0（见 7.2）——这是长循环不炸上下文的关键。
2. 三档压缩全部对 Browser 生效（`[enhanced_commander]` 开启后）：
   Rule 截陈旧观察 → Emergency 用 LLM 摘要历史 → Ultimate 用本次委派的 thinklink 存储重建
   （store 在 `Run()` 内创建，跨委派不复用）。压缩后页面状态与 `@ref` 全丢，
   prompt 的 Context Budget 一节负责把 Agent 拉回"先 `browser_status` 再 `browser_snapshot`"。
3. prompt 里把 `inspect` 定为默认观察手段，`snapshot` 只在"马上要动作"时用。
4. 渲染层做去重 + 60 字符截断 + 预算截断（见 4.6）。
5. P1 给 `micro_agent`：长正文摘要放隔离子上下文，不进主循环。
6. 无进展检测：在 `OnStepEnd` 里记录最近 N 步的 `url|title`；连续 5 步不变且无成功动作 →
   注入一条 nudge（照 Director 的强制委派 nudge 模式）；连续 10 步 → 直接结束并报失败。
7. `MaxSteps` 沿用 `[agent] browser_max_steps`（默认 200，`internal/config/defaults.go:23`）。
   不要改默认值；真实长流程需要它。

---

## 10. 测试计划

### 10.1 单元测试（必须，无需 Chrome）

| 测试 | 断言 |
|---|---|
| `browserctl/session_test.go` | `cmd.Dir` == 工作区；`cmd.Env` 含 `BROWSER_CONTROL_CDP_URL`/`_TIMEOUT`；argv 无 shell 元字符注入；超时传导；输出截断置 `Truncated` |
| `browserctl/binary_test.go` | 三级解析顺序；`--version` 不匹配只 warn |
| `browserctl/policy_test.go` | `file://` 无条件拒；deny/allow 通配；内网段；`about:blank` 例外 |
| `browserctl/render_test.go` | JSON → 紧凑文本；坐标被丢弃；重复项去重；预算截断带提示 |
| `browserctl/manager_test.go` | 端口选择跳过 9222/9223；delegationID 清洗（`../` 注入被拒）；`max_chrome` 生效 |
| `tools/browser_operations_test.go` | **双向一致性**：`tools.json` 的 `browser_*` ↔ `browserToolSpecs`；每个 spec 的 argv 构造（表驱动，含默认值注入 `--wait 3`）；错误整形（`not found: @e3` → 追加提示） |
| `tools/workspace_guard_test.go` | `browser_screenshot`/`browser_eval` 进 dangerous；`:311` 的 `nonDangerousTools` 列表相应调整 |
| `compression/context_compressor_test.go` | 新增工具的 `toolTruncationPriority` == 0 |
| `agents/browser_agent_test.go` | 能力不可用时 `Run` 立即返回含 reason 的错误；Adapters 数量与 tools.json 一致 |

fake 手段：`SessionProvider` 用内存实现（记录 argv/env/dir，返回预设 stdout），**不要**为了测试去 spawn 真进程。

### 10.2 集成测试（门控，本地手动跑）

`internal/tools/browser_operations_e2e_test.go`（已实现）。`YAGENT_BROWSER_E2E != "1"` 时 `t.Skip`，
并在 `Probe()` 失败时 skip。用例刻意从**工具层**打进真实 Chrome，覆盖整条链路
（tools.json 声明 → Adapter → argv 映射 → Session/env/Dir → CLI → CDP）：

```bash
YAGENT_BROWSER_E2E=1 go test ./internal/tools/ -run TestE2EBrowserStack -v -timeout 300s
```

`browser_status → browser_open(example.com) → browser_inspect(query) → browser_snapshot →
browser_click(@ref) → 断言落到 iana.org → browser_screenshot → 断言 `.browser-control` 只在会话工作区`，
收尾由 `mgr.Close()` 关掉 Chrome（断言无孤儿）。

### 10.3 必须断言的不变量

- **仓库根目录不出现 `.browser-control/`**（集成测试结束后断言；这是 2.4 那条硬约束的回归闸门）。
- 会话结束不留孤儿 Chrome（`stop` 后 `chrome.pid` 被上游删除）。
- `browser_*` 工具一个都没有 `IsReadOnly() == true`。

### 10.4 验证命令

```bash
go build -o yagent .
go vet ./...
go test ./internal/browserctl/... ./internal/tools/... ./internal/agents/... ./internal/compression/... -count=1 -timeout 300s
bash scripts/check_file_sizes.sh
bash scripts/check_no_agents_import.sh
```

---

## 11. 分期任务

### P0：最小可用闭环

任务：
1. `internal/browserctl`：config / binary / manager / session / policy / render（含单测）。
2. `internal/tools/browser_operations.go`：14 个工具 + 声明式表 + 一致性测试。
3. `tools.json`：14 条声明 + `delegate_browser` 描述改回真描述。
4. `browser_agent.go` 真实现 + `browser.prompt.md`。
5. `ports.go` 两个窄接口；`app.go` 构造/注入/`Close` 回收。
6. `[browser]` 配置 + 模板 + 默认值。
7. 注册表增补：`dangerousTools`（browser_eval，按 JS 敏感特征）、`nonParallelToolNames`、`toolTruncationPriority`（见 7.2 的取舍说明）。
8. 能力探测降级（7.3）。
9. `AGENTS.md` 同步（§3 模块地图、§4 Agent 表权限列、§5 工具表、§7 配置表清单、§11 坑）。

验收：
- `go build` / `go vet` / 上述包 `go test` 全绿；`scripts/check_no_agents_import.sh` 通过。
  （`scripts/check_file_sizes.sh` 有一处**既存**违规：`internal/agents/director/planner_run.go` 655 行，
  与浏览器改动无关，别顺手重构它。）
- 无 Chrome/无二进制的环境下：`delegate_browser` 描述显示不可用原因，`BrowserAgent.Run` 立即返回可行动错误
  （`browser_agent_test.go` 覆盖）。
- 有环境时：`YAGENT_BROWSER_E2E=1 go test ./internal/tools/ -run TestE2EBrowserStack`（见 10.2）。
  2026-10-13 已在 Chrome/155 + `~/.cargo/bin/browser-control` 上跑通：
  open→inspect→snapshot(`@e1 link "Learn more"`)→click→落到 `https://www.iana.org/help/example-domains`→截图落会话工作区→Chrome 干净退出。

### P1：可用性与可诊断性

P0 已顺带完成：traces 摘要回传、workspace TTL 清理（含 traces 目录）、启动时 reap 遗留 Chrome、E2E 门控测试。
P1 剩下这些：

`browser_devtools`（`events`/`network`/`console`，Unix 门控）、`browser_upload`/`browser_pdf`/
`browser_download_path`/`browser_dialog`（dialog 的**自动**处理已在 P0 实现，这里只是给 `ask` 策略留出人工入口）、
无进展检测（`OnStepEnd` 里比对 url+title）、`thinking`/`micro_agent`/`deepthinking` 接入、
Windows 下不可用工具的 description 标注、`browser_upload` 的 `pathParamNames` 登记。

验收：一个需要 5+ 步表单交互的真实站点任务跑通；失败时工具结果里能看到 trace 摘要。

### P2：登录态与语料

`profile_dir` 的 symlink 共享与 `mode=attach` 已在 P0 落地（`linkProfileLocked` / `chromeProc.attached`，
且 attach 模式下 `Close()` 不会关用户的浏览器）。P2 做：domain-skills 注入
（上游 `skills/domain-skills/<site>/<task>.md` ~250 篇，MIT；按当前 host 匹配注入 1-2 篇，预算 2-4KB；
用 `BROWSER_CONTROL_ASSET_DIR` 指向我们解出的目录）、云 provider、TUI 侧页面状态/截图路径展示。

### P3：硬化

`browser_cdp`（method 白名单）、WebVoyager 式回归基准（上游配套 `omxyz/webvoyager`）、
多标签只读观察并行（需先解决 `current-tab` 共享状态）。

---

## 12. 禁止事项

1. ~~**不要把 browser-control 二进制内嵌进 `dist/bin` / `go:embed`。**
   该能力的前提是机器上有 Chrome，内嵌 CLI 换不来开箱可用，却要往已 257MB 的 embed 里再加 4 个平台的二进制。
   走"配置路径 → `~/.yagent/bin` → `$PATH`"三级解析 + 能力降级。~~
   **决策变更（2026-10-16）**：v0.1.1 起 browser-control 随 release 构建内嵌（4 平台二进制 → `dist/bin` →
   启动时解出 `~/.yagent/bin`），换取开箱可用，原体积顾虑由维护者拍板接受；"配置路径 → `~/.yagent/bin` →
   `$PATH`"三级解析与能力降级保持不变（运行时前提仍是机器上有 Chrome）。
2. **不要暴露 `run` / `--from-stdin` / `reload`**（前两者是任意代码执行；`reload` 在上游等于 `stop`）。
3. **不要把任何 `browser_*` 加进 `knownReadOnlyTools`**：`current-tab` 与 ref 是磁盘共享状态，
   并行观察会读到错页；串行的代价远小于错页的代价。
4. **不要在项目工作目录执行 CLI**：必须 `cmd.Dir = 会话工作区`。
5. **不要用 shell 拼接命令**：一律 `exec.CommandContext` + argv 数组。
6. **不要让 Browser-Agent 拿到 `run_bash` 或文件写工具**：这是它的权限边界（只读 + 浏览器 + 会话工作区产物）。
7. **不要代填密码/凭据**，不要访问 `file://`，不要把 cookies 原文写进日志或 rollout。
8. **不要改 `internal/protocol/agent_events.go`**（DO NOT EDIT）；若确需新事件类型，改
   `protocol/agent-events.yaml` 再重新生成——P0/P1 不需要新事件，复用 `tool_call_start`/`tool_call_result`。
9. **不要顺手 `go mod tidy`**（`go-rod/rod` 是未决项）。
10. **不要相信上游 README 的 `reload` 描述**（见 2.2）。

---

## 13. 未决项（实施时按默认走，需要改动先问）

| 项 | 默认 | 说明 |
|---|---|---|
| 默认 `mode` | `headless` | 登录场景由 `ask_user_for_help` 提示用户切 `headful` 或用 `attach` |
| `[routing] baselines.browser` | 建议 `high` | 读长页面为主；改动需同步 `docs/config-guide.md` |
| 是否新增轻量 `fetch_url` 工具分流静态抓取 | 不做 | 会显著降低 Browser 调用量，但属范围外新增能力，需单独拍板 |
| domain-skills 语料是否随 yagent 分发 | P2 再定 | 体积与维护漂移 vs 成功率收益 |
| Windows 支持程度 | P0 不保证 | daemon 相关工具（devtools/dialog）在 Windows 不可用，description 需标注 |
