# AGENTS.md 瘦身与"随委派注入局部文档"通道

> 状态：定案待实施。触发原因是根 `AGENTS.md` 长成 84,484 字符（约 53k token），而
> `internal/agents/director/project_context.go:42-72` 把它**逐字、无上限**注入每一次 Director
> 会话（同一实例只读一次，但一个会话就是整个文件）。这不是"文档不好看"，是产品行为：
> 压缩阈值按 `AGENTS.md` §6.5 是 `min(目录 limit.context × 0.75, 配置)`，120k 档的阈值里
> 有四成在第一条用户消息之前就被这个文件吃掉。

## 1. 判据：什么样的句子有资格留在根文件

根文件今天同时是三份副本里最差的一份——它自己在 §3.1 写的两条规则它都违反了
（"同一句话不要同时写在两处"、"不写模块列表 / 文件计数 / 依赖边"）。修订后的唯一准入判据：

> **留在根文件的充要条件是：这句话会改变一个跨模块的决策，或者告诉读者去哪儿查。**

具体分四类，每类都有反例：

| 类别 | 留 | 走 |
|------|-----|-----|
| 架构形状 | 消息流、两个循环的分工、暂停三层 A/B/C、事件总线两个坐标、持久化路径表 | 某个包内部怎么实现（`internal/*/AGENTS.md`） |
| 全局不变量 | goroutine 只走 `supervise`、锁不跨 IO、每个 tool_call 必有一条结果、配置项必须"改值能失败" | 单包才碰得到的那条锁、那个 seam |
| 路由 | "进目录前先读它的 `AGENTS.md`" + 哪些目录有 | 模块清单本身（blueprint 每轮派生） |
| 致命坑 | **只留索引**：一句判据 + 归属指针 | 45 条坑的叙事——嵌套文档里已有一致且带行号的版本 |

反例的量化：§11 的 315 个去重标识符里 168 个（53%）已经出现在某个嵌套 `AGENTS.md` 中；
抽查之下真实语义覆盖比这个数更高（例如坑 42 的散文路径判定，
`internal/blueprint/AGENTS.md:36` 的硬约束 6 是同一件事的更精确版本，只是没复述那些字面 token）。
所以"字符串覆盖对账"只能当筛查工具，真正的无损证明靠 §4 的**先写后删顺序**。

## 2. 定案：三层，每层只有一个作者

```
根 AGENTS.md            跨模块决策面 + 路由表          目标 ≤ 480 行 / ~17k token
internal/**/AGENTS.md   包内不可派生约束（边界/硬约束/入口/闸门）   已存在 17 份，吸收根文件让出的内容
产品运行时              委派前缀注入"目标模块的局部文档"    新增，见 §5
scripts/…-size.sh       棘轮：超限即红，改小了必须同步下调     新增，见 §6
```

四条职责边界是硬的：

1. **嵌套文档不许引用根文件里已经被删掉的段落**。今天 17 份嵌套文档里有 13 份共 **24 处**
   写着"根 AGENTS.md §11 坑 N"（`grep -rho '根 AGENTS.md §11 坑 [0-9/]*'` 实测），根文件收缩后
   这些指针必须改成指向**它自己包内的硬约束条目**，否则死链从文档转移到文档内部。
2. **`docs/` 不再是知识层**。根文件目前引用 13 份设计稿，其中 11 份已不存在（`bbc1856` 删的）：
   `repo-blueprint-design.md` / `structured-concurrency-design.md` / `pause-resume-design.md` /
   `config-trim-onboarding-design.md` / `blackboard-handoff-design.md` / `tool-registry-design.md` /
   `browser-agent-design.md` / `compression-trigger-design.md` / `token-dashboard-design.md` /
   `token-compress-panel-design.md` / `setup-wizard-ui-design.md`。**所有"完整设计见 docs/X"必须改判**：
   内容进嵌套文档，引用删除。只有 `docs/config-guide.md` 与 `docs/skills-and-mcp-design.md`（+ 实施篇）
   是活的，可以继续引。
3. **不再新增 `docs/*-design.md` 作为落地后的知识归宿**——本文件自己就是例外：它记录的是
   这次改动的判据，落地后如果只剩"根文件为什么是 480 行"这一句话有用，那句话应该进 §3.1 的表，
   本文件随之删除。
4. **§3.1 那条"运行时不递归"的描述会变**：`internal/tui/AGENTS.md` 等 15 份今天只服务于
   读代码的人和外部 agent。§5 的通道落地后，这句话必须改成真实状态（哪个包在什么时机注入什么），
   不许留旧事实。

## 3. 落点表：逐节处置

"下沉"= 内容进指定嵌套文档（写成它的硬约束条目，带 `file:line` 与闸门测试名）。
"删除"= 嵌套文档已有等价或更好的版本，根文件这份是副本；或可从代码/git 派生。

| 节 | 现状 | 处置 |
|-----|------|------|
| 前言 changelog（57 行） | 2.1k tok，七轮"上一轮…"叙事 | **删除**。git log + 设计稿的事。里面唯一的硬约束（`[blueprint] export_path` 无消费者、别写进配置指南）搬进 §7 配置节那一行 |
| §1 概述 | 0.5k | 保留，压到 25 行 |
| §2 技术栈 | 1.3k | 保留（选依赖是真决策）。`2.2 外部依赖`与 `2.3 LLM 接入`下沉 `internal/llm/AGENTS.md` + `internal/tools/AGENTS.md`（rg/browser-control 的查找顺序归 tools） |
| §3 模块地图（110 行） | 3.3k tok 的文件树 | **删除整棵树**。blueprint 每轮派生，且根文件自己禁止。只留 §3.1 的路由约定表 + "哪些目录有局部文档"（该表与 `internal/AGENTS.md:8-28` 互为镜像，合并成一处，由 `internal/AGENTS.md` 持有，根文件只指过去） |
| §4 Agent 角色 | 1.4k | 保留 Agent 表（委派是真决策）。三段工具清单（Director/Coding/Repo/Browser）**删除**——`toolspec/profiles.go` + `testdata/tool_surfaces.golden` 是唯一权威，抄进文档就是会漂走的第二份 |
| §5 核心工具 | 2.8k | 保留 5.1 的并行性判定与两道闸门（跨模块）；5.2 工具总览表压到类别一行；5.3 MCP 的形状保留、实现细节下沉 `internal/mcp/AGENTS.md`；5.4 安全机制表保留 |
| §6 系统架构 | **12.3k tok** | 这是最大一块。保留五件"形状"：消息流图、两个循环的职责差异、暂停/检查点/重放三层（§6.2 后半的 per-package 细节下沉）、auto-routing 优先级（§6.3）、持久化路径表（§6.7）。§6.4 上下文与知识四个子系统（blackboard / artifact / blueprint / skills）各留 3-5 行判据 + 指针，正文下沉对应嵌套文档；§6.5 压缩留阈值口径与"一个开关"，档位细节下沉 `internal/compression/AGENTS.md`；§6.6 事件总线留 `Seq`/`DelegationID` 与丢弃记账，consumer 内部细节下沉 `internal/messaging/AGENTS.md`；§6.8 TUI / §6.9 HTTP 各压到 8 行 |
| §7 配置系统 | 4.8k | 保留：解析顺序、三级 provider 覆盖、全部 TOML 块（**TOML 块本身就是契约**，删了没人写得出配置）、`removedKeyHints` 承诺。删：每段后面复述同一规则的"存在性判定"长注（`internal/config/AGENTS.md` 硬约束 2 已持有）。`export_path` 无消费者那一句留 |
| §8 开发与构建 | 3.1k | 保留命令与测试纪律（跑什么、计时 seam、不要写不会失败的测试）。E2E 五条链路的清单下沉 `internal/agents/AGENTS.md` 的"闸门"节（已在那里） |
| §9 编码规范与不变量 | 3.3k | 保留九把守卫清单 + goroutine/锁/装箱 interface/ctx 让路四条全局不变量。9.3 新增工具的六步保留（这是跨包流程）。`ConversationMemory` 封装细节下沉 `internal/memory/AGENTS.md` 硬约束 3（已在） |
| §10 Director 行为准则 | 0.7k | 全保留——它是产品行为契约，且没有别处写 |
| §11 已知坑（45 条，285 行） | **11.6k tok** | 变成**索引**，不是叙事。每条一行：`N. 一句判据 → 去处`。去处三种：嵌套文档的硬约束条目号、闸门测试名、留在根（仅跨模块的那批）。目标 45 行 / ~2.5k tok |

§11 的 45 条按去处分堆（实施时逐条核实"去处确实写着同一件事"，不信任这张表）：

- **留根**（跨模块判据，无单一包归属）：1（旧文档过期，压成两行）、5、11、15、18、23、25、33、35、36、38、39、40、43、44、45
- **`internal/tui/AGENTS.md`**：6、7、28、29、30、31、32、34
- **`internal/compression/AGENTS.md`**：3、4、20（`Reasoning` 计入指纹的两半之一）
- **`internal/config/AGENTS.md`**：13、14、26、27
- **`internal/memory/AGENTS.md`**：19、20、21、22
- **`internal/agents/AGENTS.md`**：12、16、17、24、37、41
- **`internal/blueprint/AGENTS.md`**：42、43 的包内那一半
- **`internal/mcp/AGENTS.md`**：44、45 的包内那一半
- **`internal/browserctl/AGENTS.md`**：8、9、10
- **`internal/tools/AGENTS.md`**：2、38 的包内那一半

## 4. 实施顺序与实测结果（已落地 2026-10-24）

S1 补嵌套文档 → S2 对账 → S3 重写根 → S4 注入通道 → S5 守卫，逐步做下来得到的数：

| 项 | 结果 |
|-----|------|
| 根文件 | 84,484 → **32,085 字符**（609 行），估算 **53k → 18k token**（-66%） |
| 嵌套文档 | 17 份 → 共 2,665 行（吸收根文件让出的内容；根之外的总量是涨的，**这正是设计意图**：它们不进运行时上下文） |
| 对账 | 旧根 965 个标识符 → 逐 atom 匹配后残留 191（19%），全部人工分堆：可派生（工具名清单、文件树）、已作废（`world_state`、`ParseEventPayload`、`bedrock`、`consolidate_knowledge`、11 份死 `docs/`）、或**已补回** |
| 补回的缺口 | 8 条：token 口径不对称（→ llm）、重放三个回填点（→ agents）、`toolbatch` 不许从批次内部取消（→ agents）、流式槽三张表 + 渲染行数三道闸门 + 向导必填集合双钉（→ tui）、`handoff.status` 全词表（→ blackboard）、事件清单与 `stop_reason` 词表、`go/parser` 那句、卫生守卫的 `panic-guard` 标记（→ 根） |
| 顺带证伪的根文件旧说法 | i18n 字段 **69 个**（不是 ~120）；`-y` **绕过全部授权**含工作区外（`-Y` 的净差异只有摘 `ask_user_for_help` + 自主指令）；`validate()` 只硬校验**生效** provider；`looksLikeCancelledContext` 与"toolbatch 取消短路"已不存在；`ScanResumeCandidates` 住在 `internal/agents/resume.go`；坑 20（`Reasoning` 进 rollout）**为真**；自扫 golden 是 **43 片/137 边**；嵌套文档是 **17 份**（根原写"16 份"） |

**行数没打到最初定的 ≤480**：45 行坑索引、§7 的 TOML 契约块、§6.7 的持久化表都是"一行一件事"的形状，
硬凑行数就得删判据。真正的成本口径是 token（18k，最初估的 17k 差不多），棘轮因此钉在**实测 608 行**，
并把"编号是契约"写进根文件头（17 份嵌套文档有 25 处 `§11 坑 N` 与 `§x.y` 指回，重排编号会一次性打断它们）。

## 5. 通道（as-built）：委派前缀注入目标模块的局部文档

`internal/agents/module_docs.go`（package `agents`，与 `blueprint_view.go` 同侧——放 `director/` 子包
就得为一条窄接口绕一圈，没有收益）。接入点 `internal/agents/director.go:packDelegateTask`，
在蓝图散文之后、`YAGENT_BLACKBOARD` 判据之外，`a.env != nil` 才调（有测试用结构体字面量构造 DirectorAgent）。

与草案的**一处实质差别**：没有做"扫一棵目录树拿封闭集合"，而是**拿任务里的路径 token 去问磁盘**——
`moduleDocsDirs`/`moduleDocsRel` 只把 token 变成相对工作区根的目录链，`readModuleDocs` 逐个
`EvalSymlinks` 确认"<dir>/AGENTS.md 真的存在且仍在工作区内"。判据仍然是封闭集合，只不过分母由
文件系统当场给出；这比"再扫一遍树"少一份需要失效判据的状态，召回率相同（目录没在任务里出现，
两种做法都注入不了）。

四条硬形状：

- **祖先链最深优先**，命中 `internal/agents/director/` 时依次取它、`internal/agents`、`internal`，
  **不含**工作区根那一份（已在系统上下文里）。
- **预算** 3 份 / 合计 12 KB，包内常量 + 一条超限夹具；超预算在末尾写
  `> [module docs] N file(s) … omitted (budget 12.0KB per delegation)`——静默少一份文档
  与"这个包没写过约束"同形（§11 坑 43 那一类）。**不开配置键、不加环境变量急停**。
- **顺序确定**：按 (深度 desc, 字典序)，与 token 在任务里出现的先后无关（前缀缓存靠字节稳定）。
- **正文每次现读、只缓存判据不缓存内容**（同 `blueprint_view.go:123-135` 的理由）。
  软链指向工作区外的文档被拒——守卫在**读取侧**（`EvalSymlinks` + 前缀包含），token 侧的
  `..`/绝对路径拒绝只是第二层。

**闸门（7 条，各自做过变异）** `internal/agents/module_docs_test.go`：
`_MatchesClosedSet`、`_NearestWinsAndNeverCarriesRoot`、`_BudgetTruncationIsVisible`、
`_RejectsPathEscape`、`_SkipsSymlinkOutsideWorkspace`、`_NeverCachesBody`、
`TestPackDelegateTask_CarriesModuleDocs`。变异结果：删掉 `packDelegateTask` 里那一行调用 → 链路用例红；
预算判定改恒真 → 截断用例红；排序改成浅先 → 最深优先用例红；去掉软链包含检查 → 软链用例红。
（第四条变异"删掉 token 侧绝对路径拒绝"**没有**让任何用例红——因为真正的防线在读取侧，
删掉冗余的第二层不是行为回归。这条留在这里是为了说明：一个测试不该被要求对每个内部守卫都敏感。）

## 6. 守卫（as-built）：`scripts/check_agents_md_size.sh`

已接进 `scripts/check_all.sh:37` 的 guard 循环（九把变十把，脚本头部注释同步改成"十把"）。
形状照 `check_file_sizes.sh`：`PENDING` 只登记**超过硬上限的存量**——
`AGENTS.md:608`、`internal/tui/AGENTS.md:226`、`internal/agents/AGENTS.md:212`；其余 15 份由
`NEW_FILE_MAX_LINES=200` 直接管着（长过 200 才需要显式立项，立项就是承认一笔要还的债）。
超限即红、长回去即红、**变小了报失步**、名单里有已不存在的项也报失步。
口径是行数而不是 token：脚本里跑不了 tiktoken，而这次膨胀的真实形状是"段落变长"。
负例已实测：往根文件追加一行 → 立刻红（610 > 608）。

## 7. 明确不做

- 不动 `docs/ARCHITECTURE.md`（89KB）与 `ARCHITECTURE.en.md`（94KB）。它们没人自动读，
  膨胀不影响运行时上下文；根文件 §11 坑 1 已经把它们标成过期。要不要删是另一个决定。
- 不给 `project_context.go` 的注入加字节预算。选了棘轮而不选预算：预算的坏表现是
  "细节被砍掉而模型不报错"，正是这个仓库最恨的静默失效（§11 坑 25/43 同一类）。
- 不给局部文档通道加配置段、加 TUI 面板、加 `read_*` 工具。

## 8. 验收（实测）

1. `bash scripts/check_agents_md_size.sh` 绿；根文件 609 行 / 32,085 字符（冻结上限 608，只许减）。
2. `bash scripts/check_all.sh` 全绿：build + vet + 十把守卫 + 全量单测（26 个测试包）。
3. 对账差集已分堆（§4 表），8 条真实缺口全部补回，其中 4 条补进嵌套文档、4 条回到根。
4. `grep -rn "docs/[a-z-]*\.md" AGENTS.md internal/` 只剩活引用（`config-guide.md`、
   `skills-and-mcp-design.md`）+ 五处"已不存在"的自述。
5. 所有 `§` 指针可解析：脚本核过——根小节 `2.1-2.3 / 5.1-5.4 / 6.1-6.9 / 8.1-8.3 / 9.1-9.3 / 11`
   全部在，被引用的 19 个坑号在 `§11` 全部存在，唯一残留的 `§21` 是指向已删设计稿的死链（已改）。
6. **只能由用户在真终端跑的一条**：一次真实委派（`-p` 非交互，任务文本里点名某个包目录）
   在 rollout 里应能看到 `# Module Constraints` 头部与 `### <dir>/AGENTS.md` 段。
   我没有跑过真终端，这条**未验证**。

## 9. 这份文档的去留

它记录的是"这次改动为什么长这样"。§3.1 的写法约定、`§11` 的编号契约、
`check_agents_md_size.sh` 的头部注释已经各自承载了落地后还需要读的那几句，
所以本文件现在可以随时删除——留着也不会有人来读它。**要留就要有人负责在下次改动时更新它**，
否则它就是本文件第 2 节自己要防的那种东西：第二份会漂走的事实。
