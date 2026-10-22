# 外部能力接入：编码实施文档（Skills + 通用 MCP）

> **读者是执行改动的 coding agent。** 设计依据与"为什么"全部在
> `docs/skills-and-mcp-design.md`（下称**设计稿**），本文只讲**怎么改、改到什么程度算完、怎么证明它完了**。
> 冲突时以设计稿的判据为准，以本文的验收命令为准。
>
> 文中 `file:line` 一律以提交 `21c5f9d` 为基准；改动会让行号漂移，**按符号定位，不要按行号盲改**。

---

## 0. 全局纪律（每个阶段都适用，违反即返工）

### 0.1 依赖方向（编译期必须成立）

```
internal/toolspec   ← 叶子，只准标准库（scripts/check_toolspec_leaf.sh）
      ▲
internal/mcp        ← 只准标准库 + internal/supervise（传输层，不认识 toolspec/tools）
      ▲
internal/tools      ← 可以 import mcp + toolspec（动态规格的构造点在这里）
      ▲
internal/agents     ← 可以 import tools + toolspec + ports 窄接口
      ▲
internal/app        ← 唯一装配点（Manager、Registry、Surface 的连线都在这）
      ▲
internal/tui        ← 可以 import app；**不要** import internal/agents（文档约定，见 §7.1）
```

- `internal/toolspec` 里**不许** import `internal/mcp`：需要外部能力就声明窄接口（`deps.go:8-11` 那段注释就是这个理由）。
- `internal/mcp` 里**不许** import `toolspec`/`tools`：Manager 只吐 `DiscoveredTool` 这种纯数据。
- 新增跨子系统依赖一律走 `internal/agents/ports.go` 的窄接口，**不要往结构体上加字段**（该文件顶部注释）。

### 0.2 硬禁令

| 禁令 | 判据 |
|---|---|
| 不许裸 `go` | `scripts/check_no_bare_go.sh`，无 PENDING 名单。goroutine 只能 `supervise.From(ctx).Go("名字", fn)` 或 `supervise.New(ctx, 域)` |
| 不许自己写 tmp+rename | 只能用 `internal/util/atomic.go` 的 `util.AtomicWrite`（`check_no_inline_atomic_write.sh`） |
| 不许按工具名写 `switch`/`case` 分派 | `check_no_manual_lookup.sh` 的 FORBIDDEN 名单必须为零。并发/授权/截断/展示全部从 `Spec` 派生 |
| 不许锁内做 IO | 装配账目、发事件、写文件都在 Unlock 之后（根 `AGENTS.md` §9.2 C6-d） |
| 不许 `time.Sleep` 等退避 | 新增计时器必须先加 seam（照 `agents.stepRetryBackoffBase`、`datamanager.taskFlushDebounce`） |
| 不许动 `internal/mcp/client.go` 的传输层 | 本设计对它的改动**只有**新增 `ListTools` 方法与 `Instructions` 字段；`sendJSON`/`doRequest`/`readLoop`/`IsAlive` 一行不改 |
| 不许构建 codeseek | 测试**不得**依赖 codeseek 二进制。要跑 `build.sh` 时加 `SKIP_RUST=true` |
| 注释默认不写 | 只写"为什么"，一行以内。TOML 模板注释用**英文**（既有模板通篇英文）；Go 注释跟随周围风格（本仓为中文） |

### 0.3 每阶段收口命令（缺一不可）

```bash
go build ./... && go vet ./...                       # 必须干净
go test ./internal/<改到的包>/... -count=1            # 快循环
bash scripts/check_all.sh                            # 九把守卫 + 全量单测
```

涉及并发/goroutine/事件总线的阶段（P2、P4、P6）额外跑：

```bash
RACE=true bash scripts/check_all.sh
```

### 0.4 变异验证怎么做（本文每个"变异"条目都要真做一遍）

**不许在未提交的工作树上 `git checkout`/`git restore` 还原文件**（会连带丢掉同批未提交的改动）。
正确做法二选一：

1. 把要变异的文件复制到 `/tmp/mut/` 备份 → 改 → 跑测试确认变红 → 从 `/tmp/mut/` 拷回；
2. 或者先提交当前阶段，再用 `git revert --no-commit` 做变异，验证完 `git reset --hard` 到刚才那个提交。

**声称"这条闸门能失败"之前必须真做过变异。**没变红的闸门等于没有闸门（根 `AGENTS.md` §8.3）。

### 0.5 提交切分

一个阶段一个提交，英文 Conventional Commits，主题 ≤ 70 字符：

```
fix(toolspec): call registry self-check at startup and correct stale baseline
feat(toolspec): add inline schema, external effects and use_skill declaration
feat(mcp): add tools/list discovery, instructions and a multi-server manager
feat(tools): build dynamic specs for external MCP tools
feat(app): wire MCP manager and skill registry into every tool surface
feat(skills): directory layout, front-matter, merged scopes and use_skill
feat(agents): inject skill catalog and MCP status into system prompts
feat(tui): /skills and /mcp dialogs, split completion, typed slash skills
docs: sync AGENTS.md and config guide with external capabilities
```

**不要**在实现提交里夹带无关重排（`gofmt -l` 报出的存量漂移用 `gofmt -d` 区分，别顺手全量重排）。

---

## P0 — 漂移修复 + 启动自检落地

**前置**：无。**可独立提交、独立回滚。**

### 改动

| 文件 | 改什么 |
|---|---|
| `internal/toolspec/AGENTS.md` | "条数基线**硬写 51**" → **49**（P1 之后会变 50，那时再改一次）。同段引用的 `registry_test.go:412-421` 校正为实际行号 |
| `internal/app/app.go` | `Init` 里加一次注册表自检（见下），位置在装配 Surface 之前、`slog` 已可用之后 |
| `internal/toolspec/spec.go` | `Validate()` 的注释"装配期调用"这句从**假话变成真话**（不要改注释了事） |

### 自检的形状

```go
// internal/app/app.go（Init 内，只此一处）
if problems := toolspec.Validate(); len(problems) > 0 {
    slog.Error("tool registry self-check failed", "count", len(problems), "problems", problems)
}
```

**不要 `os.Exit`**：静态注册表的问题是构建期 bug，`registry_gate_test.go:24-33` 已经拦住；
运行期退出会把"配置坏了"和"代码坏了"混成同一个症状（对照 `config` 那条 stderr 三行人话的既有决定）。

### 验收

```bash
go test ./internal/toolspec/... ./internal/app/... -count=1
grep -n "toolspec.Validate" internal/app/app.go      # 必须有且只有一处生产调用
```

- 新增测试 `internal/app/assembly_test.go:TestInitRunsRegistrySelfCheck`：用一个会注入问题的替身不好造，
  所以断言**调用点存在且不影响装配**即可 —— 具体做法：`Init` 后 `slog` 里没有 registry 错误、
  且 `toolspec.Validate()` 返回空（说明生产注册表干净）。**不要**写一条恒真断言（`check_test_hygiene.sh` 会拦"没有失败能力"的用例）。
- 变异：把 `internal/toolspec/schemas.json` 里任一工具的 `description` 删掉 → `TestSchemasLoadAndDescribeEveryEntry` 必须红；
  把某个 `Spec.Build` 置 nil → 启动自检必须打出那条 problem（用一条读日志的测试断言，或直接 `go run` 观察后还原）。

---

## P1 — `toolspec`：`Spec.Inline` + 三个 Effect 位 + `ValidateDynamic` + `use_skill` 声明

**前置**：P0。**这一阶段结束时，工具面 golden 只允许多出 `use_skill` 一行。**

### 1.1 `Spec.Inline`（`internal/toolspec/spec.go`）

```go
// Inline 是动态注册（外部 MCP 工具）自带的模型接口声明。
// 静态工具一律留空 —— 它们的 description/parameters 只在 schemas.json 里。
Inline *Schema
```

`internal/toolspec/schemas.go` 两个取值函数改成三级优先：

```go
func (s *Spec) Description(deps *Deps) string   // Describe 钩子 > Inline.Description > GetSchema > s.Name
func (s *Spec) Parameters() map[string]interface{} // Inline.Parameters > GetSchema > nil
```

**静态路径必须逐字不变**（`Inline == nil`），所以 `TestSpecDescriptionFallbackChain`（`registry_test.go:472`）
与 `TestSpecParametersNilWithoutSchema`（`:502`）不改一行就该继续绿。**如果它们红了，说明你把优先级写反了。**

### 1.2 三个 Effect 位（`internal/toolspec/effect.go`）

```go
const (
    EffectExternalRead  Effect = 1 << 26 // 外部 MCP server 的只读工具
    EffectExternalWrite Effect = 1 << 27 // 外部 MCP server 的写类工具
    EffectSkill         Effect = 1 << 28 // use_skill
)
```

- `effectNames` 表**追加**三项（`external_read` / `external_write` / `skill`）。追加在末尾：
  `Names()` 按表序输出，插在中间会让既有 golden 的 `EffectSummary` 全部变化。
- `writeEffects` 与 `authEffects` 各加 `EffectExternalWrite`（`effect.go:51-62`）。
  这样"`Mutates=false` 的写工具"和"不过授权的危险工具"两条既有校验自动覆盖外部工具。
- `flowEffects` **不加** `EffectSkill`：`use_skill` 是 Hot 但不是流程控制工具，
  加进去会让"流程控制类不许 deferred"那条校验的语义变浑（`spec.go:200-203`）。

### 1.3 `ValidateDynamic`（`internal/toolspec/spec.go`）

```go
// ValidateDynamic 自检一批运行期动态规格（外部 MCP 工具）。
// 与 Validate() 分开：动态规格不在 schemas.json 里，1:1 那条规则对它们不适用。
func ValidateDynamic(specs []*Spec) []string
```

每条规则**都要有能触发它的夹具**（`TestValidate_ReportsEachRule` 那条纪律，根 `AGENTS.md` §11 坑 15）：

1. `Name` 为空 / 不以 `mcp__` 开头 / 含 `[^a-zA-Z0-9_-]` / 长度 > 64。
2. `Effect` 为零值。
3. `Build == nil`。
4. `Inline == nil` 或 `Inline.Parameters == nil`（外部工具没有 schemas.json 兜底，缺 schema 等于声明一个无参工具）。
5. `Inline.Description` 为空。
6. `Frequency == Cold` 而 `Brief` 为空；`Brief` 超 `maxBriefRunes`（40）。
7. `Effect.Has(writeEffects)` 而 `Mutates == false`；`Effect.Has(authEffects)` 而 `NeedsAuth == false`。
8. `Trunc == TruncProtected && Mutates`（与静态同规则）。
9. 与静态注册表同名（`Get(name)` 命中）⇒ 报"动态规格不得与静态工具同名"。

### 1.4 `Deps` 两个新字段（`internal/toolspec/deps.go`）

```go
// ExternalSpecs 是运行期发现的外部工具规格（MCP）。静态注册表之外的一条通道：
// 它们不进 schemas.json、不进 golden、不进 Meta 的工具池（设计稿 §4.1）。
ExternalSpecs []*Spec

// Skills 是技能读通道。nil = 没有技能（use_skill 会解释为什么没有，而不是从工具面消失）。
Skills SkillSource
```

```go
// SkillSource 是 Agent 侧对技能注册表的全部需求。由 *skills.Registry 天然实现。
type SkillSource interface {
    Catalog(maxTokens int) string
    Body(name string, offset, limit int) (SkillBody, error)
}

type SkillBody struct {
    Name         string
    Source       string   // "project" | "user" | "extra:<dir>"
    Path         string
    Description  string
    AllowedTools []string // 咨询性，不强制（设计稿 §6.5）
    Arguments    []string
    Text         string   // 已按 offset/limit 分页
    Truncated    bool
    NextOffset   int
}
```

`Profile.testDeps()`（`profile.go:35-65`）**必须**绑 `Skills: probeSkills{}`：
探针少绑一次的结果是 `use_skill` 从六个画像里静默消失、golden 一动不动（根 `AGENTS.md` §11 坑 39）。
`TestProbeDepsIsCapabilityComplete`（`profile_test.go:420`）加一条断言：`deps.Skills != nil`。

### 1.5 画像矩阵（`internal/toolspec/profiles.go`）

| 画像 | Grant 追加 |
|---|---|
| `ProfileDirector` | `EffectExternalRead \| EffectSkill` |
| `ProfileCoding` | `EffectExternalRead \| EffectExternalWrite \| EffectSkill` |
| `ProfileRepo` | `EffectExternalRead \| EffectSkill` |
| `ProfileChat` | `EffectSkill` |
| `ProfileDevOps` | `EffectExternalRead` |
| `ProfileBrowser` | 无 |

`ProfileDirector.Palette` **一个位都不加**（Custom Agent 拿不到外部能力，理由照抄 `profiles.go:31-33` 那条注释）。
每个画像的追加处写一行注释说明为什么（尤其 devops 为什么没有 `EffectSkill`、chat 为什么没有 `EffectExternalRead`）。

### 1.6 `use_skill` 声明

`schemas.json` 加第 50 条：

```json
{
  "name": "use_skill",
  "description": "Fetch the full body of a user-authored skill listed in the 'Skills' section of your system prompt, then follow it. Skills are reusable procedures the user wrote for this project. Returns the front-matter facts (source path, expected tools, declared arguments) plus the body; long bodies are paginated — call again with the returned offset to continue. If no skills are configured the call explains where to put them.",
  "parameters": {
    "type": "object",
    "properties": {
      "name":      {"type": "string", "description": "Skill name exactly as listed in the Skills section."},
      "arguments": {"type": "string", "description": "Free-form arguments the user gave; substituted for $ARGUMENTS / $1 / $2 in the body."},
      "offset":    {"type": "integer", "description": "0-based line offset for pagination (default 0)."},
      "limit":     {"type": "integer", "description": "Lines to return (default: everything within the body budget)."}
    },
    "required": ["name"]
  }
}
```

`internal/tools/specs_skill.go`（新文件，声明写在实现旁边）：

```go
func init() {
    toolspec.MustRegister(toolspec.Spec{
        Name:      "use_skill",
        Effect:    toolspec.EffectSkill,
        Frequency: toolspec.Hot,   // 目录已经在 system prompt 里点名它；再 Cold 一次等于强迫模型先 load_tool
        Brief:     "取回一个技能的完整正文并按它执行",
        Display:   toolspec.DisplayFull,
        // Available 刻意留 nil：没有技能时工具仍要在场，返回"为什么没有 + 放哪儿"，
        // 而不是从工具面静默消失（read_blueprint 的同一决定，deps.go:120-122）。
        Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) { return newSkillExecutor(d.Skills), nil },
    })
}
```

执行器 `internal/tools/skill_operations.go`（新文件）：

- `d.Skills == nil` ⇒ 返回 `"no skills found — put them in .yagent/skills/<name>/SKILL.md or ~/.yagent/skills/<name>/SKILL.md"`。
- 找不到名字 ⇒ 返回可用技能名单（照 `Materialize` 对 `unknown` 的处理，`surface.go:356-362`：让模型能自我纠正而不是卡死）。
- 找到 ⇒ front-matter 事实 + 正文（分页），`Truncated` 为真时最后一行写 `truncated: continue with offset=N`。
- `$ARGUMENTS` / `$1` / `$2` 文本替换（只替换整词，别把 `$10` 当成 `$1` + `0`）。

### 1.7 基线与 golden

| 文件 | 改动 |
|---|---|
| `internal/toolspec/registry_test.go:412-424` | `49` → `50`，注释追加一行 `49 → 50：技能读通道 use_skill（docs/skills-and-mcp-design.md §6.4）` |
| `internal/agents/registry_gate_test.go:135-142` | `signedReadOnlyAdditions` 加 `"use_skill": true`，理由照 `read_blueprint` 那条写（只读本地文件、无共享写端、无外部进程） |
| `internal/agents/testdata/tool_surfaces.golden` | `go test ./internal/agents -run TestAgentSurfacesGolden -update`，然后 **`git diff` 逐行看**：只允许每个画像多出 `use_skill tier=hot ro`，以及 grant 摘要多出 `external_read`/`skill` 字样 |

### 验收

```bash
go test ./internal/toolspec/... ./internal/tools/... ./internal/agents/... -count=1
go test ./internal/agents -run TestAgentSurfacesGolden -update && git diff --stat internal/agents/testdata/
bash scripts/check_all.sh
```

必须新增的测试：

| 测试 | 断言 |
|---|---|
| `toolspec/spec_test.go:TestValidateDynamic_ReportsEachRule` | §1.3 九条规则**逐条**有能响的夹具 |
| `toolspec/schemas_test.go:TestInlineSchemaBeatsStaticLookup` | 有 `Inline` 时不查 schemas.json；无 `Inline` 时逐字回落 |
| `toolspec/profile_test.go:TestProbeDepsIsCapabilityComplete` 补一条 | `deps.Skills != nil` |
| `tools/skill_operations_test.go` | 无技能 / 名字不存在 / 分页 / 参数替换 / `Truncated` 标记 |
| `agents/registry_gate_test.go:TestStaticRegistryNeverCarriesExternalTools` | `toolspec.Names()` 里没有 `mcp__` 前缀（P3 之后这条才有牙齿，先落地） |

变异：

1. `testDeps()` 去掉 `Skills` ⇒ `TestAgentSurfacesGolden` 必须红（golden 里 `use_skill` 消失）。
2. `writeEffects` 去掉 `EffectExternalWrite` ⇒ `TestValidateDynamic_ReportsEachRule` 里"写工具没声明 Mutates"那条夹具必须变绿（即闸门失效）⇒ 说明变异成功，改回。
3. `Frequency` 改成 `Cold` ⇒ `TestValidate_ReportsEachRule` 不受影响，但 golden 的 tier 列变 `cold` ⇒ golden 红。

---

## P2 — `internal/mcp`：协议面补全 + Manager

**前置**：P0（P1 可并行）。**传输层 `client.go` 只增不改。**

### 2.1 协议类型（`internal/mcp/protocol.go`）

```go
type ToolAnnotations struct {
    Title          string `json:"title,omitempty"`
    ReadOnlyHint   *bool  `json:"readOnlyHint,omitempty"`   // 指针：区分"没声明"与"声明为 false"
    DestructiveHint *bool `json:"destructiveHint,omitempty"`
    IdempotentHint *bool  `json:"idempotentHint,omitempty"`
    OpenWorldHint  *bool  `json:"openWorldHint,omitempty"`
}

type DiscoveredTool struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    InputSchema map[string]interface{} `json:"inputSchema"`
    Annotations *ToolAnnotations       `json:"annotations,omitempty"`
}

type ListToolsParams  struct{ Cursor string `json:"cursor,omitempty"` }
type ListToolsResult  struct {
    Tools     []DiscoveredTool `json:"tools"`
    NextCursor string          `json:"nextCursor,omitempty"`
}
```

`InitializeResult` 加 `Instructions string \`json:"instructions,omitempty"\``。
`ClientCapabilities` **保持空结构体**（我们不消费 resources/prompts/sampling，声明支持就是撒谎）。

`readOnlyHint` 用 `*bool` 是硬要求：`false` 与"没声明"在 §P3 的判定里走向不同分支
（`false` = 明确有副作用；没声明 = fail-safe 按写处理），用裸 `bool` 就分不清 ——
与 `[blackboard]` 三个阀门"靠键的存在性"同一个病（根 `AGENTS.md` §11 坑 26）。

### 2.2 `ListTools`（`internal/mcp/client.go`，**新增方法，不改既有行**）

```go
// ListTools 取回服务端的全部工具声明，自动跟随 nextCursor 分页。
// 不循环分页会静默少工具 —— 与本仓"错了不报错"的头号病害同族。
func (c *MCPClient) ListTools(ctx context.Context) ([]DiscoveredTool, string, error)
```

- 返回值第二项是 `instructions`（从 initialize 缓存下来，见下）。
- 分页循环上限 20 页，超出返回错误而不是静默截断。
- 每页请求走既有 `doRequest`（deadline 取小、`writeMu` 写锁、乱序配对全部继承，一行不用重写）。
- `Instructions` 在 `performInitialize` 里存进一个新的原子保护字段（`instructions atomic.Value` 或 mutex + string），
  提供 `func (c *MCPClient) Instructions() string`。**不要**在 `IsAlive()` 里读它（那个函数只准读两个原子位，`client.go:219-226`）。

### 2.3 `Manager`（`internal/mcp/manager.go`，新文件）

```go
type ServerConfig struct {
    Name, Command string
    Args          []string
    Env           map[string]string
    WorkingDir    string
    RequestTimeout time.Duration
    Enabled       bool
    AllowWrite    bool
    ReadOnly      []string // 显式声明"这些工具虽无 annotations 但只读"
    Tools         []string // 白名单；空 = 全部
    Exclude       []string
    MaxTools      int
    Curated       map[string]bool // 已由 Go 手写桥接的服务端工具名，不进动态注册
}

type ServerState int
const (
    StateDisabled ServerState = iota
    StateStarting
    StateReady
    StateOffline
)
func (s ServerState) String() string   // "disabled" / "starting" / "ready" / "offline"

type ServerHealth struct {
    Name        string
    State       ServerState
    Error       string
    Attempt     int
    MaxAttempts int
    Tools       int      // 动态注册数
    Curated     int      // 被手写桥接占用而排除的数量
    Instructions string
}

type Manager struct { /* 不导出 */ }

func NewManager(cfgs []ServerConfig, opts ManagerOptions) *Manager
func (m *Manager) Start(ctx context.Context) error          // 并发启动，不阻塞（照抄 app.go:213-230 的异步形状）
func (m *Manager) WaitForReady(ctx context.Context) error   // 有界等待 startup_timeout
func (m *Manager) Client(name string) *MCPClient            // nil = 没有这个 server（codeseek 桥靠它拿到既有客户端）
func (m *Manager) DiscoveredTools() []DiscoveredTool        // 只含 Ready 的 server；每项带 Server 字段
func (m *Manager) Call(ctx context.Context, server, tool string, args map[string]interface{}) (*ToolCallResult, error)
func (m *Manager) Health() []ServerHealth                   // 快照，按名字排序（确定性）
func (m *Manager) StatusLines() string                      // §P6 状态行与 /mcp 面板共用的**同一个字符串**
func (m *Manager) Close()                                    // 逐个 Shutdown
func (m *Manager) ForceClose()                               // 逐个 ForceShutdown（--force-quit）
```

`DiscoveredTool` 需要带 server 归属：给它加一个 `Server string` 字段（JSON 标签 `-`），
或者 `DiscoveredTools()` 返回 `[]ServerTool{Server string; Tool DiscoveredTool}`。**选后者**，
不要把归属塞进协议类型（那是 `tools/list` 的线上形状，加字段就是撒谎）。

**状态机**：

```
disabled ─(Enabled=false)                                    → 不进任何面，StatusLines 写 "disabled by config"
starting ─(Start + WaitForReady + ListTools 成功)              → ready
ready    ─(readLoop EOF / IsAlive=false / Call 报 not alive)   → offline → 退避重连
offline  ─(重连成功 + ListTools 成功)                          → ready（工具集 diff 非空则记账 + 发事件）
offline  ─(重连次数用尽)                                       → offline 终态，"gave up after N attempts"
ready    ─(ListTools 成功但返回 0 个工具)                      → degraded：进程活着但没工具，账里写明
```

**实现纪律**（每条都对应一个既有坑）：

1. 锁内只装配字节，**IO 与发事件在锁外**：`Call` 绝不持 `m.mu`；`Health()` 返回快照拷贝。
2. 重连 goroutine：`supervise.From(m.ctx).Go("mcp/reconnect-"+name, …)`。
3. 退避 seam：`var reconnectBackoffBase = 5 * time.Second`（包级变量，测试改它，不许 sleep）。
4. 重连成功后**必须重跑 `ListTools`** 并 diff；diff 结果记进 `Health()` 与事件。
5. `Close()` 幂等（`sync.Once` 或 `stopped.Swap(true)`，照 `client.go:229`）。
6. `Manager` 不读 `config`、不读环境变量：`ServerConfig` 由装配层算好传进来
   （`internal/blueprint` 不读环境变量是同一个决定，根 `AGENTS.md` §6.4）。

### 2.4 假服务器（`internal/mcp/client_process_test.go`）

沿用"重新执行测试二进制"（`TestMain` 认 `YAGENT_TEST_FAKE_MCP_SERVER`，`:22-33`）。
**不要**改成 `go run` 或外部脚本（会把编译时间算进单测，`-race` 下起不了进程）。

新增模式：`tools-list`（3 个工具，其中 1 个带 `readOnlyHint:true`）、`tools-list-paged`（两页 + `nextCursor`）、
`crash-after-list`（列出工具后退出）、`list-changed`（发一条 `notifications/tools/list_changed` 后工具集变化）、
`slow-list`（`tools/list` 拖到超时）、`empty-list`（进程活着、0 个工具）。

### 验收

```bash
go test ./internal/mcp/... -count=1 -race
RACE=true bash scripts/check_all.sh
git diff --stat internal/mcp/client.go    # 只允许新增方法/字段，既有行零改动
```

必须新增的测试（含变异）：

| 测试 | 断言 | 变异 |
|---|---|---|
| `TestMCPClient_ListToolsPaginates` | 两页都收 | 只读第一页 ⇒ 红 |
| `TestMCPClient_ListToolsKeepsInstructions` | `Instructions()` 非空 | 删字段 ⇒ 红 |
| `TestManager_ReadyExposesTools` | 3 个工具、`Health()` 状态 ready | — |
| `TestManager_ReconnectsAfterCrash` | crash → offline → 重连 → ready，工具集恢复 | 去掉重连 ⇒ 红 |
| `TestManager_GivesUpAfterMaxAttempts` | 终态 offline + `gave up` 文案 | — |
| `TestManager_DegradedOnEmptyList` | 0 工具 = degraded，不静默当 ready | 当成 ready ⇒ 红 |
| `TestManager_CallRoutesToRightServer` | 两个 server 同名工具不串 | — |
| `TestManager_ConcurrentCallsStayFramed` | 24 goroutine 并发 `Call`，`-race` 干净 | — |
| `TestManager_StatusLinesIsSingleSource` | `StatusLines()` 与 `Health()` 渲染同源（同一函数） | 各渲染一份 ⇒ 这条测不出来，所以断言"两处调用返回逐字相同" |

**耗时纪律**：重连与慢响应用 `reconnectBackoffBase` 与短 `RequestTimeout` 控制，
整个包墙钟必须 < 3s（今天 `internal/mcp` 是亚秒级；超了就是有测试在等真实退避）。

---

## P3 — `internal/tools`：动态规格构造 + Surface 合并

**前置**：P1 + P2。

### 3.1 `internal/tools/mcp_specs.go`（新文件）

```go
// MCPCaller 是动态规格执行器需要的全部能力。由 *mcp.Manager 天然实现。
type MCPCaller interface {
    Call(ctx context.Context, server, tool string, args map[string]interface{}) (*mcp.ToolCallResult, error)
}

type ExternalInput struct {
    Tools    []mcp.ServerTool
    Caller   MCPCaller
    Config   map[string]mcp.ServerConfig // 按 server 名取 allow_write / read_only / tools / exclude / max_tools
    Health   func(server string) (ready bool, reason string)
    Static   func(name string) bool      // 与静态注册表判重（toolspec.Get 的包装，便于测试）
}

type ExternalAccount struct {
    Total, Registered, Excluded int
    ByReason map[string][]string  // "curated" / "not-in-whitelist" / "excluded" / "over-max" / "name-collision" / "invalid-spec" → 工具名
}
func (a ExternalAccount) String() string  // 一行账，进日志与 /mcp 面板

func BuildExternalSpecs(in ExternalInput) ([]*toolspec.Spec, ExternalAccount)
```

**命名**（`mcp_specs.go` 内，确定性纯函数，逐条有测试）：

```go
func ExternalToolName(server, tool string) string
// mcp__<server>__<tool>；[^a-zA-Z0-9_-] → "_"；
// 超 64 字符 → 前 55 + "-" + sha256(server+"\x00"+tool) 前 8 hex（与 projectid.Compute 同风格）
```

**能力判定**（优先级严格如下，每条都要有夹具）：

| 条件 | Effect | Mutates | Serial | NeedsAuth |
|---|---|---|---|---|
| `annotations.readOnlyHint == true` **或** 名字在 `server.ReadOnly` 名单里 | `EffectExternalRead` | false | false | false |
| `annotations.destructiveHint == true` 或 `readOnlyHint == false` | `EffectExternalWrite` | true | true | true |
| 没有 annotations（含 `annotations == nil`） | `EffectExternalWrite` | true | true | true |
| 写类但 `server.AllowWrite == false` | — | — | — | **不注册**，账记 `write-not-allowed` |

其余字段一律固定：`Frequency: Cold`、`Trunc: TruncFirst`（大体积、可重取）、
`Display: DisplayCompact`、`ResultPaths: toolspec.ResultPathsJSON`、`BlocksOnUser: false`。

`Brief` = description 的第一句截到 40 rune。取首句这件事今天有一份实现在
`internal/agents/deps.go:106-111`（`firstSentence`，供 Meta 的工具池表格用），
而 `internal/tools` 不能用它（会形成 tools → agents 的反向依赖）。
**做法是把它下沉成 `toolspec.FirstSentence`（导出），`internal/agents/deps.go` 里那份直接删掉、改成调用它** ——
不要留转发函数：转发也是一份会漂走的声明，而删掉之后"两处同源"由编译器保证，不需要测试。
下沉时补一条 `toolspec` 侧的表驱动测试（含"没有句号"、"句号在首位"、"多字节字符不被截半"三种输入）。

`Build` 闭包：

```go
Build: func(*toolspec.Deps) (toolspec.ToolFunc, error) {
    return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
        res, err := caller.Call(ctx, server, tool, params)
        if err != nil { return nil, err }
        if res.IsError { return nil, fmt.Errorf("MCP 工具 %q 返回错误: %s", name, firstText(res)) }
        return renderContent(res), nil   // 文本块 join；非文本块渲染成 [unsupported content type: image]，不静默丢
    }, nil
}
```

`Available` 闭包读 `in.Health(server)`，不可用时返回**能读的原因**（`"server github 未连接: exit status 1（第 2/3 次重连）"`）。
这条原因会经 `Surface.Skipped()` 进 `surfaceFor` 的汇总 warn（`internal/agents/deps.go:42-48`）——
既有通道，不要另开一条日志。

### 3.2 `Surface.rebuild()` 合并（`internal/tools/surface.go:103-128`）

静态循环**之后**追加：

```go
for _, spec := range s.deps.ExternalSpecs {
    if spec == nil { continue }
    if s.adapters[spec.Name] != nil {
        s.skipped = append(s.skipped, toolspec.SkippedTool{Name: spec.Name, Reason: "与静态工具同名，静态优先"})
        continue
    }
    // AvailableFor → Build → NewAdapter(...).WithSchema(spec.Parameters()).WithReadOnly(spec.IsReadOnly()).WithBrief(spec.Brief)
    // s.tiers[name] = spec.Frequency（Cold）；ordered = append(ordered, name)
}
```

- guard 挂载**仍在循环之后统一一次**（`SetGuardOnAdapters(adaptersFor(...), guardOf(...))`）——
  集中构造消除"某个 appender 忘记 SetGuard"（`surface.go:123-125` 那条注释的理由）。
- 外部规格**不进 `applyBudget` 的降级候选**（它们是 Cold，`warmestLargest` 天然跳过），
  但**要进 `estimatedTokens`**（既有代码对未 materialize 的 Cold 已经只算目录一行，无需改动）。
- `Surface.Add()` **不要动**：那是 Meta 的 Custom Agent 专用通道（`surface.go:378-418`）。

### 3.3 `Adapter.brief`（`internal/tools/adapter.go`）

```go
type Adapter struct { … brief string }
func (a *Adapter) WithBrief(b string) *Adapter
func (a *Adapter) Brief() string
```

`catalogBrief`（`surface.go:537-553`）取值顺序改成 **`ad.Brief()` > `toolspec.Brief(name)` > description 首句**。
理由：`toolspec.Brief(name)` 查的是静态注册表，动态工具查不到 ⇒ `Spec.Brief` 对它们形同虚设。

`rebuild()` 与 `Add()` 两处构造 adapter 时都要 `.WithBrief(spec.Brief)`（漏一处的表现是目录里那一行变成截断的英文描述）。

### 验收

```bash
go test ./internal/tools/... ./internal/toolspec/... -count=1
bash scripts/check_all.sh
```

必须新增的测试：

| 测试 | 断言 | 变异 |
|---|---|---|
| `TestExternalToolName_CharsetAndLength` | 清洗、>64 截断 + hash 确定性（同输入同输出） | 去掉 hash 后缀 ⇒ 两个长名撞成同一个 ⇒ 红 |
| `TestBuildExternalSpecs_NoAnnotationsIsWrite` | 没 annotations ⇒ `Mutates`/`Serial`/`NeedsAuth` 全真、Effect 是 write | 默认改成只读 ⇒ 红 |
| `TestBuildExternalSpecs_ReadOnlyHintIsParallel` | `readOnlyHint:true` ⇒ `IsReadOnly()` 为真 | — |
| `TestBuildExternalSpecs_ReadOnlyPointerFalseIsWrite` | `readOnlyHint:false` ⇒ 写 | 用裸 bool ⇒ 这条与"没声明"分不清 ⇒ 红 |
| `TestBuildExternalSpecs_CuratedAndWhitelistAccounting` | curated/白名单/exclude/over-max 四类**各自**进账且理由可区分 | 合并成一个理由 ⇒ 红 |
| `TestBuildExternalSpecs_WriteNotAllowedIsDropped` | `allow_write=false` 时写工具不注册 + 记账 | — |
| `TestBuildExternalSpecs_AllSpecsPassValidateDynamic` | 构造结果喂 `ValidateDynamic` 必须零问题 | 去掉 Brief ⇒ 红 |
| `TestSurface_ExternalSpecsAreColdAndListed` | 外部工具进 `Catalog()`、不进 `Defs()`；`Materialize` 后进 `Defs()` | 默认档位改 Hot ⇒ 红 |
| `TestSurface_StaticWinsOnNameCollision` | 同名时静态在、动态进 `Skipped()` 且理由可读 | — |
| `TestSurface_CatalogUsesSpecBrief` | 目录行用的是 `Spec.Brief` 而不是 description 首句 | `catalogBrief` 顺序改回 ⇒ 红 |
| `toolspec` 侧 `TestFirstSentence` | 表驱动：无句号 / 句号在首位 / 多字节不被截半 | 截断按字节而不是 rune ⇒ 第三条红 |
| 编译器 | `internal/agents/deps.go` 里不再有 `firstSentence`：`grep -rn "func firstSentence" internal/agents` 必须零命中 | — |

`internal/agents` 侧的 `TestDerivedMetadataMatchesLegacyTables` **不该变红**：
动态工具不在 `toolspec.Names()` 里。如果它红了，说明你把动态规格注册进全局 registry 了（设计稿 §4.1 禁止）。

---

## P4 — 装配：`depsFor` 加参 + `app.Init` 起 Manager + e2e

**前置**：P3。**这一阶段的强制函数是编译错误：`depsFor` 少绑一个参数就编译不过。**

### 4.1 `internal/agents/external.go`（新文件）

```go
// External 是"外部能力"两件套的注入点。刻意做成一个参数而不是两个：
// depsFor 已经有 5 个入参，而这两件事的装配来源是同一处（app.Init）。
type External struct {
    Specs  func() []*toolspec.Spec // MCP 动态规格；nil = 没有外部工具
    Skills toolspec.SkillSource    // 技能读通道；nil = 没有技能
}

// ExternalProvider 由 *app.Yagent 天然实现（ports.go 的窄接口纪律）。
type ExternalProvider interface {
    ExternalSpecs() []*toolspec.Spec
    SkillSource() toolspec.SkillSource
}
```

`depsFor` 加**一个**参数：

```go
func depsFor(profileKey string, env Env, guard *tools.WorkspaceGuard, boardID string,
             view *BlueprintView, ext External) *toolspec.Deps
```

内部：`d.ExternalSpecs = ext.specsOrNil()`、`d.Skills = ext.Skills`。
`ext.Specs == nil` 时 `ExternalSpecs` 留 nil（**不要**填空 slice：`MatchAll`/`rebuild` 对 nil 与空的处理必须一致，
填了空 slice 就多一种"看起来有其实没有"的状态）。

### 4.2 六个调用点 + 六个 Agent 的 params

| 文件 | 现状 | 改动 |
|---|---|---|
| `internal/agents/coding.go:154` | `depsFor("coding", a.env, a.guard, boardID, a.params.Blueprint)` | 末尾加 `a.params.External` |
| `internal/agents/browser_agent.go:83` | 同形状 | 加 `a.params.External` |
| `internal/agents/devops.go:77` | 同 | 同 |
| `internal/agents/repo.go:143` | 同 | 同 |
| `internal/agents/chat.go:70` | 同 | 同 |
| `internal/agents/director_surface.go:85`（`toolDeps()`） | `depsFor("director", a.env, a.guard, a.boardID, a.blueprint)` | 加 `a.external` |

每个 Agent 的 params 结构体加 `External External` 字段（与既有 `Blueprint *BlueprintView` 并排）。
`SpawnForDelegation` 是**浅拷整个结构体**（`cp := *a`，`internal/agents/spawn.go:29-35`），
所以 `params.External` 自动跟着走，**没有"逐字段拷贝时漏一个"这回事**——不要为此写断言，
那会是一条永远为真的测试（`check_test_hygiene.sh` 要拦的正是这类）。
真正要钉的是端到端结果：`TestSpawnedDelegationKeepsExternalTools`
（给 Coding 装一份含外部规格的 `External` → `SpawnForDelegation` → 建 Surface → 断言外部工具在 `Names()` 里）。
它会因为两种改动而红：谁把 `Spawn` 改成重置 params，或谁让 Surface 丢掉动态规格。

注意浅拷的另一面：`External` 里如果放**可变**状态（比如一个带缓存的规格构造器），
两次委派会共享它 —— 与 `CodingAgent.thinkLink` 当年那个病同源（`spawn.go:3-19` 的文件头注释）。
所以 `External.Specs` 必须是**只读快照的取值函数**，不许在里面 lazily 建可变缓存。

Custom Agent 路径（`director_surface.go:261-278`）用 `Profile{Names: …}` 装配，
`MatchNames` 只认静态注册表 ⇒ **Custom Agent 天然拿不到外部工具**（设计稿 §4.5）。加一条断言钉住这件事。

### 4.3 `internal/app`

```go
// Yagent 新字段
mcpMgr  *mcp.Manager
skills  *skills.Registry      // 取代今天的 SkillRegistry *skills.SkillRegistry

// 对外只读访问器（TUI 用；不要导出可变状态）
func (ca *Yagent) ExternalSpecs() []*toolspec.Spec
func (ca *Yagent) SkillSource() toolspec.SkillSource
func (ca *Yagent) MCPServers() []mcp.ServerHealth
func (ca *Yagent) MCPStatusLines() string
func (ca *Yagent) SkillAccount() skills.LoadAccount
```

`Init` 的改动（顺序本身是契约，`internal/app/AGENTS.md` 硬约束 1；e2e harness 照抄 `app.go:112-292`，
**改完必须同步 `internal/agents/e2e_*_test.go` 的接线顺序**）：

1. 嵌入二进制解出（不动，`:162-172`）。
2. **构造 `[]mcp.ServerConfig`**：codeseek 由 `[codeseek]` 构造（`Name: "codeseek"`，
   `Curated: {codeseek_search, codeseek_skeleton, codeseek_snippet, codeseek_callees, codeseek_callers, codeseek_callgraph}`），
   其余来自 `[mcp] servers`。
3. `mcp.NewManager(...)` → `Start(ctx)`（异步）→ `WaitForReady(bounded ctx)`（`startup_timeout`）。
4. `globalctx.GlobalCtx.CodeSeekMCP = mgr.Client("codeseek")` —— **既有 `RepoOperationsTool` 一行不改**
   （`app.go:249` 与 `global_context.go:39-40` 都保持原样）。
5. `skills.Load(dirs)`（§P5）→ `ca.skills`。
6. `ca.mcpMgr.DiscoveredTools()` → `tools.BuildExternalSpecs(...)` → 存起来供 `ExternalSpecs()` 返回。
   **每次调用重算还是缓存？** 缓存 + 失效：Manager 的工具集只在 `list_changed`/重连后变，
   所以 `ExternalSpecs()` 返回缓存快照，Manager 发一次 `onChange` 回调让 app 重建。
   不要在每次 `depsFor` 里重跑 `BuildExternalSpecs`（那是每次委派一遍 sha256 + 校验）。
7. 启动自检（P0）+ `ValidateDynamic(externalSpecs)`，问题打 `slog.Error`。
8. 一行账进日志：`mcp: 2 servers, 12 tools (9 external + 3 curated-excluded), 1 offline` + 逐 server 明细
   （`Manager.StatusLines()`，**同一个字符串**三处出口）。

`Close()`（`app.go:852-881`）次序：`blueprint.stop()` → `browserMgr.Close()` → **`mcpMgr.Close()`/`ForceClose()`** → `supervise.Root().Drain` 最后并报名字。
`ForceQuit` 走 `ForceClose()`（保持今天 `--force-quit` 的语义，`app.go:864-871`）。

### 4.4 e2e（`internal/agents/e2e_mcp_test.go`，新文件）

照 `e2e_blueprint_test.go` 的形状：真 dispatcher / 真 `WorkspaceGuard` / 真 rollout / 真 `Surface`，
只把 LLM 换成脚本引擎（按 **Agent 身份**分派，不按全局调用序号——`e2e_engine_test.go` 那条纪律），
MCP 服务端用 `internal/mcp` 的假服务器（重新执行测试二进制）。

`TestE2EExternalToolReachesModel` 的断言链（**验收断言是"结果出现"，不是"没报错"**）：

1. 脚本第一轮：模型调 `load_tool{names:["mcp__fake__echo"]}` → 断言工具结果里含该工具的 `inputSchema` 全文。
2. 脚本第二轮：模型调 `mcp__fake__echo{...}` → 断言假服务器**真的收到了**这次调用（它记在 stdout 之外的一条 side channel 或文件里）。
3. 断言 `agent_exit` 之后 Director 侧看到的委派结果里含 echo 的回显文本。
4. 反向臂：Manager 处于 offline 时，同一个工具**不在** `Surface.Names()` 里，且 `Skipped()` 有一条含 server 名与原因的记录。

### 验收

```bash
go build ./... && go vet ./...
go test ./internal/agents/... -count=1 -timeout 300s
go test ./internal/app/... -count=1
RACE=true bash scripts/check_all.sh
```

变异：

1. `SpawnForDelegation` 之后把 `cp.params.External = External{}` 塞进去（模拟"重置 params"）
   ⇒ `TestSpawnedDelegationKeepsExternalTools` 必须红。
2. `depsFor` 里不填 `d.ExternalSpecs` ⇒ `TestE2EExternalToolReachesModel` 红（第 2 条断言）。
3. `Close()` 里把 `mcpMgr.Close()` 挪到 `Drain` 之后 ⇒ 需要一条测试能红：
   `TestCloseStopsManagerBeforeRootDrain`（用一个记录调用顺序的替身）。

---

## P5 — `internal/skills` 重写

**前置**：P1（`SkillSource` 接口已在 toolspec 里）。**与 P2–P4 可并行。**

### 5.1 文件划分

| 文件 | 职责 |
|---|---|
| `skills.go` | `Skill` / `Registry` / `Load(dirs []string) (*Registry, LoadAccount)` |
| `frontmatter.go` | `--- ` 块解析：JSON 优先，扁平 `key: value` 子集容错 |
| `render.go` | `Catalog(maxTokens int) string` 与 `Body(name string, offset, limit int) (toolspec.SkillBody, error)` |

依赖方向：`internal/skills` **import** `internal/toolspec`（`SkillBody` 定义在 toolspec，§1.4）。
toolspec 是叶子包，被谁 import 都合法；`scripts/check_toolspec_leaf.sh` 守的是它**不反向依赖**别人，
所以这次改动不会碰到那把守卫。反过来，`internal/toolspec` 里**不许**出现 `skills` 字样 ——
它只认 `SkillSource` 这个自己声明的窄接口（`deps.go:8-11` 那套理由）。

### 5.2 数据结构

```go
type Skill struct {
    Name         string
    Description  string
    Body         string   // 去掉 front-matter 的正文
    Path         string   // SKILL.md 的绝对路径
    Source       string   // "project" | "user" | "extra:<dir>"
    FrontMatter  string   // "json" | "yaml-subset" | "none"
    AllowedTools []string // 咨询性
    Arguments    []string
    Dir          string   // 目录式技能的目录（扁平式为 ""）
}

type LoadAccount struct {
    Total      int
    BySource   map[string][]string
    Overridden []Override  // {Name, Winner, Loser}
    Rejected   []Rejected  // {Path, Reason}
}
func (a LoadAccount) String() string   // 一行账：skills: 7 loaded (project: 4, user: 3), 1 overridden (…), 0 rejected
```

`Content` 字段**删掉**（今天 `tui_update_keys.go:1007` 用它）。消费方改用 `Body`。
这是有意的破坏性改名：留着 `Content` 会让人以为它还是"全文含 front-matter"。

### 5.3 `Load` 的判据

- 目录顺序：`~/.yagent/skills` → `<repo>/.yagent/skills` → `[skills] dirs`。**后者覆盖前者。**
- 每个目录里两种形态都认：`<name>/SKILL.md`（目录式，胜）与 `<name>.md`（扁平，兼容）。只扫一层。
- 目录不存在 → 跳过，不记账。目录存在但读不出 → `Rejected{Path, "read: <err>"}`，**继续**下一个目录。
  **永不返回 nil Registry**（今天 `LoadSkills` 返回 `(nil, err)` + 调用点 `.Count()` 是启动即崩，
  `internal/AGENTS.md:83-89`）。签名里没有 error 就是这个意思：所有失败都进账。
- `Rejected` 的其他理由：`SKILL.md` 读失败、front-matter 解析失败（仍加载正文，`FrontMatter: "none"` + 一条 warn）、
  同层重名。
- `name` 校验：`^[a-z0-9][a-z0-9_-]{0,63}$`（斜杠命令要用它）。不合规 → `Rejected{"invalid name"}`。

`LoadSkills(dirPath string)` **删掉**（它只有"单目录"语义，三个调用点都要合并语义；留着就是第二份会漂走的事实）。
`skills_test.go` 六个既有用例改成 `Load([]string{dir})`，**扁平 `.md` 那档的断言逐字保留**
（`FrontMatter: "none"` + `Description` = 第一行去 `#`）。

### 5.4 front-matter

```go
func parseFrontMatter(raw string) (fm map[string]interface{}, body string, kind string, err error)
```

- `kind == "json"`：`---\n{...}\n---\n`，`json.Unmarshal` 成功。
- `kind == "yaml-subset"`：`---` 块里每行 `key: value` 或 `key: [a, b]` 或 `key: a, b`；
  **不认**嵌套、多行、锚点、引号转义。至少解析出 `description` 才算成功。
  文件头注释必须写明"**这不是 YAML 解析器**"（否则下一个人会往里加特性）。
- `kind == "none"`：没有 `---` 块，或块解析失败（失败要 `slog.Warn` 一行 + 记进账）。
  回落：`Description` = 第一行去 `#`（今天的行为，`skills.go:64-72`）。
- 认得但没用上的键（比如 `license`）→ 不 warn（那是正常技能），但**不许静默丢**：
  存进 `Skill.Extra map[string]string`？—— 不需要。判据是"改这个值有没有用户可观察的行为"
  （根 `AGENTS.md` §11 坑 25）：没有行为就别存。

### 5.5 `Catalog` 与 `Body`

```go
func (r *Registry) Catalog(maxTokens int) string
```

- 空注册表 ⇒ 返回 `""`（**不注入空标题**；与 `Surface.Catalog()` 同语义，`surface.go:219-233`）。
- 正文形状（照 deferred 目录的措辞风格）：

```
## Skills
Reusable procedures the user wrote for this project. Call `use_skill` with a name to fetch its full body, then follow it.
- code-review: 对一段 diff 做安全与可读性审查
- release-notes: 从 git log 生成发布说明
(3 more not shown — call use_skill with the name directly)
```

- 每行 `name: description`，description 截到 `catalog_line_chars`（默认 120 rune，中间省略用 `common.Ellipsize`？
  ——那是 TUI 包，skills 不许 import tui。自己写一行截断，**尾部**省略即可，并在注释里说明为什么不用中间省略）。
- 预算：`maxTokens` 用 `len/4` 近似（与 `surface.go:175-188` 同口径，不要引 tiktoken：
  这段每次 Run 都算，且只为决定截多少行）。
- 超出 ⇒ 按名字排序保留前 N 条 + 一行截断标记（**标记必须写进正文**，蓝图 `TRUNC` 的同一教训）。
- 排序确定性：按 `Name` 字典序（每次进程内一致，利于 provider 前缀缓存）。

`Body` 按行分页，返回 `toolspec.SkillBody`；`Truncated` 与 `NextOffset` 必须自洽
（`NextOffset = offset + 实际返回行数`）。

### 5.6 `main.go` 去重

删掉 `main.go:224-241` 与 `main.go:374-391` 那两份逐字相同的 17 行；技能装配收进 `app.Init`。
`runHTTP` 因此自动获得技能（今天压根不加载）。**不要**在 `runHTTP` 里再抄一份。

### 验收

```bash
go test ./internal/skills/... -count=1
go test ./internal/tools/... ./internal/agents/... -count=1
bash scripts/check_all.sh
grep -rn "LoadSkills" --include="*.go" . | grep -v _test   # 必须零命中
```

必须新增的测试：

| 测试 | 断言 | 变异 |
|---|---|---|
| `TestLoad_MergesProjectOverUser` | 两级都加载；同名时项目级胜且 `Overridden` 记一条 | 改回"整体替换" ⇒ 红 |
| `TestLoad_UnreadableDirDoesNotPanic` | 目录 mode 000 ⇒ `Rejected` 一条、其余目录照常加载、返回非 nil | 恢复 `(nil, err)` ⇒ 红 |
| `TestLoad_DirectoryLayoutWinsOverFlat` | 同名时 `<name>/SKILL.md` 胜 | — |
| `TestParseFrontMatter_JSON` | 三个键都读到，`Body` 不含 front-matter | — |
| `TestParseFrontMatter_YAMLSubset` | 扁平 `key: value` 与 `key: a, b` 都认；嵌套**不认**且回落 none + warn | 悄悄支持嵌套 ⇒ 这条夹具要能红（断言 kind=="none"） |
| `TestParseFrontMatter_BrokenFallsBackToFirstLine` | 坏块 ⇒ `FrontMatter=="none"` 且 `Description` = 第一行去 `#` | — |
| `TestCatalogRespectsBudget` | 超预算 ⇒ 条数减少 + 截断标记在正文里 | 去掉标记 ⇒ 红 |
| `TestCatalogEmptyRegistryReturnsEmpty` | 返回 `""` 而不是 `"## Skills\n"` | — |
| `TestBodyPaginationIsSelfConsistent` | 两页拼起来 == 全文；`NextOffset` 正确 | — |
| `TestLoadAccountStringIsStable` | 同一输入逐字同一输出（排序） | — |

---

## P6 — 注入 + 事件

**前置**：P4 + P5。

### 6.1 两处 system prompt 注入

| 位置 | 加什么 |
|---|---|
| `internal/agents/executor.go:250-260` | `systemPrompt += cfg.SkillCatalog`（在 `Surface.Catalog()` **之后**） |
| `internal/agents/director.go:909-936`（prompts hook） | `systemPrompt += a.skillCatalog()` 与 `+= a.mcpStatusLine()`，紧挨 `a.blueprint.Resident()` |

`ExecutorConfig` 加一个字段：

```go
// SkillCatalog 是技能目录（稳定前缀的一部分，与 Surface.Catalog() 同理）。
// 空串 = 不注入。由装配侧按 [skills] inject_into 决定给不给。
SkillCatalog string
```

哪些 Agent 填它：`[skills] inject_into` 名单（默认 `director, coding, repo, chat`）。
判据写在**一处**（`internal/agents` 的一个 helper），五个子 Agent 各调它，
**不要**在五个 `Run()` 里各写一遍 `if`（那正是压缩开关历史上写过 5 个 AND 点的病，根 `AGENTS.md` §6.5）。

**MCP 状态行只注入 Director**（子 Agent 不需要知道 server 拓扑；它们的工具面里没有就是没有，
`Skipped()` 的日志足够排障）。理由写进注释。

注入的两段文本都是**稳定前缀**：技能集与 server 状态在一次进程内基本不变。
状态行会变（重连），所以它必须**短**（每 server 一行、有上限），
否则每次变化都打掉 provider 的前缀缓存。上限：`[mcp] status_max_lines`（默认 8，超出合并成 `+N more`）。

### 6.2 `mcp_server_status` 事件

| 文件 | 改动 |
|---|---|
| `protocol/agent-events.yaml` | 加一条 `event_types` 条目（照 `context_loaded` 的形状，`:172-187`）：fields = `server`(string,required) `state`(string,required) `tools`(number) `curated`(number) `error`(string) `attempt`(number) `max_attempts`(number) `instructions`(string) |
| `protocol/agent-events.schema.json`、`protocol/ts/agent-events.ts`、`protocol/ts/render-mapping.ts` | 这三个与生成器输出逐字节相同，可以重跑生成器只覆盖它们 |
| `internal/protocol/agent_events.go` | **手工粘**四块：`EventType…` 常量、`…Data` 结构体、registry 条目、render hint 条目（照 `context_loaded` 那四处：`:21,108-112,221,266-269`）。**不要整档重跑生成器**：它会删掉手写的 `InteractionType`(`:45-55`)、`InferInteractionType`(`:364`)、`isBooleanPair`(`:380`) |
| `protocol/go/agent-events.go` | 同样手工粘（该文件**零引用方**，但不改就是第二份漂移的事实） |

发布点：Manager 的状态迁移与 `tools/list` diff。**Publisher 由装配层注入 Manager**
（`Manager` 不认识 `messaging`：传一个 `func(eventType string, payload map[string]interface{})` 进去，
保持 `internal/mcp` 的依赖面只有标准库 + supervise）。

发布形状照 `director.go:255-262`：`publisher.Publish("mcp_server_status", map[string]interface{}{...}, "mcp")`。

**总线不需要注册**：`internal/messaging` 没有类型白名单，任何字符串都能流（`message_dispatcher.go:208-244`）。

### 6.3 TUI 消费（否则界面会出现 `map[...]`）

未知事件类型今天会被 `formatEventAsEntry` 的 `default` 分支**原样渲染**
（`internal/tui/tui_render.go:713` 的 switch，`default` 在 `:918-923`）。
所以必须在 `handleTaskEventMsg`（`internal/tui/tui_update_tasks.go:80` 起的 if 链）里加一个分支：
把 `mcp_server_status` 渲染成一行人类可读文本（复用 `Manager.StatusLines()` 的**同一段措辞**——
把渲染函数放在 app 侧或 mcp 侧，TUI 只调它，不要在 TUI 里再拼一份格式）。

### 6.4 单源断言

`TestStatusLinesIsSingleSource`：`Manager.StatusLines()`、启动日志那一行、`/mcp` 面板、事件 payload 的
`state`/`error` 字段，**必须来自同一次 `Health()` 快照**。做法：断言"两次调用 `StatusLines()` 逐字相同"
+ "TUI 渲染函数与 `StatusLines()` 对同一份 `[]ServerHealth` 产出同一串"。

### 验收

```bash
go test ./internal/agents/... ./internal/mcp/... ./internal/tui/... -count=1
bash scripts/check_all.sh
```

新增测试：

| 测试 | 断言 |
|---|---|
| `TestExecutorInjectsSkillCatalogAfterToolCatalog` | `SkillCatalog` 非空时出现在 system prompt 里、且在 `Surface.Catalog()` 之后 |
| `TestSkillCatalogRespectsInjectInto` | 名单外的 Agent 拿不到目录（用一个只有 chat 的名单跑 repo，断言 system prompt 不含 `## Skills`） |
| `TestDirectorPromptCarriesMCPStatusLine` | 状态行在 Director 的 system prompt 里；子 Agent 的不在 |
| `TestMCPStatusLineIsBounded` | 12 个 server ⇒ 行数 ≤ `status_max_lines` + 一行 `+N more` |
| `TestStatusLinesIsSingleSource` | §6.4 |
| `internal/tui`：`TestMCPServerStatusEventRendersOneLine` | 事件进来后日志区多一行、**不含** `map[` |

变异：把 `StatusLines()` 复制一份到 TUI 里改格式 ⇒ `TestStatusLinesIsSingleSource` 必须红。

---

## P7 — TUI

**前置**：P6。

### 7.1 事实校正（与 `internal/tui/AGENTS.md` 的措辞不同）

- "不 import `internal/agents`" 是**文档约定，不是脚本守卫**：`scripts/check_no_agents_import.sh:15-16` 只守 `internal/agents/director`。
  本阶段仍然遵守它（落点由 `app` 侧算）。
- `internal/tui` **已经** import `internal/app`（`m.assistant` 是 `*app.Yagent`，`tui_model.go:360`），
  所以技能与 server 数据经 `Yagent` 的只读访问器拿（§4.3），**不要**新加一条依赖路径。

### 7.2 两个信息对话框

新文件 `internal/tui/components/skills_dialog.go` 与 `mcp_dialog.go`：

```go
type SkillRow struct{ Name, Description, Source, FrontMatter string }
func NewSkillsDialog(rows []SkillRow) *SkillsDialog

type MCPServerRow struct{ Name, State, Detail string; Tools, Curated int }
func NewMCPDialog(rows []MCPServerRow, statusLines string) *MCPDialog
```

必须满足 `components.Dialog` 契约（`components/dialog.go:24-28` + `component.go:13-44` 的 11 个方法），
`View()` 首行加尺寸守卫（照 `model_select_dialog.go:169`：`if d.width < 40 || d.height < 5 { return "" }`）。

行数预算：`dialogListChrome = 10`（`components/dialog_list.go:20-22`，含边框 2 + 内边距 2 + 标题 2 + 空行 2 + 表头 1 + 提示 1）。
**对话框每多/少一行非列表内容，这个常数必须同步改**（`internal/tui/AGENTS.md` 硬约束 7）。
列表几何一律走 `common/list_metrics.go` 的 `PanelWidths` / `ListBudget` / `WindowRange` / `MoreLine` / `Ellipsize` / `PadLeft`
——**不许在 components 或 tui 任何一边再写一份**（硬约束 7 的另一半）。

入栈（照 `tui_update_commands.go:220-284` 的 `showModelSelectionDialog` 链）：

```go
d := components.NewSkillsDialog(rows)
d.SetBounds(m.termWidth, m.termHeight)   // 必须在 Push 之前：弹窗守卫会吞 WindowSizeMsg
m.dialogStack.Push(d)
```

这两个是**信息对话框**（不可选择），所以不进 `selectProbe`（`select_dialog_contract_test.go:22-38`）；
但要检查 `TestListDialogs_RenderContract`（`:329`）是否枚举列表类对话框 —— 如果枚举，加进去。

### 7.3 斜杠命令

`tui_update_commands.go` 的 `/` 分支（`:27-42`）**必须显式列出**，否则 `/skills` 是在搜字符串 "skills"：

```go
if cmd == "/skills" { return m.showSkillsDialog() }
if cmd == "/mcp"    { return m.showMCPDialog() }
```

**手打 `/name args` 执行技能**（今天必须先让补全弹出来再按 Tab）：在内置命令之后、`runSearch` 之前插入

```go
if name, args, ok := splitSlashSkill(cmd); ok {
    if _, isSkill := m.assistant.SkillSource().Get(name); isSkill { return m.runSkill(name, args) }
}
m.runSearch(strings.TrimPrefix(cmd, "/"))
```

规则：**首个 token 精确命中技能名**才走技能，否则照旧搜索（技能名是 kebab-case，搜索词通常不是）。
`runSkill` 保持今天的行为——把正文拼进任务提交（`tui_update_keys.go:1005-1012`），
但正文取 `Body`（不含 front-matter），并带上 `arguments`。
**理由要写进注释**：用户显式点了这个技能，就不该为它多付一次 `use_skill` 往返；
模型自主发现走的是 `use_skill` 那条路。

### 7.4 补全列表分类

今天 `skillSuggestions []string` 把内置命令与技能混在一起，靠 `tui_update_keys.go:971-1003` 三个硬编码特判区分。
改成带类型：

```go
type completionItem struct{ name, kind, desc string } // kind: "cmd" | "skill"
```

需要改的每一处（漏一处就编译不过或界面错位）：
`tui_model.go:424-426` 与 `:583-584`、`tui_view.go:253-272`、`tui_render.go:64-66`、
`tui_update_autocomplete.go:44-46,81-82,120,128-192,299-300`、`tui_update_keys.go:916-1013`。

- 渲染：`▶ name  [cmd]` / `  name  [skill] — description`，角标用既有强调色令牌（`common/styles.go`，141/62/214 那套）。
- 撞名：**内置命令胜** + 加载期一条 warn（与 `tui_keymap` 抢键的既有规则同取向，根 `AGENTS.md` §11 坑 29）。
- Enter 分支的三个硬编码特判可以删掉，改由 `kind` 分派（`cmd` → `processCommand`，`skill` → `runSkill`）。
  **`history`/`resume` 那两个是进模式而不是执行命令**，分派时保留它们的特殊路径。

### 验收

```bash
go test ./internal/tui/... -count=1
bash scripts/check_all.sh
```

TUI 的测试约定是**断言精确单元格宽度而不是子串**（`internal/tui/AGENTS.md`）：

| 测试 | 断言 |
|---|---|
| `TestSkillsDialog_WidthMatrix` | 宽度 `{30,40,60,88,120,240}` × 高度 `{18,24,40}`：行数 ≤ 终端高度、每行宽度 ≤ 终端宽度 |
| `TestMCPDialog_WidthMatrix` | 同上 |
| `TestView_ExactHeight` 系列 | 两个对话框入栈后整屏高度仍自洽（`tui_chrome_test.go:267,530`） |
| `TestTUI_SlashSkillRunsFromTypedText` | `/code-review 看看这个 diff` + Enter ⇒ 提交的任务正文含技能 `Body` 与 `arguments`，**不含** front-matter |
| `TestTUI_SlashUnknownFallsBackToSearch` | `/notaskill foo` ⇒ 进搜索模式（回归保护） |
| `TestCompletionItemsAreKinded` | 内置命令与技能同名时 cmd 胜 + 一条 warn |
| `TestDialogGuardPassesTaskCompleteToSkillsDialog` | 对话框打开时 `taskCompleteMsg` 不被吞（`internal/tui/AGENTS.md` 硬约束 2；对照 `TestTaskCompleteMsgNotSwallowedBehindDialog`） |

**无法在 CI 里验证的部分要明说**：TUI 是交互界面，本仓没有终端录制回放。
提交说明里写清"渲染断言覆盖了宽度矩阵，但没有在真实终端里手动确认过"——不要声称"界面已验证"。

---

## P8 — 配置面

**前置**：无（可与 P1 并行开工，但必须在 P4 之前合入，否则装配层没有配置可读）。

### 8.1 新文件

| 文件 | 内容 |
|---|---|
| `internal/config/mcp.go` | `MCPConfig` / `MCPServerEntry` / `DefaultMCPConfig()` / `MCPPresence` / `SetGlobalMCP` / `GlobalMCP` / `MCPEnabled()` / `NormalizeMCP` |
| `internal/config/skills.go` | 同构：`SkillsConfig` / `DefaultSkillsConfig()` / `SkillsPresence` / `SetGlobalSkills` / `GlobalSkills` / `SkillsEnabled()` |

**照 `internal/config/blueprint.go` 逐段抄形状**（它是这套约定最完整的一份）：

1. `Enable`/`Enabled` 靠**键的存在性**判定，不看零值（`Presence` 结构体 + `sectionHasDecodedKey`/`md.IsDefined`）。
   缺省语义是"开"，Go 零值是 false ⇒ 直接看值会把"没写"当成"用户关掉了"（根 `AGENTS.md` §11 坑 26）。
2. kill-switch 写在 `XEnabled()` **内部**：`os.Getenv("YAGENT_MCP") == "0"` / `"YAGENT_SKILLS"`，只认精确 `"0"`。
   判据只此一处，所有消费方共问（`BlueprintEnabled()` 的注释就是这条纪律）。
3. `Normalize*` 里做缺省归一，**不要在调用点做**（漏一处的表现只是"还是老行为"）。
4. 每个键都要有真实消费者；`SetGlobal*` 里打一行 `slog.Info` 把生效值全列出来（照 `blueprint.go:107-117`）。
5. 文件头注释写清"为什么走全局单例而不是穿参数"（照 `deferred_tools.go:1-8`）。

`[mcp]` 的键（默认值）：

```
enable(true) startup_timeout("30s") reconnect_backoff("5s") max_reconnects(3)
max_tools_per_server(40) status_line(true) status_max_lines(8)
```

`[[mcp.servers]]` 的键：`name` `command` `args` `env` `working_dir` `enabled(true)` `request_timeout(30)`
`allow_write(false)` `read_only([])` `tools([])` `exclude([])`。

`[skills]` 的键：`enable(true)` `dirs([])` `inject_into(["director","coding","repo","chat"])`
`catalog_max_tokens(600)` `catalog_line_chars(120)` `body_max_tokens(4000)`。

`config.validate()` 里加校验（**注意别加提前 return**，否则后面的归一化段全停在零值，根 `AGENTS.md` §11 坑 14）：

- server `name` 必须唯一且匹配 `^[a-z0-9][a-z0-9_-]{0,31}$`（它要进工具名前缀）。
- `name` 不得是 `codeseek`（保留给内建 server）。
- `command` 非空（`enabled = true` 时）。
- `inject_into` 里不认识的名字 → warn 后丢掉（照 `normaliseInjectInto`，`blueprint.go:134-154`）。

### 8.2 两份模板

`internal/config/default_config.toml` 与 `config/config.toml` 都要加这三段，**注释用英文**，
每个键一行说明"改它会发生什么"（照 `[codeseek]`/`[browser]` 的既有风格）。
`TestTemplatesDecodeCompletelyAndValidate`（`template_test.go:14-58`）会拦住"写了没人读的键"——
所以模板里的每个键都必须有 struct tag 与真实消费者。

**`[codeseek]` 三个键一个不删**（`binary_path`/`mcp_args`/`request_timeout`）：删了会命中 `removedKeyHints`
告警，而老配置会漂。它现在语义变成"内建 codeseek server 的覆盖项"。

### 验收

```bash
go test ./internal/config/... -count=1
bash scripts/check_all.sh
```

新增测试（照 `blueprint_test.go` / `resume_test.go` 的形状，**每个键都要有一条"改值就必须变"的断言**）：

| 测试 | 断言 |
|---|---|
| `TestMCPConfigDefaultsWhenSectionAbsent` | 整段没写 ⇒ 全部默认值，且 `MCPEnabled()` 为真 |
| `TestMCPConfigEnableFalseIsHonored` | 显式 `enable = false` ⇒ 关（不是被写回 true） |
| `TestMCPEnabledKillSwitch` | `YAGENT_MCP=0` ⇒ 关；`"1"`/`""`/`"false"` ⇒ 不关（只认精确 `"0"`） |
| `TestMCPServerNameValidation` | 重名 / 非法字符 / `codeseek` ⇒ `LoadFromFile` 报错且错误里点名 |
| `TestSkillsConfigInjectIntoUnknownIsDropped` | 不认识的名字 warn + 丢；显式 `[]` = 全不注入（与"没写"不同） |
| `TestSkillsConfigEachKeyChangesBehavior` | 六个键逐个改值 ⇒ `GlobalSkills()` 逐字跟着变 |
| `TestTemplatesDecodeCompletelyAndValidate` | 不改，但必须继续绿（它同时覆盖两份模板） |

---

## 收口清单（全部阶段做完之后）

### 1. 全量验证

```bash
go build ./... && go vet ./...
bash scripts/check_all.sh
RACE=true bash scripts/check_all.sh
gofmt -l internal/ main.go          # 新建/大改的文件必须干净；存量漂移用 gofmt -d 区分
grep -rn "TODO\|FIXME" internal/mcp internal/skills internal/tools/mcp_specs.go   # 本分支不留 TODO
```

`go test ./... -count=1 -timeout 600s` 全仓墙钟基线约 12s；
如果 `internal/mcp` 或 `internal/skills` 单独超过 3s，说明有测试在等真实退避或真实 IO（§0.2）。

### 2. 手工验收（**不能只靠单测**）

用一个最小假 MCP server（`node -e` 或一个 20 行的 Go 程序都行，**不要**用 codeseek）跑一遍真机：

1. 配一个 server → 启动 → 日志里出现 `mcp: 1 servers, N tools`，`/mcp` 面板显示 ready。
2. 让模型 `load_tool` 取回一个外部工具 → 调用 → 结果回到对话。
3. `kill -9` 那个 server → 状态转 offline、退避重连、`/mcp` 面板与状态行都变；重连成功后工具回来。
4. 放一个 `.yagent/skills/demo/SKILL.md`（JSON front-matter）→ `/skills` 看得见 → `/demo 参数` 能跑 →
   让模型自己发现它（问"这个项目有什么可复用的流程"）→ 它调 `use_skill`。
5. `YAGENT_MCP=0` / `YAGENT_SKILLS=0` 各跑一次：两套机制**整体退出**，不起 goroutine、不建目录、不注入。

**跑真机时不要启长驻进程做验证**（根 `AGENTS.md` §10.1 第 5 条）：yagent 本身用 `--prompt` 一次性跑，
假 server 用 `timeout` 兜住。

### 3. 文档同步（设计稿 §13 那张表，逐条做完）

- 根 `AGENTS.md`：§3 模块地图两行、§5.2 工具总览（`use_skill` + "50 条 + 运行期动态外部工具"）、
  §5.3 MCP 整节重写、§6.4 加 Skills 一段、§7 配置面三段 + 基线数字、§9.3 加"外部工具走动态通道"、
  §11 新增两条坑（"动态规格不进全局注册表的三条理由"、"没 annotations 一律按写处理"）。
- `internal/toolspec/AGENTS.md`：基线 49→50；加"静态 registry 只读、动态走 `Deps.ExternalSpecs`"硬约束。
- `internal/AGENTS.md`：mcp 一节（"从不调 tools/list" 改成"调，且分页"）；skills 一节（删 nil 崩溃条目，换成合并语义与账目）。
- `internal/tools/AGENTS.md` / `internal/app/AGENTS.md` / `internal/tui/AGENTS.md`：各加本设计引入的硬约束
  （`catalogBrief` 取值顺序、`Close` 次序含 Manager、`dialogListChrome` 对账与两个新对话框）。
- `internal/mcp/AGENTS.md`：**新建**（这个目录今天没有局部文档，P2 之后它有了不可派生的知识：
  状态机、退避 seam、假服务器模式、curated 名单）。
- `docs/config-guide.md`：三段新配置的键表；`allowed-tools` **必须写明"不强制"**。
- `README.md` / `README_zh.md`：技能那段从"扁平 .md 当斜杠命令"改成目录式 + 模型可自主发现；MCP 那段加"可接第三方 server"。
- `docs/ARCHITECTURE.md` / `.en.md`：整体已过期（根 `AGENTS.md` §11 坑 1），只在 MCP/skills 两节做最小更正。

### 4. 阶段依赖图

```
P0 ─┬─► P1 ─┬────────► P3 ─► P4 ─► P6 ─► P7
    │       │            ▲
    └─► P2 ─┴────────────┘
P8 ─────────────────────► P4（装配要读配置）
P1 ─► P5 ────────────────► P4
```

可并行：P1/P2/P8 三条同时开；P5 只等 P1。串行硬约束：P3 要 P1+P2，P4 要 P3+P5+P8，P6 要 P4，P7 要 P6。

### 5. 一句话完成判据

> 配一个第三方 MCP server、放一个 `SKILL.md`，**不重启不改代码**：
> 模型能自己发现并调用它们；server 崩了会重连且状态可读；技能与工具都没有时，
> 界面、日志、模型三个视角都能读到"为什么没有 + 怎么修"。
