# internal/ — 目录导览与小包硬约束

> 范围：本文件只讲 `internal/` 下**从代码派生不出来**的东西——职责边界、硬约束、
> 踩过的坑、入口文件、钉住行为的测试。模块列表、文件计数、依赖边由 `internal/blueprint`
> 每轮派生，不要往任何 AGENTS.md 里抄（抄了就是第二份会漂走的事实）。
> 全局架构见仓库根 `AGENTS.md`。

## 哪些目录有局部文档

有局部 `AGENTS.md` 的目录（进去改东西之前先读它）：

| 目录 | 一句话 |
|------|--------|
| `internal/tui/AGENTS.md` | Bubble Tea v2 界面：事件消费、布局高度、共用排版原语 |
| `internal/agents/AGENTS.md` | 子 Agent 与共享 LLM-工具循环（含 `activity/`、`toolbatch/`） |
| `internal/agents/director/AGENTS.md` | Director 编排内核：Planner、窄接口、瞬态索引 |
| `internal/tools/AGENTS.md` | 工具执行器、Surface 装配、工作区守卫、授权回路 |
| `internal/toolspec/AGENTS.md` | 工具元数据唯一声明面（叶子包） |
| `internal/memory/AGENTS.md` | rollout 账本与重放读侧 |
| `internal/compression/AGENTS.md` | 三档上下文压缩与判定口径 |
| `internal/blueprint/AGENTS.md` | 仓库蓝图：派生层 + 落盘层 + fold |
| `internal/blackboard/AGENTS.md` | 跨 Agent 便签与派生索引 |
| `internal/llm/AGENTS.md` | 引擎、路由、故障转移、在飞闸门 |
| `internal/config/AGENTS.md` | TOML 加载、归一化、文本级写回 |
| `internal/app/AGENTS.md` | 一次性装配（顺序本身是契约） |
| `internal/http/AGENTS.md` | REST/WS 与任务生命周期 |
| `internal/messaging/AGENTS.md` | 事件总线与丢弃记账 |
| `internal/mcp/AGENTS.md` | stdio JSON-RPC 客户端 + 多 server Manager（状态账、重连、分页） |
| `internal/browserctl/AGENTS.md` | 外部 browser-control CLI 的执行层 |

**没有局部文档 = 这个目录没有不可派生的知识**，直接读代码。下面收录的是那些
"没有局部文档、但有一条不读代码就会做错"的小包（`mcp` 一节是例外：它已有局部文档，这里只留指针）。

## projectid — 算法逐字节冻结

`Compute` 的 base 截断 100 + sha256 前 8 hex 是 rollout / artifact / 蓝图存储共用的主键
（`internal/projectid/projectid.go:24-44`，`internal/blueprint/fold.go:70` 记为 I12）。改算法**不会报错，只会让历史数据
找不到目录**。期望串必须是字面量：`TestComputeGoldenTable`（`internal/projectid/projectid_test.go:15`）。
`director.ComputeProjectID` 只是转发，不许长成第二份（`TestDirectorForwardsToProjectid`）。
曾经分叉的那份（`app.getProjectID`，sha 前 12 hex，只管 `shared_memory` 目录）已随 KV 仓库记忆一起删除
——**主键从此只有 `projectid.Compute` 一处**。

## protocol — DO NOT EDIT 的文件里有手写类型

`internal/protocol/agent_events.go:1` 写着生成器产物，但 `InteractionType`（`:46-55`）、
`InferInteractionType`（`:364`）、`isBooleanPair`（`:380`）**生成器不产出**（`scripts/protoc-gen-yagent`
里没有对应发射逻辑）。整档重跑生成器会删掉它们并打断授权对话框
（消费方：`internal/tools/flow_control.go:82`、`internal/tools/user_confirm.go:200`、`internal/tui/components/user_help_dialog.go:80`）。
另外这个包**不是**事件总线的类型登记表：`internal/messaging` 自带 `EventType` 且不 import 它。

## logging — TUI 模式永不写 stdout

`internal/logging/logger.go:74-76`：TUI 下 stdout 归界面，任何 `fmt.Println` 都会撕裂渲染。自带日志器在开文件
失败时必须退回 `GetFallbackWriter()`（`internal/logging/logger.go:208-225`，按模式返回 app 日志 / `io.Discard` /
stderr），下游已把这条写成 CRITICAL 注释（`internal/agents/tool_logger.go:67,94`）。
`YAGENT_LOG_LEVEL` **区分大小写**，只认 `DEBUG|INFO|WARN|ERROR`，小写静默回落 INFO（`:127-141`）。

## embedbin — `dist/bin/1` 不能删

`dist/` 被 `.gitignore` 忽略，但 `dist/bin/1`（0 字节）和 `dist/data/models.json` 是被跟踪的
（`git ls-files dist/`）。`//go:embed dist/bin/*`（`embed.go:5`）在空目录上**直接编译失败**
（实测 `pattern dist/bin/*: no matching files found`），那个单字符文件就是占位符。
它对提取不可见只因 `isExecutableName` 拒绝长度 1（`internal/embedbin/embedbin.go:154-156`）——改名成两字符会
解出一个 1 字节的假可执行文件。是否重写磁盘由 `version.json` 决定，**不校验内容**
（`:66-89`）：手工替换 `~/.yagent/bin/codeseek` 不会被发现。提取失败只 `slog.Debug`
（`internal/app/app.go:168`），默认级别下不可见，症状是"退化成纯文件工具"。
`rg` 不在 `dist/bin` 里（git 树），`internal/tools/search_operations.go:16-23` 回落系统 PATH。

## globalctx — 没有版本符号

`globalctx.Version/BuildTime/GitCommit` **不存在**（`grep -rn` 在 `*.go` 里零命中），
而 `build.sh:164-166` 与 `.github/workflows/release.yml` 仍在 `-X` 注入它们；`go build -ldflags
"-X pkg.NoSuch=1"` 静默成功，所以没人发现。别按根 AGENTS.md 的旧说法加 `--version`。
读运行时可变字段必须走 `EnvView`（`internal/globalctx/env_view.go:3-5` 的动态读取语义）。跨 Agent 共享的可变值
走 `RepoContextStore`（RWMutex，`internal/globalctx/repo_context_store.go:5-12`），它是原先无锁共享字符串的替代品。

## dict — 没有热加载

补全词典只在 TUI 构造时加载一次（`internal/tui/tui_model.go:653-657`），改文件必须重启。
`fsnotify` 只活在 `go.mod`，Go 代码里零引用。根 AGENTS.md 的"fsnotify 热加载（词典）"是过期描述。
`Complete` 的 20 条上限写死（`internal/dict/completion.go:82`），而 TUI 包装层的 `limit` 形参被丢弃
（`internal/tui/completion.go:4-10`）。源文件读失败一律静默跳过（`internal/dict/completion.go:42-49`）。

## skills — 合并语义，Load 永不返回 nil

`Load(dirs []string)` 按 user → project → `[skills] dirs` 的顺序**合并**（后者覆盖前者），
返回 `(*Registry, LoadAccount)`：目录不存在跳过不记账，存在但读不出记一条 `Rejected` 并继续。
签名里没有 error 就是这个意思 —— 所有失败都进账，"启动即崩"那条路径从接口上消失。
磁盘两种形态都认：`<name>/SKILL.md`（目录式，同名时胜）与 `<name>.md`（扁平）。
front-matter 是**手写的 JSON + 扁平 key:value 子集**，不是 YAML 解析器（`frontmatter.go` 头注释）；
解析失败仍加载正文（`FrontMatter: none`）并 warn。`Catalog(maxTokens)` 的截断标记必须写进正文；
`Body(name, offset, limit)` 的分页片段拼接 == 全文。三个入口（TUI/HTTP/--prompt）共用
`app.Init` 里的同一份加载，`LoadSkills` 已删除。只扫一层：子目录里没有 `SKILL.md` 就当资源目录、不递归
（`internal/skills/skills.go:172-173`）。加载顺序的实际落点是 `~/.yagent/skills` →
`<workDir>/.yagent/skills` → `[skills] dirs`（`internal/app/skills.go:11-21`）。
`use_skill` 是 **Hot** 且 `Available` 刻意留 nil：没有技能时工具仍在场、回执自带"放哪儿"
（`internal/tools/specs_skill.go:9-12,17`、`internal/tools/skill_operations.go:18-22`），
名字查不到时回吐可用名单而不是报错（`skill_operations.go:41-49`）。`allowed-tools` 与 `arguments`
是**咨询性**字段：只随回执回吐给模型，不改工具面、不做强制（`internal/skills/skills.go:34-37`、
`internal/tools/skill_operations.go:51-52`）。账目一行的实现只有一份（`LoadAccount.String`，
`internal/skills/skills.go:65-66`），而**当前唯一的消费者是启动日志**（`internal/app/app.go:161`）；
`/skills` 面板列的是 `registry.Skills()` 的 name/description/source/front-matter 四列
（`internal/tui/tui_update_commands.go:458-468`），与那行账**不同源**——改渲染时别以为改一处就都变了。

## tokenutil — 降级是一次性粘滞的

`sync.Once` 只加载一次编码器（`internal/tokenutil/tokenutil.go:20,31-37`），首次失败（离线）后 `encoder` 永远为
nil，此后全进程走 `len(text)/4`，warn 只打一次（`:39-49`）。对中文这是约 4 倍低估，会让压缩
触发点系统性偏晚（口径见根 AGENTS.md §6.5）。
`internal/blueprint` 刻意不 import 本包（`check_blueprint_leaf.sh`），计数器由装配层作参数传入。

## gitinfo — ctx 必须 `WithoutCancel`

`Collect`/`Status` 都从 `context.WithoutCancel(ctx)` 派生预算（`internal/gitinfo/gitinfo.go:65`、`:118`）。直接拿
调用方**已取消**的 ctx 跑 git，得到的是"全空身份、不报错"和"dirty=false 但其实没跑过"
（`:57-60`、`:110-113`）。闸门：`TestCollect_SurvivesCancelledContext`（`internal/gitinfo/gitinfo_test.go:130`）。
时间上界整批 2s / 单次 1s，脏文件清单截断 200 条（`:26,29,102`）——否则检查点比它保护的会话还大。
本包必须是叶子包：两个 `session_meta` 写入点分属 `agents` 与 `agents/director`，后者不得 import 前者。

## util — 原子写只准有一处

`internal/util/atomic.go:39-41` 是全仓唯一的 tmp+rename 内核，由 `check_no_inline_atomic_write.sh` 守着。
守卫有两个盲区（脚本 `:29,37-38`）：判定要求同文件同时出现 `os.Rename(` 与写 `*.tmp`，
所以临时名不含字面 `.tmp` 的第二份实现抓不到；扫描范围只有 `internal/` 非测试文件。
选项有牙齿：`TmpSuffix` 结果不得命中调用方目录的列举扩展（`internal/util/atomic.go:19-22`，blackboard 只认
`*.md`）；`DirPerm=0` 保持"目录不存在就失败"（`:12-14`）；`InheritPerm` 是 config 的 chmod 保留规则。
`numeric.go` 与 `error_utils.go` 的 `NewErrorWithContext` 是死代码（全仓零调用）。

## recovery — 查询本身就是那次试探

`Allow()` 带 open→half-open 迁移副作用，half-open 只放一个探针（`internal/recovery/circuit_breaker.go:33-66`），
而消费方把查询写成 `return !r.breaker.Allow()`（`internal/agents/director/recovery.go:74-78`）。加新查询点前先数有几个调用点在读它（根 AGENTS.md §11 坑 36）。
`probeAt` 必须在 Success 与 Failure 两处都清零（`:69-88`）；判据是 `failures >= threshold`（`:100`）；
`Success()` 在 open 态也要闭合（`:80-84`，并行委派下另一条链可能先返回）；`Reset()` 同时清 state 与
failures（`:119-125`）。`SetLastFailure`（`:127-132`）注释写明"用于测试"，生产不许调。闸门：`TestCircuitBreaker_HalfOpenAdmitsExactlyOneProbe`（`internal/recovery/circuit_breaker_test.go:208`）。

## pause / supervise — 见根 AGENTS.md §6.2 与 §9.2

`internal/pause`：闸门不取消 ctx、`Wait` 对 nil receiver 安全、`AbandonedError` 必须同时
`errors.As` 且 `errors.Is` 到 `context.Canceled`（`internal/pause/gate.go:30-37`、`:83-107`）。
`internal/supervise`：叶子包（`check_supervise_leaf.sh` 判 `go list -deps` 里不许出现任何 `yagent/` 包）、
裸 `go` 全仓硬禁且**没有 PENDING 名单**（`scripts/check_no_bare_go.sh:1-12`）、`Drain` 不制造取消（`internal/supervise/supervise.go:156-158`）。
两条使用纪律（批次仍用本地 WaitGroup、执行体不进自己的域）见 `internal/agents/AGENTS.md` 与
`internal/http/AGENTS.md`。

## artifact — ID 形态是安全边界

`^\d{8}-\d{6}-[0-9a-f]{8}$` 在 `Load` **和** `write` 两处强校验防路径穿越（`internal/artifact/store.go:20-22,113-118,147-150`）；
反序列化后必须校验 `schema_version == 1`（`:130-132`）。摘要必须带 ID，而 ID 要 `Save` 之后才有
→ `Save` 后 `UpdateSummary` 回写是既定两步（`:101-110` + `internal/agents/result.go:168-178`）。
`EstTokens` 是确定性纯函数（ASCII/4 + 非 ASCII×1，`internal/artifact/summary.go:9-27`），判定与截断同一口径。
落盘只在**超预算**时发生：`EstTokens(交付全文) <= DefaultBudgetTokens` 就全文即摘要、不落盘
（`internal/artifact/summary.go:29-34` 的 500/60 两个常数，判定在 `internal/agents/result.go:136-137`）；
这个预算**没有配置键**，判定与降级摘要共用同一常数（`result.go:137,174`）。摘要优先取合规 handoff
（`Degraded == ""` 才算合规），取不到才逐字回落到 `Reduce` 的正文前缀（`result.go:167-175`）；
`Save` 失败时降级成全文摘要、不返回 error、不阻断委派（`result.go:149-165`）。
本包无 TTL/GC：全文只增不删。kill-switch 是 `YAGENT_ARTIFACT_DISABLE=1`（`internal/artifact/summary.go:87-91`），
它的落点在采集侧——关掉就是不落盘，`Text`/`Summary` 双双退回 `exec.Text`（`result.go:123-134`）。

## datamanager — 不是 /resume 的事实来源

任务账本 `~/.yagent/tasks/*.jsonl`，5s 防抖；**rollout 才是断点续跑的事实来源**（见
`internal/memory/AGENTS.md`）。`FlushAll` 不许持 `dm.mu` 做 IO（锁内只拿 writer 快照，
`internal/datamanager/data_manager.go:270-273`），且**首错不得提前 return**（`:273`，否则后面 N-1 条任务记忆凭空蒸发）。
增量判断与 pending 收集必须来自同一次 `Snapshot()`（`:198-207`）。索引只描述磁盘状态，文件没写成就
不更新索引（`:127-129`）。`persistIndex` 只走 `util.AtomicWrite` 且**不建目录**（`internal/datamanager/index.go:274-276`）。
计时 seam：`taskFlushDebounce`（`:24-27`）、`flushHook`（`:140-142`）。

## thinklink — 第一条用户输入永不淘汰

`KindUserInput` 的第一条是终极压缩重建的底线信息（`internal/thinklink/thinklink.go:57-60`，淘汰实现 `:255-270`）。
`Snapshot()` 必须深拷贝（`:114-120`）。`ParseKind` 认不出来必须返回 `false`，不得回落默认值
（`:37-41`——把 thought_plan 错认成 user_input 会让重建输出看起来完全合理）。
`RebuildPrompt` 的 `[TP-n]` 全局序号要用淘汰偏移换算（`:204-234`），`keepPlans<=0` 是"全部保留"。
`Seed` 不可省：未 seed 的重放会话走到第三档只能拿原始任务输入重建（`:132-146`）。
`<thought>` 解析失败**不报错只静默丢失**（`internal/thinklink/thoughtplan.go:61-63`）。
"追加记录"说的是写入方式，不是无限增长：Store 有 `DefaultMaxEntries = 200` 的条数上限、超出按最旧
优先淘汰（第一条 `KindUserInput` 除外，`internal/thinklink/thinklink.go:37-38`、`:64-66`），
所以"重建原料"从来是最近 200 条而不是全部。落盘副本走 rollout 的 `thinklink_entry`，
`ParseKind` 就是那条反序列化路径的入口。

## mcp — 已有局部文档，这里只留指针

`internal/mcp/` 现在有自己的 `AGENTS.md`（分页发现、`*bool` annotations、`RequestTimeout` 兜底只在一处、
状态五档与 `StatusLines()` 单一出口、两条 readLoop 死锁纪律、假服务器 = 重新执行测试二进制）。
导览不再复述——上面表格是入口，正文在 `internal/mcp/AGENTS.md`。

## models — 查不到就返回 0

绝不兜底成"看着差不多的那个"；三级顺序 完整 id → 裸名 → 相似度（`internal/models/catalog.go:104-136`），
模糊命中首次必 warn。`fuzzyMinSimilarity = 0.6` 是实测钉住的两头（`:23-26`）。先按 id 排序再建索引，
并列固定取 id 字典序更小者（`:81-83,186-203`）——否则 map 遍历顺序让压缩阈值每次启动都抖。
`ClassifyTiers` 的可打分候选 <3 或基准交集为空时必须 `ok=false` 且给 `Reason`（`internal/models/score.go:72-100`）。
`dist/data/models.json` 是被 git 跟踪的产物，改完要 `git add -f`（根 AGENTS.md §11 坑 2）。
