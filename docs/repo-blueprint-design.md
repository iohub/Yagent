# 仓库蓝图（Repository Blueprint）设计 v4

> **本文是实施规范**，读者是执行改动的 Coding-Agent。
> 核对时间 2026-10-17（`main` @ 2b16f3a）。所有 `file:line` 都经本次实测核过。
> **本文取代 v3**（2026-10-17 上午，599 行，三条待签已签字，文件已由作者删除并要求重推）。
> v4 不是"重写一遍"：它推翻 v3 三条、沿用 v3 七条、新增三条，每条都在 §0 记账；
> 沿用部分的价值在于 v3 的结论是**量出来的**，而 v4 重测了一遍（§1.3）。
> 前置阅读：`AGENTS.md` §6.4（Blackboard/Artifact/RepoMemory）、§9.3（新增工具六步）、
> §11 坑 11（索引必须是派生态）、坑 16（挂在"必然发生"的调用上）、坑 25（配置项存在 ≠ 生效）、
> 坑 26（布尔键的缺省为真靠键存在性）。

---

## 0. 与 v3 的账目

### 0.1 v4 推翻 v3（三条，各带依据）

| v3 定案 | v4 | 推翻依据 |
|---|---|---|
| **定案 2**：只有语义落盘，落点是 `modules/<路径镜像>.md` 一模块一文件 | **一份 `blueprint.md`**（§3），模块注解作为 `## Modules/<path>` 子节住在同一文档里 | 用户这次的要求原文是"给项目生成蓝图（**markdown 文件**）/ 更新当前项目蓝图"。一模块一文件回答的是"数据库该怎么存"，回答不了"给我看这个项目的蓝图" —— 那需要 N 次 `cat`。单文档同时保住行级可寻址（更新仍是外科手术）与"一份可读可 diff 的产物" |
| **§15.2 第 8 条**：v1 不做"蓝图提交进仓库" | **做，但只是导出**：真源在 `~/.yagent`，`export_path` 非空时同步重渲染一份进工作区（§9） | 用户本次重新拍板选了"双层"。v3 拒绝它的理由（脏工作区、被 stash 收走）今天仍然成立并被写进 §9.3，但**可幂等重生成的渲染产物**把这些代价降成"暂时看不到" |
| **定案 4**：与现有三套仓库知识**并存**，删 KV 记为待还之债（§15.2 第 5 条） | **本轮还债**：`RepoMemoryStore` / `ConsolidationWorker` / `MaxMemoryTokens=1500` 连 `memory.SharedMemory` 一起退休（§12） | 用户本次拍板"收编删除"。技术上 v3 自己已经把动作写好了（"三个 sink 共用一个接缝，摘掉一个不动其余"，`repo.go:200-209`）；今天复算发现摘掉还能连带删掉一整具没人认领的骨架 —— `SharedMemory` 在生产里的**唯一**用户就是这份 KV（`app.go:433`），且它的持久化 goroutine 从来没被停过（§14） |

### 0.2 v4 沿用 v3（七条，都是实测或结构性判断）

1. **结构层不落盘**（v3 §0.1 的第一条推翻）。v4 复测：一次单遍扫描 + 238 个文件的 `go/parser` ImportsOnly = **6.6ms**（walk 1.6ms + parse 5.0ms）。可派生的东西一旦落盘就需要一套新鲜度机器。
2. **边界一句话：蓝图拥有 目录→目录，codeseek 拥有 符号→符号**（v3 §1）。骨架不写行号、不写函数签名、不做 embedding。
3. **注入形状 = 骨架②（spine+edges），散文只在按需路径**（v3 定案 6 已签字）。v4 复测牌价：① 882 字符 ≈ 220 token，② 3496 字符 ≈ 874 token（42 片、103 条边、出 4 入 3、单空格分隔）。
4. **写入通道挂在 `agent_exit` 的新参数 `blueprint[]` 上，不是新工具**（v3 定案 10）。依据是坑 16：挂在"模型记得调"的工具上 = 静默失效的形状；`agent_exit` 是 Repo 收尾必然发出的那一个调用，且采集点已存在（`result.go:90-99`）。
5. **证据集校验**（v3 I17）：提交的模块必须属于本次 run 真打开过的文件所属模块集，且**入参侧与出参侧都要算**（v3 §7.3 第 2 条记录的理由照搬）。
6. **三臂价值验收程序**（v3 §13.1 + §16）：主指标 M1 = 首次委派之前的"自我定位自读"次数下降 ≥40%，M4 = 死路径率，M5 = 缓存命中率，M6 = 语义覆盖率；终止条件 = 只有 B 达标而 C 不达标就砍掉散文层。
7. **蓝图正文一律英文，不跟 `[agent] lang`**（v3 §6.2）。依据是 `tokenutil_test.go:26-38` 那两条断言：cl100k 下中文 ≈1 token/字、英文 ≈4 字符/token —— 同一句话中文贵 3-4 倍，而这个字段的读者是模型。（对比 AGENTS.md §10.2：`<thought>` 的值跟配置语言，因为那是给人看的。）

### 0.3 v4 新增（三条）

- **散文原子带 `where` 出处锚点，并因此获得第二条新鲜度判据**（§5.2）。v3 只有结构指纹，它答不了"这个模块结构没变、但这句话已经说错了"。
- **`supersedes` 取代机制**（§7.4）：模型看到既有注解时能显式指认它被自己推翻，程序做摘除手术。v3 的合并规则是"逐字段覆盖"，同一模块的说法来回摆动时没有裁决手段（v3 §14.1 自己点出了摆动风险）。
- **多进程 fold 的乐观 CAS**（§14）：v3 §12 只管单进程内四类访问者，没管两个 TUI tab 同时写同一个项目。

---

## 1. 为什么需要它

### 1.1 codeseek 给坐标，不给方向

六个符号级工具（`internal/tools/repo_operations.go`）返回的全是符号坐标：

| 工具 | 行 | 入参 | 得到 | 拿不到 |
|---|---|---|---|---|
| `semantic_search` | :108 | `query`,`limit` | `hits{file_path,line_start,line_end,symbol_name,…}` | 这个目录归谁管 |
| `query_code_skeleton` | :291 | **`filepaths` 绝对路径数组** | 定义不含实现 | 路径本身必须先知道 |
| `query_code_snippet` | :327 | `filepath`+`function_name` | 单个符号全文 | 同上 |
| `find_function_callee/caller` | :352,:378 | `function_name` | function→function 边 | 目录→目录的依赖 |
| `query_call_graph` | :410 | `function_name`,`depth≤3` | 符号调用树 | 同上 |

**缺口是"从 0 到 1 的路径发现"**，而最强的两个导航工具要求模型已经知道路径 —— 鸡生蛋。两个放大器：
Director 的工具面里一个 codeseek 工具都没有（`internal/agents/testdata/tool_surfaces.golden`）；
`repo.prompt.md:50` 让 Repo 每次输出"mental map"，`director.prompt.md` 又要求用这张地图兜地、绝不猜路径 ——
**这份地图每个任务重算一次、用完即弃。**

### 1.2 但要诚实区分"新价值"与"已有能力的重复"（v3 §1.1 的表，v4 用今天的数复算）

| 蓝图内容 | `print_dir_tree` 能给吗 | codeseek 能给吗 | 结论 |
|---|---|---|---|
| 目录形状、文件数 | ✅ | — | **零边际价值**，只是预先算好、常驻 |
| **目录→目录依赖边**（今天实测：121 条目录级、聚合成 **103 条模块级**，边集摘要 `d7dd46cd2ed9`） | ❌ | ❌（只有符号→符号） | **唯一无可替代的确定性事实** |
| 入口点（`package main`，AST 验证） | ❌ | 间接 | 有价值 |
| 构建/测试命令 | ❌ | ❌ | 有价值但靠启发式，必须标 `*_source` |
| 模块 purpose / data_flow / 新功能落哪 | ❌ | ❌ | 有价值，但**是未验证散文**（§18.2 第 4 条） |

这张表决定了三件事：常驻预算优先花在可验证的那部分（边）；散文放按需路径；
以及 **v1 只做 Go 的边抽取 ⇒ 非 Go 仓库的蓝图退化成 `print_dir_tree` + 散文**。

### 1.3 本次复测的规模事实（本仓 HEAD 2b16f3a，一次性程序，见 §19 附录）

```
非测试 .go 文件 238 · 含码目录 42 · 遍历目录 55 · 模块级边 103
单遍扫描 1.6ms · go/parser ImportsOnly ×238 = 5.0ms · 合计 6.6ms
形状①(spine)       882 字符 ≈ 220 token，最长行 31
形状②(spine+edges) 3496 字符 ≈ 874 token，最长行 151
阈值扫描 12/16/24/40 → 47 片；64 → 40 片（复测程序未做 v3 的 docs/config 分类与链折叠精修，片数偏高约 4 片）
```

**结论沿用 v3**：6.6ms ≪ `derive_budget`，所以"重扫很贵"这个前提是假的 —— 结构每轮现算，
git sha 不参与可信度判定（到期条件见 §5.1）。
片数与边数的**权威数字必须由 R2 落地的扫描器重测并写进 golden**：v3 的 43 片 / 119 条边是它的实现算的，
v4 的复测程序更粗（47 片 / 103 条）。两个实现指向同一结论就够用了，但别让设计稿里的数字冒充最终事实。

---

## 2. 现状与根因（v4 补：这条回路今天已经存在，而且是三份）

### R1 仓库级长期记忆不是文件，是 JSON 里的一个字符串

`app.go:433` 建 `RepoMemoryStore(repoID, sharedMemory)`，key = `repo_memory:{projectPath}`（`repo_memory.go:47,97-99`），
序列化进 `~/.yagent/data/shared_memory/{projectID}/shared_memory.json`（`shared.go:282-312`，路径 `app.go:941-955`）。
用户看不到、git 追不到、无法 diff、没有版本；唯一的历史是一条按天追加的日志（`consolidation_worker.go:176-214`）。

### R2 1500 token 上限是"整份重写"逼出来的

`repo_memory.go:45` `MaxMemoryTokens = 1500`，超预算 `EnforceTokenBudget` 按 `\n## ` 从尾部逐段砍（`:171-191`），
而合并提示词第 8 条把丢弃顺序钉死（`consolidation_worker.go:334`）。根因：合并是"旧记忆 + 新观察 → 重新输出整份记忆"
（`consolidation_worker.go:260-268`）。模型必须重发全文，所以文档越大越不可靠，上限只能压到 1500。**形态决定容量。**

### R3 输入不分轻重

`repo.go:200-204` 无条件提交全文，而 `repo.prompt.md:4-12` 明确要求优先 Quick Lookup。
快查琐事持续稀释架构结论，去重全靠模型自觉。→ v4 用证据集 + `blueprint[]` 的申报门槛治它（§7）。

### R4 三份真相并行，而注释说错了

- Repo 读 KV（`repo.go:129-131`）；Coding 读**进程内** `RepoContextStore`（`coding.go:385`，内容由 `director.go:815-826` 写入，`executor.go:206-207` 追加进系统提示）；Director 谁都不读，只把委派结果当 tool 消息（压缩一动就没，正是 §11 坑 11）。
- `repo.go:195-196` 的注释说"注入 coding/browser 系统提示" —— **实际读取点只有 `coding.go:385` 一处**，本仓没有 browser 的仓库上下文注入。文档与代码不符，R4 顺手改正。

### R5 没有任何"这条还成立吗"的判据（KV 那份），v3 的 leaf 也只有结构指纹

→ §5.2 的锚点验旧补上第二判据。

### R6 两条 projectID 实现已经在漂

`app.getProjectID`（`app.go:913-939`，`sha[:12]`）只管 shared_memory；
`director.ComputeProjectID`（`project_context.go:77-100`，`sha[:8]`）管 rollout（`director.go:462,484`）与 artifact（`result.go:144`、`read_artifact_tool.go:87`）。
`assembly_test.go:165` 那句注释写着前者"是共享记忆与 rollout 目录的主键" —— **后半句是错的**。v4 的处理见 §11。

### R7 往工作区写文件的既有代价（为 §9 的导出层而列）

`gitinfo.Status` 用 `git status --porcelain`（`gitinfo.go:114-137`），**untracked 也算 dirty**；
`[git_checkpoint] stash_dirty_worktree` 走 `stash push --include-untracked`（`git_checkpoint.go:161`）；
`ProjectContextLoader.Load` 每实例只读一次并缓存（`project_context.go:42-48,72`），所以"把蓝图写成仓库根的 AGENTS.md 同族文件"在一个进程内不会被自动重读。

---

## 3. 数据模型：一份文档、两层内容、行级原子

### 3.1 派生的不落盘，落盘的不派生

```
派生层（内存，每次现算，实测 6.6ms，不落盘）        持久层（磁盘，只有一份文件可写）
├─ 模块集合与边界（§4.2）                          ~/.yagent/data/blueprint/{projectID}/
├─ 每片：文件数/语言/测试数/生成物                    ├── blueprint.md            ← 只有散文
├─ 103 条模块级边（AST 验证，§4.3）                   │     front-matter(JSON) + 仓库级段 + ## Modules/<path>
├─ 入口点 + manifest 摘要（带 *_source）              └── revisions/{UTC ts}.md   ← fold 前的整档，滚动 5
└─ 每片一个结构指纹（sha256）
```

- **渲染 = 派生层 + 持久层拼出来的字符串**，`read_blueprint` / 注入 / 导出用的都是它；从不解析正文来生成索引。
- 落点根 `~/.yagent/data/blueprint/{projectID}/`（与 v3 同）。判据 I7：存储根**不得**落在项目路径内。
- 写盘复用全仓唯一的原子写内核 `util.AtomicWrite`（R1 落地）。
  **实测比 v3 估计的还多一份**：这类 tmp+rename 实现今天有 **6 份** —— `blackboard/board.go:106`、
  `artifact/store.go:144`、`datamanager/index.go:273`、`config/persist.go:324`、
  `config/default_config.go:32`（模板首次落盘，v3 漏了这一处）、`memory/shared.go:301`。
  R1 把它们全部收成一份内核 + 六处转调，权限与 tmp 后缀作为选项传入（各站点的既有语义逐位保持：
  datamanager 0644、blackboard 0600、config **继承原文件权限**）。
  内核的临时名带随机段而不是 `path+".tmp"`：同一路径可能被两个进程同时写
  （两个 TUI tab 的 history 索引、向导与主进程写 config.toml），固定名会让后来者截断前者的中间态。
  附带约束：tmp 名不能命中各目录的列举扩展名（`blackboard/index.go:129`、`gc.go:131`、`datamanager/index.go:124`
  靠的都是"中间态不长成数据文件的后缀"）。

### 3.2 `blueprint.md` 的形状

front-matter 用 **JSON 不用 YAML**、`---` 定界，与 `blackboard/note.go:239-258` 同形（不引依赖、解析只有一份）。

```markdown
---
{"version":1,"project_id":"yagent_77506fcd","project_path":"/Users/ydl/iohub/yagent",
 "updated_at":"2026-10-17T07:10:00Z","folds":17,"atoms":41,"stale":3,"unanchored":0,
 "sources":{"repo_agent":41},"git_sha":"2b16f3a","git_branch":"main","git_origin":"…",
 "module_fingerprints":{"internal/tui":"9d2c…","internal/agents":"41ae…"}}
---
# Repository Blueprint

## Architecture
- (a7c3) Director is the only user-facing agent; every sub-agent shares RunAgentLoop <!--id:a7c3 where:internal/agents/executor.go:136 src:repo_agent@1f2e3c4d seen:2026-10-17T07:10:00Z state:ok-->

## Flows
- (b2f1) a keystroke goes tui_update_* -> Director.Run -> planner loop -> delegate_* -> sub-agent; results return over the event bus, and listenForEvents is one-shot with no replay queue <!--id:b2f1 where:internal/tui/tui_update.go src:repo_agent@… state:ok-->

## Conventions
## Dependencies
## Gotchas

## Modules/internal/tui
- (5e12) purpose: Bubble Tea v2 frontend; owns chrome, layout kernel, dialog stack, i18n <!--id:5e12 field:purpose where:internal/tui/common/list_metrics.go src:repo_agent@… state:ok-->
- (9c04) add_here: a new slash command goes into processCommand's "/" branch, never into a key handler <!--id:9c04 field:add_here where:internal/tui/tui_update_commands.go:27 src:repo_agent@… state:ok-->

## Modules/internal/blueprint
```

原子一行的规则：

| 部分 | 约束 |
|---|---|
| 可见短 ID `(a7c3)` | **故意渲染出来**：唯一目的是让读到本文档的 Agent 能在下一轮 `supersedes` 里指认它（§7.4）。人类读时它是噪声，代价换"矛盾不并存" |
| 正文 | 一条论断一句一行，**英文**（§0.2 第 7 条）；`## Modules/*` 段的行必须带 `field:`（purpose / data_flow / add_here 三选一） |
| `where` | 出处锚点：`path` / `path:line` / `path:symbol`。空 → `state:unanchored` |
| `src` | `{agent}@{sessionID 前 8 位}`，出处而非权限 |
| `state` | `ok` \| `stale` \| `unanchored`（§5.2） |

**为什么必须是"一行一原子"**：更新是行级手术 → git diff 只动那一行 → 文档能长到 40 行/段而合并仍然可靠。
这正是 R2 的解药：不需要模型重发全文，所以不需要 1500 token 上限。

### 3.3 段落枚举封闭

```go
// internal/blueprint/section.go
SecArchitecture = "Architecture"   SecFlows        = "Flows"
SecConventions  = "Conventions"    SecDependencies = "Dependencies"
SecGotchas      = "Gotchas"        SecModules      = "Modules/"   // 前缀段
```

`Modules/` 是**前缀段**：它的叶子集合 = 本次派生的模块集合（§5.3 的孤儿判据）。
段名英文且固定 —— 它们是索引、验旧、schema 三方的共同 key，不是文案。
`agent_exit.blueprint[].section` 的 schema enum 与代码枚举双向对账（I12）。

### 3.4 承重不变量：模型写不进结构

结构层不落盘 ⇒ **模型没有任何途径写坏它**。可写的只有散文行，而每条散文必须过 §7.5 的五道校验，
其中 `field` 三值与 `section` 枚举把形状钉死。front-matter 的 `git_*`/`folds`/`sources`/`module_fingerprints`
由程序写，提交方写了也会被覆盖（I5）。

---

## 4. 派生层：扫描器（`scan.go`，零 LLM，实测 6.6ms）

规则整体沿用 v3 §4（每条都注明 v4 是否改动）—— 它们是 v3 用一次性程序量出来的，
其中一条还真抓到过 bug（§4.1 的 submodule）。

### 4.1 遍历与排除

- 扩展名白名单**分两类**分别计数：代码类 `.go .rs .c .h .cpp .hpp .py .java .kt .ts .tsx .js .jsx .mjs .vue .proto .sql`；
  文档/配置类 `.md .yaml .yml .json .toml .sh .ps1 .mk` + `Makefile/go.mod/package.json/Cargo.toml`。
  **只有代码类计入规模阈值**；文档类在 leaf 单列一行（`docs: 7 md`）。理由：200 篇文档的仓库不该被切成 9 个"文档模块"。
- 目录黑名单：`.git .yagent node_modules vendor dist build out target __pycache__ testdata .next coverage bin` + 任何 `.` 开头目录。
- **submodule 在目录分支里跳过**（解析根 `.gitmodules` 的 `path =`），命中即整片作一张 leaf 并标 `submodule:true`、不递归。
  v3 §0.2 记的实测教训：把这条写在文件分支上，`codeseek/` 被当普通目录走进去，多出 **14 片假模块**。闸门 I6。
- **符号链接目录一律拒入**（`os.Lstat` + `ModeSymlink`，写法照 `blackboard/gc.go:51-59`）：一次 `ln -s /` 就能把扫描器带出项目根。
- `*_test.go` / `tests/` / `test_*.py` 计入 `tests`，不计入阈值（1:1 配测试的仓库否则永远拆不完）。
- 生成物按 stdlib 头行识别（前 4KB 匹配 `^// Code generated .* DO NOT EDIT\.$`），计入 `generated`，不进文件列表。
- **根护栏**：项目根是 `$HOME`、`/`、或"既不是 git 仓库也没有任何 manifest"时**拒绝建蓝图**并说明原因（I16）。
  `max_scan_entries` 只能救慢，救不了"在家目录里扫出 3000 个模块"。
- 硬预算：`max_scan_entries` 20000、单次扫描墙钟 2s；超限停在当前层，该片标 `TRUNC` 并在索引行显示（I4）。
  **宁可说"没看完"，不可静默少一片。**

### 4.2 规模阈值与折叠（v3 §4.2 伪代码原样沿用）

```
partition(dir):
  children = 含 ≥1 代码文件的子目录
  own      = dir 自身的代码文件（不含测试/生成物）
  total    = len(own) + Σ(child 递归前计数)
  if depth >= 4 or total <= max_files_per_module: emit leaf(dir)
  if len(own) == 0 and len(children) == 1: recurse(child, depth+1)      // 链折叠，不 emit
  else: if len(own) > 0: emit leaf(dir, 只覆盖 own); for c: recurse(c, depth+1)
```

v3 的三条实测结论在 v4 仍然成立（v4 复测：12→40 片数不动、64 才明显合并）：

1. **阈值对片数几乎不敏感** ⇒ 别把调这个键当"索引太大"的自救手段；真正的手段是 §6 的形状选择。取 40（"零超阈值片"的最小值）。
2. **存在按路径切不动的片**（v3 实测 `internal/tui` 39、`internal/agents` 36 个自有文件）：照发，`files` 列表按常数 24 截断并显示 `+15 more`，索引行标 `oversize`。**骨架的能力上限必须说出来**，否则模型以为那 24 个文件就是全部。
3. **"把小片折进父片一行"实测否决**（能把 41 行压到 26 行）：代价是把 `internal` 下 19 个各自成体系的目录压成一行。**行数省下来的钱买不到导航。**

### 4.3 事实与可信度分级（v3 §4.3 沿用）

| 事实 | 怎么得 | `*_source` |
|---|---|---|
| 目录→目录边 | `go/parser` ImportsOnly + `go.mod` module path 前缀 → 目录 → 聚合到模块；今天实测 121 条目录级 → 103 条模块级 | `ast` |
| Go 可执行入口 | 同一次解析已拿到包名，`package main` 即入口，零额外成本 | `ast` |
| Rust/Node 入口与脚本 | 文件名 + `Cargo.toml [[bin]]` / `package.json main|bin|scripts` | `manifest` |
| 构建/测试命令 | 根 `Makefile` target 名、`build.sh` 是否存在（**只列名字，绝不执行**） | `filename-heuristic` |

`*_source` **必须原样渲染**。"猜的"和"AST 验的"混在一个字段里，模型会把猜测当权威 —— 这是蓝图最自我否定的失效模式。
v1 只实现 Go 的边抽取；其他语言渲染 `edges: (no extractor for rust)`，**不留空**（空读起来像"确实没有依赖"，那是假事实）。
边必须用**相对路径**（`->agents` 里的 `agents` 可以是任何一层），截断必须写成 `+N`（I3/I4）。

---

## 5. 新鲜度：三条判据，全部机械

### 5.1 结构层 —— 成本自适应，不落盘（v3 §5.1 沿用，含到期条件）

```
启动时量一次扫描成本（今天实测 6.6ms）：
  < derive_budget(常数 150ms) → 每轮注入前重派生：无缓存、无失效判据、无一致性问题
  ≥ derive_budget            → 进程内缓存；失效判据 = git HEAD 变了（一次 rev-parse ~10ms），非 git 退化为项目根 mtime
```

默认"每轮重派生"的理由不是性能余裕，而是**它让一整类 bug 不存在**：没有缓存就没有失效判据。
**这条推翻是有到期条件的**：一旦某仓库扫描超过 150ms（5 万文件量级），缓存 + HEAD 失效 + sha 锚定增量刷新重新变成正解，
届时把那套加回来。写下到期条件是为了让实施者不把推翻当信念，也不要偷偷加回来而不说触发条件。
派生层永不在任务关键路径上做磁盘工作：Init 时起一个 goroutine（形状同 `app.go:419-447` 的 worker 装配），
注入侧渲染当前已有结果，没就绪就在 header 写 `blueprint: scanning…`。
唯一允许同步扫盘的入口是 `read_blueprint`（那是模型主动要的）。

### 5.2 散文层 —— 两条判据叠加

**(a) 结构指纹（v3 §5.2 沿用）**：每个模块在 front-matter 的 `module_fingerprints` 里记一条
`sha256(排序后的 文件列表 + 语言直方图 + 出边 + 入边)`。渲染时比一次：不符则该 `## Modules/<path>` 子节全部原子标 `stale`，
正文照旧渲染并加一行 `annotation predates a structural change`。
**指纹不符不删除**：清空会把可用信息变成不可用；旧说法在下一次 Repo 覆盖之前仍然是"曾经成立的说法"。

**(b) 出处锚点（v4 新增，I19）**：每条原子的 `where` 由程序复核，三态：

| 锚点形态 | 判据 |
|---|---|
| `path:line` | 文件存在 且 `line <= 总行数` |
| `path:symbol` | 文件存在 且 该文件内出现 `\bName\b`（朴素匹配；**不**依赖 codeseek —— 索引在建或不可用是常态，`repo.prompt.md:47-48`） |
| `path` / `path/` | 存在 |
| 非路径锚点 | 不判定，`state` 保持 `ok`，但计入 `Coverage` 的 `unverifiable`（不谎称验过） |

时机：fold 末尾只验本次涉及的原子（增量、单文件一次 stat/read，本次 fold 内缓存）；全档重验只在 CLI `yagent blueprint verify`。
**(a) 与 (b) 各自能答一件事**：(a) 答"这个模块形状变了，注解整体可疑"；(b) 答"这句话指的那个东西没了"。
只有 (a) 时，"结构没变但结论过期"（某个键被删、某段职责换了包）会长期冒充事实。

**stale 是粘滞的**（v3 的关键含义保留）：没有后台进程会补写，一片过期之后**只有下一次 Repo 真的覆盖它才修**。
所以索引行必须写成行动理由而不是错误：`N modules annotated but stale (delegate_repo to refresh)` ——
Director 读到的语义是"这里有份旧地图，要不要派人复核"，而不是"蓝图坏了"。
把"是否值得为新鲜度花一次委派"留给有任务上下文的一方判断，而不是让后台进程替它花钱。

### 5.3 孤儿（v3 §5.3 沿用，形态改成子节）

模块集合是派生的，所以目录改名/删除后旧注解**永远不会再被扫到，也永远不会被自己删**；
更坏的是目录名被复用时，模型会读到关于**另一个旧模块**的散文。三条：

1. **可见集合 == 本次派生的模块集合**：`## Modules/<path>` 里 `<path>` 不在派生集合 → 不进索引、`read_blueprint` 也读不到（I13）。
2. 老化清理按 `seen`：超过常数 90 天 → 从文档里摘除该子节。只在启动时跑一次，形状照 `blackboard/gc.go`（一层遍历、`os.Lstat` 拒符号链接、`<=0` 关闭）。
3. 摘除前整档已在 `revisions/`（§3.1），所以"删错了"有 Undo。

---

## 6. 注入面与预算

三处都是**每轮重算的 system prompt 字符串拼接**，不是 `messages`：

| 位置 | 文件:行 | 现有同族注入 |
|---|---|---|
| Director | `director.go:857-891`（`prompts` 闭包，每轮执行） | `knowledge.Inject` :881-888、custom agent 清单 :874 |
| Repo | `repo.go:127-146` | `RenderMemoryForInjection` :129-131（**本轮被蓝图替换**，§12）、`knowledge.Inject` :138-144 |
| Coding | `coding.go:361-370` | `knowledge.Inject` 同位；`cfg.RepoContext`（`executor.go:206-207`）本轮删除（§12.2） |

**不变量（照抄黑板那条，理由一样，I2）**：注入块只进**这次 LLM 调用**的 system prompt，
绝不写回 `ConversationMemory` 或 rollout。第二/三档压缩会整段重写消息（紧急压缩把 memory 重写成 `[system, one user]`），
进 messages 的东西会被摘要吞掉；system prompt 每轮由闭包重建，天然扛压缩。AGENTS.md §11 坑 11 记的正是这个事故。

### 6.1 形状牌价（今天实测，42 片 / 103 条边）

| 形状 | 内容 | 字符 | ≈token(`len/4`) | 最长行 |
|---|---|---|---|---|
| ① spine | `path Nf` | 882 | **220** | 31 |
| ② spine+edges | ①+出边 4/入边 3（相对路径，截断写 `+N`） | 3496 | **874** | 151 |
| ③ rich | ②+55 字符 purpose | — | 约 ②×1.25 | — |

**定案沿用 v3：常驻 ②，purpose 永不进常驻**（§1.2 的对账表：边是唯一无可替代的确定性事实，purpose 是唯一不可验证的散文）。
散文的入口只有 `read_blueprint`。
`len/4` 是 `tokenutil.go:35` 自己的降级口径，是**下限**；R3 落码时用 `EstimateTokens` 重量并把实测数写进配置注释。

### 6.2 牌价不是账单（v3 §6.2 的实测结论，通用价值最高，保留）

进 system prompt 的注入文本**前缀稳定 ⇒ 可命中 provider 缓存**。账目在 rollout 里就能读到：
`event_msg` → `payload.type=="token_count"` → `payload.info.total_token_usage.{input_tokens, cached_input_tokens}`
（v3 实测一份 director 会话：相邻两轮命中率分别 14% 与 73%）。
所以"874×(1+委派数)"这种算法是牌价。**缓存命中率是 §16 的必报指标 M5**：若注入臂命中率反而低于对照臂，
说明常驻文本打散了前缀，② 的牌价才成立，届时退 ①。这条对黑板索引和任何每轮注入同样适用。

### 6.3 预算阀门

```
index_max_tokens = 1000    # ② 今天实测 874，留余量；截断必须写进 header `+N modules not shown`（I3）
```
`index_max_modules=60`、`index_line_chars=170` 降为常数（v3 §9 的收缩方向）。

**v3 删掉的"子 Agent 作用域视图"**（按委派文本里的路径字符串匹配、只注入该片+邻居）**保持删除**：
省约 700 token，代价是一个脆弱匹配器 + 一条"匹配不上就静默退回"的不可测分支。省钱不多、失效无声。

---

## 7. 读写通道

### 7.1 读：`read_blueprint`（唯一的新工具）

| 项 | 内容 |
|---|---|
| 参数 | `section?`（`architecture/flows/conventions/dependencies/gotchas/modules`）、`module?`（空=返回索引）、`field?`（purpose/data_flow/add_here）、`offset/limit` 分页（照 `read_note`）、`state?`（`ok`/`stale`/`unanchored`） |
| 语义 | 渲染"派生骨架 + 落盘散文"。派生没就绪返回 `scanning…` 而不阻塞 |
| Spec | `Effect` 复用 `EffectContext`（`effect.go:22`）；`Frequency: Warm`；`!Mutates && !Serial` ⇒ 可并行，**必须进 `registry_gate_test.go:135` 的 `signedReadOnlyAdditions` 并写明理由** |
| 画像 | **不改任何画像**：`EffectContext` 已被六处 Grant（`toolspec/profiles.go`），因此 golden 会一次变六个 Agent 的工具面 —— `go test ./internal/agents -run TestAgentSurfacesGolden -update` **先看 diff 再提交** |
| 找不到模块时 | 返回"未知模块 + 最接近的 5 个候选 + 一句去查 `semantic_search`"，**不能返回空** —— 空返回会被读成"这里没东西"，然后模型开始不信蓝图 |
| 不要做 | 不进 `executor.go:261-278` 那段 `cfg.Surface == nil` 的历史兜底（`:257-260` 的注释写明它只服务"只传 Adapters 的循环单测"） |

注册照 AGENTS.md §9.3 六步走。

### 7.2 写：`agent_exit` 的新参数 `blueprint[]`

沿用 v3 定案 10 与其四条理由（`agent_exit` 必然发出 / 采集点已存在 / 有界数组是这套 schema 的既有语法 / 单一写入通道）。
schema 进 `toolspec/schemas.json` 的 `agent_exit.parameters.properties.blueprint`：

```jsonc
{ "type":"array", "maxItems": 8,
  "description": "Durable, directory-level knowledge about the modules you actually analyzed in this run. One entry per module; omit entries for modules you did not read.",
  "items": { "type":"object", "required":["section","claim"],
    "properties": {
      "section":    {"type":"string", "enum":["architecture","modules","flows","conventions","dependencies","gotchas"]},
      "module":     {"type":"string", "description": "Project-relative directory, exactly as it appears in the blueprint index. Required when section == 'modules'."},
      "field":      {"type":"string", "enum":["purpose","data_flow","add_here"], "description": "Required when section == 'modules'."},
      "claim":      {"type":"string", "description": "One sentence, English. <= 400 chars."},
      "where":      {"type":"string", "description": "Evidence anchor: path, path:line, or path:symbol. Omit rather than guess."},
      "supersedes": {"type":"array", "maxItems": 4, "items": {"type":"string"}, "description": "Short ids of existing atoms this claim replaces."} } } }
```

三条边界沿用 v3 §7.2：schema 是共享的（七个 Agent 都有 `agent_exit`），落盘只发生在 Repo 完成的接缝；
prompt 侧写清"只写你真查过的模块，宁缺毋滥"；Coding 将来若要沉淀，是把同一套校验接到它的完成接缝上，不是把它变成通用工具。
**Director 侧不需要知道这个参数存在。**

### 7.3 与 v3 三字段模型的关系

v3 的 leaf 只有 `purpose/data_flow/add_here` 三个模块级字段。v4 把文档扩成 6 段，其中
`Modules` 段的合法 `field` **就是那三个**，其余五段是仓库级散文（无 `field`）。
这不是"多一个维度"，是让"Director 是唯一入口"这类**跨模块论断**有地方落脚 ——
v3 只能把它塞进某个模块的 `purpose`，而塞错地方的论断永远不会被下一次覆盖该模块的 Repo 纠正。

### 7.4 合并与取代规则（"要么生成要么更新"的确切语义）

fold 是确定性纯函数，零 LLM：

1. **拒收**（每种各记一条账，I18）：`section` 不在枚举 / `claim` 为空 / `modules` 段缺 `field` 或 `module` / 短 ID 冲突且内容不同。
2. **去重**：`normalize(claim)`（折空白、小写、去尾句号）相同 → 只更新既有条目的 `seen/state/src`，正文不动。
3. **取代**：`supersedes` 里的短 ID 存在 → 摘除旧条目；ID 不存在 → **不报错也不假装成功**，记一条 reject，新条目照样写入。
   模型指认错 ID 是常态，吞掉一条正确的观察比留一条噪声贵。
4. **同字段覆盖**（v3 的规则保留）：`Modules` 段同一 `module`+`field` 的新值非空才覆盖，**空字段不抹掉上一次的内容**；同一次提交里同一 `module`+`field` 出现两块 → 取最后一块并 warn（不去猜哪块更好）。
5. **追加与稳定排序**：段内按 `(where, claim)` 字典序 —— 顺序必须可复现，否则每次 fold 产出一整片 diff，人类 review 与 provider 前缀缓存同时失效（§6.2 的账单依赖这条）。
6. **配额**：单段超 `max_atoms_per_section`(常数 40) 按 `(state 优先保 ok, seen 最旧先出)` 淘汰，淘汰数写进渲染出的 `Coverage` 行；全文仍在 `revisions/`。

### 7.5 五道校验（提交进来的散文必须全过）

前四道沿用 v3 §7.3，第五道 v4 新增。

1. `section` ∈ 枚举；`modules` 的 `module` ∈ **本次派生**的模块集合。未知 → 整块丢弃 + warn（**不许模型创建模块** —— 边界是扫描器的决定）。
2. **证据集校验（I17，v3 的承重安全阀）**：`module` 必须属于本次 run 真打开过的文件所归属的模块集。证据从 `ExecutorResult.History` 取，**入参侧与出参侧都要算**：
   - 入参侧：凡 tool call 参数里出现路径的（`read_file` / `query_code_snippet` / `query_code_skeleton` / `search_replace_in_file` …）；
   - 出参侧：`semantic_search` 的 `hits[].file_path`、`list_dir`/`print_dir_tree` 的输出、`search_by_regex` 的命中行。
   > v3 的理由照抄：只认入参，一轮"检索到 8 个文件、读过其中 3 个"的分析就只能提交 3 片，
   > 而那 8 片才是它真正掌握的 —— **只做入参侧的校验是个会误伤的校验**。闸门 `TestEvidenceSetIncludesSearchResults`。
   这条挡不住"读了但说错"，但挡得住"没读就说"，后者频率高得多。实现是一个纯函数，不动热循环。
3. **路径成员校验（I14）**：散文里的"路径样 token"（含 `/` 或以已知源码扩展名结尾）**如果能被归属**，必须属于该片的派生事实集（`files` ∪ 邻居模块路径）。
   v3 的理由保留：`os.Stat` 只证明"这条路径存在"，模型完全可以拿真路径支撑假断言；成员判定证明"它属于这个模块"，那才是真会被搞错的东西。
   > **v4.1 修正（2026-10-20）**：归属判据必须是"它到底指哪儿"，而不是"它长得像不像路径"。
   > 原实现把 `looksLikePath`（任何含 `/` 的 token）直接当违规判据，于是两类正常散文被当成幻觉：
   > 斜杠分隔的标识符列表（`Name/Brief/Effect/Frequency`、`Cold/Hot`），以及少写前缀的真实路径
   > （`toolspec/schemas.json`、裸 `schemas.json` —— `ModuleOf` 只沿目录前缀上溯，部分限定的写法永远落到根片）。
   > 实测代价：2026-10-19 一次真实提交的三条 `blueprint[]` 全被判成 `path-not-in-module`，
   > fold 因此 NoOp，`blueprint.md` 从未出现，唯一痕迹是两行 WARN。
   > 现在的三条判据（`prosepath.go`，挂在 `AnchorChecker` 上 —— 存在性本来就只有它管）：
   > ① 能归属到唯一一片（从根写全 / 少写前缀 / 裸文件名都算）才谈张冠李戴；② 同名散在多片 = 判不了，不谎称验过；
   > ③ 仓库里查无此物 = 那不是路径断言，存在性交给 `where` 锚点（第 5 条）。
   > 拒收语义没变：**一条被拒就丢整条 claim**，所以判据宁可弃权不可误判。闸门 `fold_prose_test.go` 四形 + `TestProsePathMustBelongToModule`。
4. **长度**：`claim ≤ 400`、`where ≤ 120` rune，超限按 `handoff` 的"丢弃整字段"处理而非截半句（`handoff.go:134-184`）。
5. **锚点复核（I19，v4 新增）**：`where` 非空则按 §5.2(b) 判定；判为 stale **不拒收** —— 写进去并立刻标 stale，比拒收更有信息量（拒收会让"最后一次正确说法"直接消失）。

---

## 8. 落盘钩子：接在 Repo 完成的那个既有接缝上

**位置**：`repo.go:197-209`。那三行今天已经在做同一件事的另外两种版本
（`worker.Submit` 写 KV 记忆、`autoConsolidateSubtask` 写 codeseek 知识库）。蓝图是**同一时点的第三个 sink**：

```go
agentResult := FinalizeResultFull(a.Name(), input, result)
... 既有 sink 按 §12 处置（KV 那条摘掉，知识库那条不动）...
if a.blueprint != nil && len(agentResult.Handoff.Blueprint) > 0 {
    if err := a.blueprint.Commit(ctx, agentResult); err != nil {
        slog.Warn("blueprint: commit failed", "error", err)   // 绝不向上抛
    }
}
```

`Commit` 的输入是 `AgentResult` 里已有的两样：`History`（§7.5 第 2 条的证据）与
`agent_exit` 返回值里的 `blueprint[]`（走 `collectHandoff` 同一条解析路，`result.go:90-99`）。
**没有任何新执行体**：不起 goroutine、不排队、不调模型、不需要引擎与档位。
v2 的 worker 规格 —— chan(16)、满则丢、30s 超时、3 次退避、失败留旧、fast 档取档、两个写入方的所有权规则 —— 连同测试整节删除。

**四条实现纪律（沿用 v3 §8）**：

1. **不许静默丢失**：块缺失 / 不可解析 / 越出证据集 / 路径成员违规 / 锚点判 stale 五种情形各打一条 `slog.Warn`
   （带 module 名与违规 token），并各记一条 rollout `event_msg`，让 §16 与 `/blueprint` 能回答"这一轮为什么没更新"。
   **一条都没更新 ≠ 更新失败**，两者必须在账目上可区分（I18）。反面教训是 thinklink：`<thought>` 解析失败静默丢失（AGENTS.md §10.2）。
2. **不能影响任务结果**：钩子跑在委派返回的那条 goroutine 上，只做解析+校验+一次文件写（ms 级）。
   IO 失败只 warn，绝不向 Repo 的返回值传播错误（§11 坑 22"降级不是错误"同族）。
3. **暂停/取消/`max_steps` 不写**：abandoned 委派的交付物不完整 —— `writeDelegateNote` 在那条路径上什么都不写是同一条理由
   （`director.go:540-546`，§11 坑 24）。`agent_exit` 没发生就没有块；但 **`max_steps` 耗尽那条路要显式跳过**，
   别拿一轮中途的 History 去证明模块归属。
4. **测试要有真接缝**：走 `internal/agents/e2e_*_test.go` 那套（真实 dispatcher/便签盘/rollout，LLM 换成按 Agent 身份分派的脚本引擎）；
   变异测试：去掉证据集校验必须红。这是 `TestAgentSurfacesGolden` 之外唯一需要新写的闸门族。

**kill-switch**：唯一启用判据是 **`config.BlueprintEnabled()`** —— 它把两件事算在一起：
环境变量 `YAGENT_BLUEPRINT=0` 立即关，以及 `[blueprint] enable = false`（设计稿 §10）。
派生、注入、工具、落盘钩子、GC 五处共问这一个函数（I8）。
`internal/blueprint` **自己不读环境变量**：蓝图与 resume 一样有配置键，判据必须有唯一一处合并点，
形状照 `config/resume.go:125-130` 的 `ResumeEnabled()`（blackboard 没有配置键，所以它的判据可以就待在包里，
`board.go:29-31`）。装配层的读取点见 `app.go` 的 `resolveBlueprintRoot`。
关闭时 `read_blueprint` 的 description 走动态降级成
"蓝图已禁用（enable=false 或 env YAGENT_BLUEPRINT=0）+ 修复方式"，复用 `agents/specs.go:91-101` 的写法，
**不要把工具静默从工具面上摘掉**。

---

## 9. 导出层（v4 新增，对应 §0.1 第 2 条推翻）

- 时机：fold 成功后 `export_path` 非空则同步重渲染；`/blueprint export [path]` 与 `yagent blueprint export` 同一函数。
- 内容：**渲染结果**（骨架 + 散文，含短 ID 与注释元数据），顶部一行
  `<!-- generated by yagent blueprint; do not edit — the writable source lives in ~/.yagent/data/blueprint/{project_id}/blueprint.md -->`。
  两份不同形态的同一文档必然漂走，所以导出物不是"精简版"而是"同一份 + 骨架"。
- 路径安全：经 `WorkspaceGuard` 校验必须落在工作区内（AGENTS.md §5.4），越界拒绝并点名真源位置。
- **R7 的相互作用与补偿**：导出物是 untracked ⇒ 脏 `git status`；`stash_dirty_worktree` 会把它收进 stash。
  ① 真源在 `~/.yagent`，导出物是幂等渲染 ⇒ 被 stash/误删都不丢信息，下一次 fold 或一条命令就复原；
  ② 开启 `export_path` 时 `slog.Warn` 一次说明这件事；③ 写导出物只走原子写，绝不 `O_TRUNC` 原地改写。
- 不做：不改用户 `.gitignore`、不自动 commit、不与 `project_context.go:53` 的三个文件名相撞
  （蓝图是渲染产物，不是指令文件；把它当指令注入会绕过所有预算与验旧判据）。

---

## 10. 配置：5 个键，其余是常数

判据仍是 AGENTS.md §11 坑 25（**改这个值，有没有一条用户可观察的行为会变**）。
v3 §9 批评"转身加 14 个键就是同一个病"，v4 自己也受这条约束 —— 比 v3 多的两个键各自对应一条 v4 新增能力（导出、散文预算）。

```toml
[blueprint]
enable = true                  # false = 五处全关（连派生都不做）；缺省 true 靠键存在性判定（坑 26）
inject_into = ["director","repo","coding"]   # 成本自救阀；认不出的名字 warn 后忽略（不许静默）
index_max_tokens = 1000        # 常驻骨架硬顶；<=0 = 默认；截断写进 header
prose_budget_tokens = 1500     # 按需渲染散文的预算（今天 KV 那套的上限正是这个数，语义从"整份记忆"变成"按需渲染"）
export_path = ""               # 空 = 不导出；相对路径按项目根解析，必须落在工作区内
```

常数（改它们要动代码 + 过测试，这正是该有的门槛）：`max_files_per_module=40`、`max_depth=4`、`files_list_cap=24`、
`max_scan_entries=20000`、`scan_wall_budget=2s`、`derive_budget=150ms`、`orphan_ttl=90d`、
`blueprint_max_blocks_per_run=8`（与 schema 的 `maxItems` 同一个数，两处必须一致，I12）、
`max_atoms_per_section=40`、`keep_revisions=5`、`claim/where` rune 上限（400/120）、边渲染 cap（出 4 入 3）、
`index_max_modules=60`、`index_line_chars=170`。

**v4 明确不采纳"9 键"草案**（`verify_anchors` / `consolidate_after` / `dedupe_*` / `keep_revisions` /
`max_atoms_per_section` 等）：验锚是 fold 的固有步骤、去重是确定性算法、快照是安全阀而非用户选项 ——
它们都没有"关掉"的用户语义，进配置就成了坑 25 点名的那类假开关。

实现照 `config/resume.go:22-130` 逐件对齐：struct+tags / `Default…` / `Presence` / `SetGlobal…` 归一化 / `Global…` / env 判据；
`Config` 加字段；`LoadFromFile` 里 `sectionHasDecodedKey(md,"blueprint")` + 逐键 `md.IsDefined` + `warnRemovedKeys`。
**两个模板都要改**（embedded `internal/config/default_config.toml` + 参考 `config/config.toml`），
`template_test.go:14` 会因一个 undecoded key 直接红。**模板注释必须英文**（shipped artifact 的口径）。
向导（`internal/tui/setup*.go` 首选项屏）加一格 `enable`：它与 `[routing] mode` / `[browser] enabled` 同属仓库级长期能力开关家族，
不加就是"文档里有、向导里没有"的又一处不符。

---

## 11. projectID：抽包，并且把第二份实现真的删掉

现状两份（v3 §10 的表，v4 复核仍然成立）：

| 实现 | 算法 | 管谁 | 闸门 |
|---|---|---|---|
| `app.go:913-939 getProjectID` | `base≤242 + sha[:12]` | **只有** `shared_memory`（`app.go:953`） | `assembly_test.go:167`（且 `:165` 那句注释声称它还管 rollout —— **错的**） |
| `director/project_context.go:77-100 ComputeProjectID` | `base≤100 + sha[:8]` | rollout（`director.go:484`）+ artifact（`result.go:144`、`read_artifact_tool.go:87`） | `project_context_test.go:103`（`:136` 钉具体串、`:151` 钉长度） |

蓝图必须与 **rollout/artifact 同 ID**。v3 的做法是抽 `internal/projectid`、`app` 那份"并存不动"。
**v4 多加一步**：`app.getProjectID` 与 `getSharedMemoryPath`（`app.go:941-955`）随 §12 的 KV 退休一起**删除**
—— 它唯一的调用者消失了，留着就是一具骨架，也正好消灭"注释比实现更自信 → 长出第三份"的机制。

- 新增叶子包 `internal/projectid`，实现取 `ComputeProjectID` 的**现有算法**（`base≤100 + sha[:8]`）。
- `director/project_context.go:77-100` 改一行转发；`internal/app` 与 `internal/blueprint` 都调它。
- 零迁移的判据不是新写测试，而是 **`TestComputeProjectID` 抽包后一字不改仍绿**（它钉了具体串与长度，任何漂移当场红）。
- `internal/blueprint` **不 import `internal/agents/director`**（叶子守卫）：projectID 由调用方作为参数传进来。
- 已知限制（写进 front-matter，不假装解决）：ID 由**绝对路径**派生，`~/a/yagent` 与 `~/b/yagent` 是两份，换机器即丢。
  `git.origin` 已存进 front-matter，是给未来"按 origin 迁移"留的钩子；v1 不做迁移。

---

## 12. 收编并删除仓库 KV 记忆（v3 定案 4 的还债动作）

### 12.1 迁移（一次性，不静默）

首次加载时若 `blueprint.md` 不存在、而 `shared_memory.json` 里有 `repo_memory:{projectPath}`：
把六段 KV Markdown 按段拆成散文原子写入 `blueprint.md`，**每条 `state=unanchored`**（旧格式没有出处，不可能有锚点），
渲染的 `Coverage` 行写明"N 条来自旧仓库记忆，均未验锚"，然后**删除该 KV key**。
判据是两段真相不能同时成立；`unanchored` 计数让"这份蓝图看起来完整但没法验"这件事留在账面上而不是被藏起来。

### 12.2 删除清单

| 现有点 | 处置 |
|---|---|
| `repo.go:129-131`（`<repository_knowledge>` 注入） | 换成蓝图的按需渲染（受 `prose_budget_tokens` 约束） |
| `repo_memory.go` 全文件（`RepoMemoryStore`/`MaxMemoryTokens`/`EnforceTokenBudget`/`RenderMemoryForInjection`/`ValidateMemoryFormat`） | 删除 |
| `consolidation_worker.go` 全文件（整份重写式 LLM 合并、`consolidationSystemPrompt`、`writeConsolidationFile`、`triggerPruneMerge` 连同其上的临时禁用块、`pruneTriggerInterval`） | 删除 —— 蓝图没有"整份重写"这个环节 |
| `coding.go:385` + `executor.go:43,206-207`（`cfg.RepoContext`） + `ports.go:39-42` + `director.go:55,120,168,815-826` + `globalctx/repo_context_store.go` | 删除。Coding 的仓库上下文改走同一份蓝图渲染；`delegate_repo` 的返回值不再需要任何特殊处理 |
| `memory.SharedMemory`（`shared.go` 全文件）+ `app.go:58,421-431,941-955` + `app.getProjectID` + `assembly_test.go` 的两条相关断言 | 删除。生产里唯一的用户就是那份 KV（`app.go:433`）；消息/pubsub 半边（`AddMessage`/`Publish`/`Subscribe`/`FilterByAgent`/`GetContext`）今天零调用点 |
| `repo_memory_test.go` / `consolidation_worker_test.go` / `TestGetSharedMemoryPathIsPerProject` | 删除，职责由 §15/§16 的蓝图闸门顶替 |
| `AGENTS.md` §6.4 的 RepoMemory 段、§6.7 持久化表的"共享/仓库记忆"行、§3 模块地图的 `shared.go` | 同步改写（§18.1） |

**每步 token 成本要说清**：注入在 system prompt 里，Coding 一次委派最多 150 步就是 150 次重复计费。
今天的 `repoCtx` 是 `delegate_repo` 全文（可达数千 token）只会更贵，所以这是净改善；
且 `Coverage` 永不进注入（最没信息量又最易过期的一段，却总排在文档末尾被整段带进来）。

---

## 13. TUI 与 CLI：v1 收缩（沿用 v3 §11）

**不做全屏叠加层**。这个模块的价值还没被证明，而叠加层要付的是 §11 坑 30/31 那一整套纪律（按渲染后行数算预算、
`fitLines`/`wrapPlain`、模态键必须带修饰符、宽度矩阵 {30..240}）。v1 只出：

- `/blueprint` —— 当前索引（形状②）+ stale/未覆盖清单，作为一条普通日志项打进主区，复用现成渲染路径。不是叠加层、不抢键盘、不需要新的渲染测试。
- `/blueprint refresh` —— 强制重派生并列出过期片与从没被 Repo 覆盖过的模块。**不触发任何自动补写**（没有 worker 可触发）：
  它的作用是把"该不该派人复核"交给人或 Director。
- `/blueprint export [path]` —— 见 §9。
- 三条命令都必须**显式进 `processCommand` 的斜杠分支**（`tui_update_commands.go:27`），否则 `/blueprint` 变成"在搜字符串 blueprint"（§11 坑 17 后半段）。
- CLI：`yagent blueprint show|refresh|verify|export`（不装配 Agent/MCP，形状照 `yagent setup` 的"配置坏了也能跑"）。

叠加层留到"有人真的用它"之后再加，届时照 `common/list_metrics.go` 的原语走，**不要再写一份**（坑 18）。

---

## 14. 并发模型

结构层进内存后同时有四类访问者（v3 §12 整套沿用；本仓对并发是有纪律的：`mcp` 的 `sendJSON` 写锁、
`Client.Engine` 只能经 `DefaultEngine()` 读、`Yagent.engine` 只能经 `defaultEngine()/setEngine()` 读写）：

| 访问者 | 时机 | 规则 |
|---|---|---|
| 派生 goroutine | Init 起跑；缓存模式下 HEAD 变化时重派生 | **唯一写者**。整份快照构造完后 `atomic.Pointer[Snapshot]` **一次替换指针**，读者永远看到完整快照 |
| 三处注入 | 每轮 prompt 构造 | 只读 `Load()`；不加锁、不阻塞、不等派生（没就绪就渲染 `scanning…`，I15） |
| `read_blueprint` | 工具批次内，可与其他只读工具并行 | 只读快照 + 读文档 |
| 落盘钩子 | Repo 完成时，委派返回的那条 goroutine 上 | 只写文档；**不碰快照** |

- 快照必须是**不可变值**，否则 `atomic.Pointer` 只是把数据竞争换成可见性问题。
- 散文只有一个写入方（落盘钩子），所以 v2 那段"worker 与工具同时改同一片"的 race 讨论不存在。
- `--prompt` 非交互路径必须能干净退出：派生 goroutine 挂在 `Yagent.Close()`（`app.go:885-895` 的先例）。
  **顺带一条 v3 没记录的既有泄漏**：`SharedMemory.EnablePersistence` 起了一个 5s ticker（`shared.go:227-249`），
  而 `Close()` 只停了 consolidation worker（`app.go:889`），**从没调 `sharedMemory.Close()`** —— 这条不用修，因为整块要删（§12）。
- **多进程（v4 新增，I20）**：两个 TUI tab 同开一个项目会各自 fold。策略是乐观 CAS：
  读档 → fold → **写前重读一次**，字节变了就对最新内容重放同一 fold（fold 幂等 + §7.4 第 5 条的稳定排序 ⇒ 重放安全），
  两次仍冲突就放弃本次并 `slog.Warn`，由下一次 fold 自然补上。**不引入锁文件** ——
  那是"进程崩了留给后人一个解不开的 `.lock`"那类设计。
- 改并发相关代码后手动跑 `RACE=true bash scripts/check_all.sh`（默认流程不含 `-race`）。

---

## 15. 不变量清单（v3 的 I1–I18 与 v4 新增合并；每条都要有"它必须失败"的测试）

| # | 不变量 | 闸门 |
|---|---|---|
| I1 | 渲染出的每条路径都来自本次派生（结构不落盘 ⇒ 不可能有死路径） | `TestRenderedPathsComeFromScan` |
| I2 | 注入只进 system prompt，不进 `ConversationMemory`/rollout | `TestBlueprintIndexNotInMemory` + `TestBlueprintIndexSurvivesEmergencyCompressionShape`（仿 `board_index_test.go:70,106`） |
| I3 | 注入受 `index_max_tokens` 硬顶，且截断写进 header | `TestIndexRespectsTokenBudget`（夹具必须自带"超预算"的 `t.Fatalf` 前置条件） |
| I4 | `stale`/`unannotated`/`TRUNC`/`oversize` 四态在索引行必然可见 | `TestAllFourFlagsAlwaysRendered` |
| I5 | 模型能写的只有散文行；front-matter 的 `git_*`/`folds`/`sources`/指纹只能由程序写 | `TestAgentExitBlueprintItemSchemaShape`（断 schemas.json：item 只有 6 个键）+ `TestCommitProgramFieldsAreOverwritten` |
| I6 | 扫描不越出项目根：symlink 拒入、submodule 在**目录分支**跳过、`..` 不解析 | `TestScanRefusesSymlinkedDir`、`TestScanSkipsSubmoduleAtDirLevel`（§4.1 那条实测 bug） |
| I7 | 存储根不在项目路径内 | `TestStoreRootMustNotBeInsideProject` |
| I8 | 启用判据只有一处：`config.BlueprintEnabled()`（env `YAGENT_BLUEPRINT=0` ∪ `[blueprint] enable`），派生/注入/工具/钩子/GC 五处共问；`internal/blueprint` 自己不读 env | `TestBlueprintKillSwitchOverridesConfig` + `TestResolveBlueprintRootKillSwitch`（R1 已落）；R2+ 每加一处消费点就加一条"env=0 时它必须不动" |
| I9 | 非 git 仓库照常工作（每轮派生；缓存模式退化 mtime），不报错 | `TestNonGitRepoStillDerives` |
| I10 | 结构指纹不符 → 标 stale 且**保留**旧散文；且没有任何后台进程会补写（粘滞语义） | `TestFingerprintMismatchKeepsProseAndFlagsStale` |
| I11 | 钩子从不向 Repo 返回值传播错误；`max_steps`/取消/暂停路径不写 | `TestCommitFailureNeverBreaksDelegate`、`TestAbandonedRepoRunWritesNothing` |
| I12 | projectID 与 rollout 同源、零迁移；schema 的 `section` enum 与代码枚举双向对账；`maxItems` 与常数一致 | 既有 `TestComputeProjectID` 抽包后一字不改仍绿 + `TestSectionEnumMatchesSchema` |
| I13 | 孤儿模块子节永不进注入、永不可被 `read_blueprint` 读到；可见集合 == 派生集合 | `TestOrphanSectionNeverRendered`、`TestVisibleSetEqualsDerivedSet` |
| I14 | 散文里**能归属**的路径样 token 必须属于该片（含写短的前缀与裸文件名），否则整块丢弃；同名多处与查无此物都弃权不拒收 | `TestProsePathMustBelongToModule`（无 fs 的老判据）+ `fold_prose_test.go` 四形 |
| I15 | 快照不可变 + 原子替换；注入永不阻塞等派生 | `TestInjectionNeverBlocksOnDerive` |
| I16 | 根护栏：cwd 是 `$HOME`/`/`/无 manifest 且非 git → 拒绝建蓝图并说明原因 | `TestRefusesHomeDirAsProjectRoot` |
| I17 | 提交只能覆盖本次 run 的证据集模块（入参侧 ∪ 出参侧） | `TestCommitRejectsModuleNeverReadInThisRun`、`TestEvidenceSetIncludesSearchResults`、`TestEvidenceSetReadsAllPathParams` |
| I18 | "没更新"与"更新失败"在账目上可区分（五种情形各一条 warn + 一条 rollout `event_msg`） | `TestEveryRejectionReasonIsAccounted` |
| **I19** | **锚点三态**：`path:line` 越界 / 文件缺失 → `stale`；`where` 为空 → `unanchored`；非路径锚点不判定但计入 `unverifiable`。三态都必须能在索引行看到 | `TestAnchorVerificationIsThreeValued`（夹具目录里造三种锚点） |
| **I20** | **fold 幂等 + 顺序可复现 + 多进程 CAS**：同批原子连 fold 两次字节不变；乱序输入 fold 结果字节相同；CAS 重放不丢条目也不复制条目 | `TestFoldIsIdempotent`、`TestFoldOrderIndependent`、`TestFoldCASReplay` |
| **I21** | **原子写只有一份实现**：`util.AtomicWrite` 是唯一内核，blackboard / artifact / datamanager / config 写回 / config 模板 / memory-shared **六处**转调 | 行为面 `TestAtomicWritesAgree`（R1 已落，覆盖 blackboard/artifact/config 三条导出路径 + datamanager/memory 各自包内测）；数量面 `scripts/check_no_inline_atomic_write.sh`（Go 测试证不了"没长第二份"，靠 grep 形状 + 一条变异验证） |

棘轮：新文件 ≤300 行（`check_file_sizes.sh`）；新增 `scripts/check_blueprint_leaf.sh` 守 `internal/blueprint`
不 import `agents`/`tools`/`blackboard`/`config`（只准用标准库 + `gitinfo` + `tokenutil` + `projectid`，projectID 仍由参数传入）。

---

## 16. 价值验收程序（v3 §13.1 全套沿用，指标口径不动）

整个模块的价值主张是行为变化，不是测试通过。这套程序的存在理由是：没有它，R6 之后没人能回答"这 874 token 买到了什么"。

对照不需要代码分叉 —— `YAGENT_BLUEPRINT=0` 是五处共用的唯一判据（I8）。

| 臂 | 设置 | 单独证明什么 |
|---|---|---|
| A（off） | `YAGENT_BLUEPRINT=0` | 基线 |
| B（只有骨架） | 默认开，但删掉 `blueprint.md` | v3 §1.1 对账表的兑现：103 条 AST 边 + 模块划分本身值多少钱（零 LLM、永远全量） |
| C（骨架 + 沉淀） | 默认开，且先跑过若干 `delegate_repo` 让散文落盘 | Repo 写的那些论断 adds 多少 —— 只有 B→C 的差值能回答 |

三臂而不是两臂：v4 之后两层分开产生价值（派生层每次都在，散文层取决于有没有 Repo 跑过），只测 A vs C 无法归因。B 臂的成本只是删一个文件。

- **采样量**：8 条定位类 prompt × 3 次 × 3 臂 = 72 次会话。必须重复跑：v3 实测一份真实 director 会话里
  `delegate_repo` 只有 **2 次/任务**（同会话 70 次工具调用），在这个基线上"下降 30%"完全落在 LLM 抖动里 —— 所以 `delegate_repo` 不设门槛。
- **采样纪律**：同一 HEAD、同一 provider、`[routing] mode = "fixed"` 钉死档位（auto-routing 会把模型差异混进来，AGENTS.md §6.3）。
- **指标与取数路径**（字段名 v3 是从真实 rollout 读出来的，v4 复核口径仍然成立；顶层形状 `{timestamp,type,payload}`）：

| # | 指标 | 取数 | 判据 |
|---|---|---|---|
| **M1 主指标** | 首次委派之前的"自我定位自读"次数 | `type=="response_item"` 且 `payload.type=="function_call"` 且 `payload.name ∈ {read_file,list_dir,print_dir_tree,search_by_regex}`，截止于首条 `delegate_*` 之前 | B、C 各自相对 A **下降 ≥40%** |
| M2 | `delegate_repo` 次数 | 同上按名字计数（`payload.arguments` 含 `{"task":…}`，用于人工核对这条委派是不是纯定位） | 只报数 |
| M3 | 到首个委派的 step 数 | 首条 `delegate_*` 的序号 | ≤ A |
| **M4 诚实性** | 死路径率 | 最后一条 assistant `payload.type=="message"`，正则抽路径样 token，相对项目根逐条 `os.Stat` | **≤ A**（这才是"蓝图有没有让模型更靠谱"的自动判据） |
| M5 | 成本与缓存命中 | 末条 `event_msg` 且 `payload.type=="token_count"` → `info.total_token_usage.{input_tokens,cached_input_tokens,output_tokens}` | 如实报，不进 pass 判据；**命中率显著低于 A ⇒ 重估形状②**（§6.2） |
| M6（仅 C） | 语义覆盖率 + 五种"没更新"的 reason 分布 | 覆盖模块数 / 派生模块数；从 rollout `event_msg` 数 | 只报数。**覆盖率是结构性风险指标**：低就说明写入通道在实际用法里根本没被喂起来 |

- **通过** = （B 或 C）的 M1 达标 且 M4 不退 且 M3 不涨；M5/M6 无论好坏都写进结果表。
- **终止条件**：只有 B 达标而 C 不达标 ⇒ 散文没带来增量 → **砍掉散文层**（`blueprint[]`、§7 整节、§12 的迁移一起删），
  蓝图退化成"每轮派生的目录级依赖图"。这是真实的可执行结局，不是修辞。
- **夹具进仓库**：`internal/blueprint/testdata/orientation-tasks.md`，8 条只问"在哪儿/谁负责/该改哪"的任务，答案可验证
  （本仓示例："`/resume` 的停点账本写在哪几个文件？"、"加一个新工具从实现到注册要动哪几处？"、
  "暂停闸门与取消的区别落在哪个包？"、"TUI 的列表度量原语在哪个包，谁复用它？"）。夹具不进仓库 = 半年后跑不出同一组数。
- **跑法**：R6 之后跑三臂（R4 结束可先跑 A vs B —— 那是纯骨架+边的效果，值得提前一次）。本仓无 CI 质量闸门，这是人工步骤。
- **结果必须填表**，并同时记录：sha、provider 与 `[routing] mode`、夹具路径、C 臂沉淀前跑过几轮 `delegate_repo`、
  抽样人工读过哪几条会话的最终答案。"验收通过"四个字不算验收。

---

## 17. 落地顺序

| 步 | 内容 | 验收 |
|---|---|---|
| **R1** | `internal/projectid` 抽包（零迁移）+ `internal/util/atomic.go` 收 5 份原子写 + `internal/blueprint` 骨架（`Enabled()`、store 根解析、front-matter、根护栏）+ `[blueprint]` 五键（两模板 + 存在性判定） | `TestComputeProjectID` 一字不改仍绿；I7/I16/I21；`go build ./... && go vet ./...` 干净 |
| **R2** | 扫描器：两类白名单、目录级 submodule 跳过、symlink 拒入、规模递归 + 链折叠、`go/parser` 边与反向边、manifest/入口 + `*_source` | 合成 fixture 树 `testdata/tree/` + **本仓自扫 golden**；I1/I6。golden 里必须能看出片数与边数（v3 那个假模块 bug 就是靠这个抓的） |
| **R3** | 派生快照（`atomic.Pointer`）+ 成本自适应缓存 + 形状②渲染 + `index_max_tokens` + 结构指纹 + 孤儿老化 | I3/I4/I9/I10/I13/I15；用 `EstimateTokens` 重测三种形状，实测数写进配置注释 |
| **R4** | 三处注入（只进 system prompt）+ **委派前缀带上目标模块的现有散文**（v3 §14.1：这是"更新"而不是"重新发明"的唯一路径，形状照 `director.go:636-677`）+ 改掉 `repo.go:195-196` 那句与代码不符的注释 | I2 |
| **R5** | 读通道 + 写通道 schema：`read_blueprint` 六步注册（含 `signedReadOnlyAdditions`）；`agent_exit` 加 `blueprint[]`；`repo.prompt.md` 加蓝图契约段（"只写真查过的模块 / 没有 `where` 就别写 / 看到既有注解时同意就别重写、不同意就写完整新说法并 `supersedes`"）；Director/Coding prompt 各一句"先读蓝图索引，它给目录不给符号" | golden 显式 diff；I5/I12/I14/I17 的 schema 与校验部分 |
| **R6** | 落盘钩子：证据集函数 + 五道校验 + fold（幂等/稳定排序/CAS/配额）+ 锚点验旧 + `revisions/` + 五种拒绝记账；接进 `repo.go:197-209` | I11/I17/I18/I19/I20；e2e：脚本化 Repo 一轮 → 文档出现；**变异测试：删掉证据集校验必须红**；`--prompt` 路径无泄漏 goroutine |
| **R7** | 收编 KV 记忆：§12.1 迁移器 + §12.2 删除清单（`repo_memory`/`consolidation_worker`/`SharedMemory`/`RepoContextStore`/`app.getProjectID`）+ `AGENTS.md` 改写 | `grep -rn "RepoMemoryStore\|SharedMemory\|RepoContextStore" --include=*.go .` 零命中；`go test ./...` 全绿；**单独一个 commit**（§18.2 第 6 条） |
| **R8** | 导出（§9）+ `/blueprint` 三条命令与 CLI 子命令 + `docs/config-guide.md` 的 `[blueprint]` 段 + 向导那一格 | TUI 宽度矩阵测试；导出物被 stash 后再跑一次 fold 能逐字复原 |

R1→R3 是"零 token、纯确定性"的最小闭环，单独就有价值（几十片 + 一百条边 + 入口）；R5 之后模型才真的用上它。
**R7 必须排在 R6 之后**：先有替代物再拆旧机器，顺序倒了就会出现一段时间里 Repo 谁都不读。

---

## 18. 文档改动与风险

### 18.1 实施完必须同步的文档

- **AGENTS.md §3 模块地图**：加 `internal/blueprint`、`internal/projectid`、`internal/tools/blueprint.go`、`config/blueprint.go`；`internal/util` 那行补 `atomic.go`。
- **AGENTS.md §5.2 工具表**：加 `read_blueprint`；`agent_exit` 那行补 `blueprint[]`（它是**共享参数**，七个 Agent 的 schema 里都会出现，只有 Repo 的完成接缝会读它 —— 不写清楚，下一个人会以为是通用写入通道）；§5.3 之后补一句分工：**目录→目录归蓝图，符号→符号归 codeseek**。
- **AGENTS.md §6.4**：**删掉 RepoMemory 段**（§12），改写为"蓝图（派生骨架 + Repo 提交的散文，唯一有机械新鲜度判据的仓库级知识）"；
  并把剩余两套知识的权威范围写清（① `AGENTS.md` 人手写的意图，机器不改；② 蓝图；③ codeseek 侧知识库 —— `consolidate_knowledge` 写、注入器今天恒返回空 `injector.go:73-76`）。
- **AGENTS.md §6.7 持久化表**：删"共享/仓库记忆 `shared_memory.json`"一行，加"蓝图 `~/.yagent/data/blueprint/{projectID}/blueprint.md`（+ `revisions/`）"。
- **AGENTS.md §7 配置**：加 `[blueprint]` 五键与存在性判据；§10.2 旁边加一句"蓝图块的格式契约与 `<thought>` 同族，但**失效行为相反** —— thinklink 解析失败静默丢失，蓝图五种'没更新'各自记账（§8 第 1 条）"。
- **AGENTS.md §11 坑**：新增五条 —— "结构层不落盘：能派生的东西一旦落盘就需要一套新鲜度机器"；
  "`projectID` 曾有 `sha[:12]`/`sha[:8]` 两份，新子系统一律 `internal/projectid`"；
  "submodule 跳过必须写在目录分支里（实测写错位置多出 14 片假模块）"；
  "沉淀挂在收尾必然发生的那个调用上，不要挂在'模型记得调'的工具上（坑 16 同族）"；
  "注入文本前缀稳定 → 可命中 provider 缓存，所以'每轮 N token × 步数'是牌价不是账单（§6.2）"。
- **`docs/config-guide.md`**：`[blueprint]` 段。

### 18.2 风险登记

1. **v4 的结构性风险：散文覆盖率由"有没有委派过 Repo"决定，不由系统决定。** 冷启动散文是 0%（只有骨架与边）。
   这是把生成主体交给一次具体分析的必然代价。两条对冲：常驻形状②**不含散文**（覆盖率低不影响地图正确性），
   以及 M6 把覆盖率变成必报数字而不是"没人发现蓝图一直是空的"。
2. **非 Go 仓库的边际价值接近零**：v1 只有 Go 的边抽取 ⇒ 非 Go 仓库拿到的蓝图 ≈ `print_dir_tree` + 未验证散文。
   本仓是 Go（+ Rust submodule），当前用途够；**别在实现第二种语言的边抽取前宣称"通用仓库蓝图"**。
3. **形状② 的牌价是 874 token/份 × (1+委派数)**（今天实测），账单大概率低得多（§6.2 的缓存账），但要靠 M5 实测而不是假设。退顾形状①的判据写在 §16。
4. **散文幻觉**：防线是五道校验 + 锚点验旧 + stale 标记。它挡得住"没读就说"（I17）与"路径不属于这个模块"（I14），
   **挡不住"读了但说错"** —— 剩下那一半没有真值可对账，这条不假装解决。
5. **单文档成为并发争用点**：v3 的一模块一文件天然把写冲突摊薄；v4 收成一文件后多进程写同一份的暴露面变大
   → **I20 的 CAS 是 §0.1 第一条推翻的补偿控制，不是可选项**。若实测 CAS 失败率高，
   正确回应是散文层退回 per-module 文件、只在导出时拼一份 —— 那是可执行的回退路径，不是失败。
6. **删 KV 的连带面比看上去大**：`SharedMemory` 一删就同时动了 `app.go` 的装配段与 `assembly_test.go` 的断言。
   这是好事（少一具骨架），但意味着 **R7 必须单独一个 commit、单独跑 `bash scripts/check_all.sh`** —— 出问题时要能只回滚这一步。
7. **不可证伪性只解决了一半**：M1/M4 能判"少走了弯路""没开始编路径"，判不了"最终答案质量"。
   所以这套验收会**低估**蓝图的价值而不会高估 —— 用它的结果做"砍掉散文层"的决定时要记得这一点。
8. **扁平大目录切不动**（`internal/tui`/`internal/agents` 两片）：靠 `oversize` + `+N more` 说出来，不靠更聪明的算法。
9. **v1 不做**：跨机器迁移、非 Go 语言的依赖边、全屏叠加层、蓝图驱动的自定义 Agent 画像、多 root/workspace、
   Coding agent 提交散文（schema 允许，落盘接缝只接 Repo）。

---

## 19. 附录：本次复测程序（可复现，不入库）

一次性 Go 程序（`/tmp/bpmeasure`，stdlib + `go/parser`）：`filepath.WalkDir` + 目录黑名单 + 符号链接拒入 +
`.gitmodules` 目录级跳过 → 单遍收集 238 个非测试 `.go` 文件的所属目录 → 每文件 `parser.ParseFile(…, ImportsOnly)` →
用 `go.mod` 的 module path 前缀把 import 换成目录 → 聚合 `dir→dir` 边集（121 条）并折叠到模块级（103 条）→
渲染形状①/②并数字符与 `len/4`。分阶段计时：walk 1.6ms、parse 5.0ms。

三条纪律沿用 v3：**设计稿里的数字必须是量出来的**（示例本身就是幻觉的第一现场）；
**"猜的"和"AST 验的"必须分开渲染**；**截断必须被说出来**。
R2 落地时这套测量要变成仓库里的 golden 测试（`internal/blueprint/testdata/`）；
本附录的作用只是让 v4 的每个数字都能被读者自己重跑一遍。

---

## 20. R1 实施记录（2026-10-17，落地时对本文的修正）

R1 已落地：`internal/projectid`、`internal/util/atomic.go`、`internal/blueprint`（文档模型 + 段落枚举 +
原子行编解码 + 存储根 + 两条根护栏）、`[blueprint]` 五键（两模板 + 存在性归一）+ 两把新守卫。
`bash scripts/check_all.sh` 全绿（26 包 / 7 守卫）。

实施中发现本文需要改的四处，都已就地修正：

| # | 本文原来的说法 | 落地时的事实 |
|---|---|---|
| 1 | 原子写有 **5** 份重复实现 | 是 **6** 份：漏了 `config/default_config.go:32`（首次生成模板的落盘）。§3.1 与 I21 已改写 |
| 2 | kill-switch 判据叫 `blueprint.Enabled()`（照 blackboard 的形状） | 蓝图有配置键，判据必须同时吞下 env 与 `enable` → 唯一判据是 **`config.BlueprintEnabled()`**（形状照 `resume.go:125-130`），`internal/blueprint` 一律不读 env。§8 与 I8 已改写 |
| 3 | 内核沿用 `path + ".tmp"` 的确定性临时名 | 改成**带随机段**的名字：两个进程写同一路径时（两个 TUI tab 的 history 索引、向导与主进程写 config.toml）固定名会让后来者截断前者的中间态。各站点原有的 tmp 后缀作为选项保留（`.md.tmp` / `.tmp` / `.persist.tmp`），保证不被目录列举当成数据文件 |
| 4 | `internal/blueprint` 可用 `gitinfo` / `tokenutil` | R1 实际只用到标准库 + `util` + `projectid`；守卫按**这个更严的集合**写（`scripts/check_blueprint_leaf.sh`），将来 R2 真要用 `gitinfo` 时再显式放宽 |

### 20.1 顺手挖出并修掉的一个既有缺陷

`memory/shared.go` 的 `persistLoop` 每轮 select 都**重新读** `sm.persistTick` / `sm.persistDone` 字段，
而 `Close()` 在锁内就把它们置 nil —— 结果是 nil panic，或 goroutine 卡在 nil channel 上永久阻塞。
这段代码在生产里从来没被执行过（`Yagent.Close()` 只停了 consolidation worker，**从不调
`sharedMemory.Close()`**，这条已写进 §14），所以它一直没人碰。
R1 把句柄改成**起跑时捕获为参数**（`persistLoop(done, tick)`），并补了
`TestSharedMemoryCloseIsIdempotent`（带超时断言，卡住就是红）。整块将在 R7 随 KV 一起删除。

### 20.2 闸门有效性（每条都真做过变异，不是"应该能失败"）

| 变异 | 命中的测试 |
|---|---|
| `Root()` 在缺 home 时退回当前目录 | `TestRootFailsInsteadOfFallingBackToCwd` 红 |
| `CheckProjectRoot` 不再拒绝 `$HOME` | `TestCheckProjectRootRefusesDangerousRoots` 红 |
| `Render` 不重算派生计数 | `TestRenderRecomputesDerivedCounters` 红 |
| 段内缺 `enable` 时当成关 | **第一次是绿的** —— 原测试只覆盖"整段缺省"那条归一化路径；补了 `TestBlueprintEnableDefaultsTrueWhenKeyAbsentInSection` 之后才红 |
| 内核无条件 `MkdirAll` | `TestAtomicWriteSkipsMkdirWhenDirPermZero` 红 |
| projectID 哈希 8→12 位 | `TestComputeGoldenTable` 红（`TestComputeProjectID` 同时红，I12 的"零迁移"判据因此是真判据） |
| 就地再写一份 tmp+rename | `scripts/check_no_inline_atomic_write.sh` 红（用临时文件验证后立即删除） |

那条绿色的变异是本轮最值得记住的：**"每条路径都要有能响的测试"不是修辞** ——
`!Section` 与 `!Enable` 是两条独立归一化路径，只测前者等于后者没有闸门。R2+ 写归一化代码时按这个来。

### 20.3 键的消费者对账（坑 25 的欠账清单）

R1 只让 `enable` 有真实读取点（`app.resolveBlueprintRoot`，三种结果可区分 + 测试）；
`index_max_tokens` / `inject_into` / `prose_budget_tokens` / `export_path` 目前只有解析、归一化与
逐键测试，消费者分别在 R3 / R4 / R4-R6 / R8 落地。
**如果某个键到了它该被消费的阶段还没被读，它就是本文的 bug，删掉而不是补注释。**

---

## 21. R3–R6 实施记录（2026-10-19，落地时对本文的修正）

分支 `feat-blueprint-persist`。**起点是一条账**：R2 的扫描器（`6415486`，77 文件 / +1965 行）
从来没被并进任何主战分支，它只活在两条 `agent/coding-*` 委派分支上 —— 便签里"R2 已落地并全绿"
是真的，只是不在工作区里。本轮第一步是把它接回来（它的父节点正好是 `cbb31d7`，所以是直线），
然后 R3→R6 一次推平：派生快照 + 形状②注入 + `read_blueprint` + `agent_exit.blueprint[]` + fold 钩子。
**验收的那条断言是文件出现**：`internal/agents/e2e_blueprint_test.go:TestE2ERepoRunWritesBlueprintFile`
（真实 Repo 循环 + 真实扫描器 + 真实 WorkspaceGuard，只把 LLM 换成脚本）。

### 21.1 落地时发现本文需要改的，以及实际做法

| # | 本文原来的说法 | 落地时的事实 |
|---|---|---|
| 1 | §8 的钩子读 `agentResult.Handoff.Blueprint` | 落在 **`AgentResult.Blueprint`**。`Handoff` 是便签的 front-matter，把同一些句子既写进便签头又写进蓝图正文，就是 §2 R4 点名的"三份真相并行"再加一份。采集点仍然只有一处（`result.go`，与 `collectHandoff` 并排），executor 不认识蓝图 |
| 2 | §15 的棘轮允许 blueprint import `gitinfo` / `tokenutil` | **没放宽守卫**。HEAD 读数与 token 计数器都做成了参数（`DeriveSpec.RepoToken` / `IndexSpec.CountTokens` / `ProseSpec.CountTokens`），生产由 `internal/app` 注入 `gitinfo` 与 `tokenutil`。守卫仍是 R1 那个更严的集合，而两条分支因此在测试里可确定性驱动 |
| 3 | §7.4 的幂等 = "同批原子连 fold 两次字节不变" | 需要一个本文没写的判据：**正文逐字没变就不算一次 fold**（`FoldResult.NoOp`）。否则重放会推进 `Folds` 与 `sources`，于是 I20（字节不变）与 I18（"没更新"≠"更新失败"）直接打架 —— 第一次实现就红在这里 |
| 4 | §7.2 `maxItems: 8` 与 §7.4 单段配额 40 | 两者会相互作用：一次 fold 最多落 8 条，所以"段里长过 40 条"在真实世界里**只能靠多次 fold 攒出来**。配额测试因此分批驱动（`commitBatches`），单批塞 45 条的夹具测的是一条生产走不到的路 |
| 5 | §7.5 第 2 条"出参侧"列举了几个工具名 | 落成了 **`toolspec.Spec.ResultPaths`**（`""`=JSON / `bare` / `rg`）：`list_dir`/`print_dir_tree` 的结果是**裸文件名**，当路径用会映射到错误的片。按工具名 switch 会被 `check_no_manual_lookup.sh` 当场拦下 —— 守卫是对的，"结果里路径长什么样"确实是工具自己的事实 |
| 6 | §8 纪律 1"各记一条 slog.Warn **并各记一条 rollout `event_msg`**" | **只落了 slog + `FoldResult` 账目**。钩子跑在委派返回的那条 goroutine 上，那里没有 rollout writer（writer 的生命周期在 `RunAgentLoop` 内部）。把 writer 交出来是另一次改动，不该塞进这一条分支的尾巴上。**这是本文与代码当前唯一已知的口径差**，I18 的"可区分"目前由日志与 NoOp 账目回答 |
| 7 | §12 收编 KV 记忆（R7） | **没做**。Repo 完成接缝上现在并存两个 sink（`worker.Submit` 写 KV + 蓝图 fold）。所以 §6.4 里"注入被蓝图替换"这句只对**注入**成立，KV 那套的删除清单仍然待执行 |

### 21.2 顺手挖出并修掉的既有缺陷

| 症状 | 根因 | 现在的闸门 |
|---|---|---|
| **蓝图永远不会落盘，而且不报错** | `ExecuteAgentExit` 只回显 `handoff`，没有回显 `blueprint` —— 而 Agent 侧的采集点读的就是返回值。提交在离开循环的那一刻静默消失 | `TestE2ERepoRunWritesBlueprintFile`（变异 M2：删掉回显 → 红） |
| R2 的 8 个 fixture 是非法 Go（`const F08 = 08`） | 八进制字面量里出现 8/9。它们在 `testdata/` 下，`go build` / `go vet` / `go test` **全都不看**，只有 `gofmt -l` 会报 | 全部改成十进制；`gofmt -l internal/blueprint/` 干净 |
| 入边渲染成 `<-tui`（一片引用它自己） | 出/入两头共用 `relEdgeName(e.From, e.To)`，入边的对端是 `From` | `TestRenderIndexSkeletonLines` 直接断 `<-app` |
| 截断行把预算顶穿 | header 先按"不含 `+N` 那行"选行，选完再把这行加回去 | 迭代收敛（omitted 单调不减）；`TestIndexRespectsTokenBudget` 断总量不超 |
| `Deriver.Run` 不听 `Close` | 只 select `ctx.Done()`，`--prompt` 收尾会挂住 | 加 `quit` 通道；`TestDeriveRequestNeverBlocks` 断 Close 后 Run 退出 |
| 先 `DeriveOnce` 再 `Run` 会扫两遍 | **Repo 用纯文本交付，写通道因此从未被触发**（实测 10 份 rollout 里 7 份 `plain_text`） | 通道设计没变（§7.2 单一入口），但没人保证 `agent_exit` 真被调用；`repo.prompt.md` 一度只在后文提"optional handoff" | prompt 加 Output Contract + 循环给 Repo 一次收尾提醒（`NoActionRetryLimit=1` + 专用文案）；闸门 `TestE2EPlainTextRepoRunIsNudgedIntoSubmitting`（变异：limit 调回 0 → 不落盘），"零提交"另有一条可区分的 Info 账 |
| `Run` 入口无条件扫描，绕过了缓存判据 | 起跑也走 `needsRescan()`（`TestCachedModeInvalidatesOnRepoToken`） |
| 段配额排反 | 排序把"最该丢的"排到前面，`ranked[len-cap:]` 又切了一刀 → 淘汰 40 条留下 5 条 | `TestSectionQuotaEvictsTheRightAtoms`（不只数条数，还断"丢的是 stale/unanchored"） |

### 21.3 闸门有效性（每条都真做过变异）

| 变异 | 命中的测试 |
|---|---|
| 删掉证据集校验（`fold.go validate` 的 `evidence[item.module]` 分支） | `TestCommitRejectsModuleNeverReadInThisRun` 红 + `TestE2ERepoRunDoesNotWriteUnreadModule` 红 |
| 不回显 `blueprint[]`（`flow_control.go`） | `TestE2ERepoRunWritesBlueprintFile` 红 |
| 注入侧不调 `Request()` | `TestE2ERepoRunWritesBlueprintFile` 红（"每轮请求一次重派生"的账目归零） |
| 短 ID 的内容 key 掺入 `time.Now()` | `TestFoldIsIdempotent` + `TestFoldOrderIndependent` 红 |
| 把 `edgeNames` 的 outbound 参数忽略 | `TestRenderIndexSkeletonLines` 红（`<-app` 变 `<-tui`） |
| `commitReReadHook` 只在第 0 次动手 → 让它每次动手 | `TestFoldGivesUpAfterTwoConflicts` 红 |
| 阈值判断 `cost >= deriveBudget` 改成恒 false | `TestDeriveModeFollowsCost` 红 |

### 21.4 键的消费者对账（§20.3 欠账清单的结清）

| 键 | 消费者 | 状态 |
|---|---|---|
| `enable` | `app.buildBlueprint`（唯一读取点，派生 / 注入 / 工具 / 钩子 / GC 五处共问） | ✅ |
| `index_max_tokens` | `RenderIndex`（形状②的硬顶 + `+N modules not shown`） | ✅ |
| `inject_into` | 三处 system prompt 注入点的开关（`viewFor`） | ✅ |
| `prose_budget_tokens` | `RenderProse`（read_blueprint 与委派前缀） | ✅ |
| `export_path` | **仍然没有消费者** —— 导出层是 §9 / R8。按 §20.3 的规矩，这条承诺本轮不结清：`docs/config-guide.md` 的 `[blueprint]` 段落**先不要写这个键** | ⏳ |

自扫 golden：45 片不变，模块级边 **141 → 144**。三条新边逐条对过 ——
`internal/agents → internal/blueprint`、`internal/app → internal/tokenutil`、`internal/app → internal/gitinfo`。
没有新增目录（片数那条断言同时证明这一点），也没有哪条黑名单失效。

### 21.5 R6.1：蓝图从未落盘的真实原因（2026-10-20）

排查"blueprint 没落盘"时先要分清两件事，因为它们的账目长得几乎一样（目录建了、文件没有）：

1. **没人提交** —— `repo` 以纯文本收尾，`agent_exit` 一次都没调。本轮已由 bba25cb（收尾提醒 +
   `NoActionReminder`）处理，账目是 `blueprint: 本次 run 不是 agent_exit 收尾，跳过落盘`。
2. **提交了但全被拒** —— 账就在 `~/.yagent/logs/app-2026-10-19.log:179-182`：
   三条 `blueprint: 提交被拒收 reason=path-not-in-module` + 一句 `本次提交没有改变文档 noop=true`。
   被点名的"路径"是 `Name/Brief/Effect/Frequency/Mutates/Serial/…`、`Cold/Hot`、`toolspec/schemas.json`
   —— 前两个是散文里的标识符列表，第三个是真的、只是没从仓库根写全。

判据错误在 I14（§7.5 第 3 条）：`looksLikePath` 把"含 `/`"当成路径断言，`Snapshot.ModuleOf` 又只认
从根写全的形态，于是**准确写法与正常中文都算违规**，而一条拒收丢整条 claim、三条全丢就没有 fold、
没有 fold 就没有文件（I20 的 NoOp 判据在这里是对的，只是喂给它的东西全被丢光了）。

修法是给归属判据补上它本来就缺的两问（`prosepath.go`，挂在 `AnchorChecker` 上）：
**"它到底指哪儿"**（含写短的前缀与裸文件名）与 **"仓库里有没有这个东西"**。
存在性不是这道的新职责 —— 凭空路径由 `where` 锚点复核兜住（第 5 条），那道本来就会判 stale。

| 变异 | 红在 |
|---|---|
| `foreignPath` 强制走 `fs == nil` 的老判据 | `TestProseSlashListIsNotAPathAssertion` / `TestProseShortenedPathsResolveToRealOwner` / `TestProseInventedPathLeavesExistenceToAnchorCheck` / `TestCommitAcceptsProductionShapedProse` / `TestE2ERepoRunWritesBlueprintFile` 全红，且拒收 detail 逐字复现生产那两行 |
| `foreignPath` 恒返回"没违规" | `TestProsePathMustBelongToModule` + `TestProseShortenedPathsResolveToRealOwner`（负向那条）+ `TestEveryRejectionReasonIsAccounted` 红 —— 真阳性仍然咬得住 |
| 撤掉存在性闸门（`existsUnderRoot` 短路） | 斜杠列表与凭空路径两条红 |

### 21.6 R6.2：拒收账目回到对话（2026-10-20）

修完 I14 之后又跑了一轮，症状仍然一样（目录建了、文件没有），但账目换了一种：

```
blueprint: 提交被拒收 reason=outside-evidence-set module=internal/tui …  ×4
blueprint: 本次提交没有改变文档 rejected=4 noop=true
```

那一轮的 rollout 只有 11 条记录、**一次读工具调用都没有**：任务是"快速架构讲解"，
模型（`Qwen3.5-35B-A3B` / effort=medium）照着注入的 45 片目录名编了 4 句 `purpose`。
I17 拦住它是**正确的** —— 蓝图不该收没读过的散文。要修的是接下来那一环：

`agent_exit` 是终止点，fold 的拒收只进 `slog`。于是 Director 收到的仍是一次成功的委派，
模型永远不知道自己白交了四条，下一轮同一批散文还会再交一次。**防线生效但没人知道防线生效过**，
和静默丢失只差一步（I18 说的是"账要能读出来"，日志也是读不出给用户看的）。

落点：`BlueprintCommit.Commit` 多返回一行账目（提交几条 / 写入几条 / 拒收原因逐条归堆、上限 3 堆 /
证据覆盖几片），经 `AgentResult.BlueprintAccount` 到两处 ——
`FormatForDirector` 的正文之外（这一次决策看的地方）与便签正文的 `> [blueprint]` 尾注
（下一次委派与索引重扫看的地方）。**不进 `conclusion`**：索引每轮从磁盘重扫，
把程序的账目混进结论等于替模型下了一句架构判断。

两处都要：工具输出会被压缩卷走，便签正文不进索引 —— 只修一边，另一边就是新的静默。
判据仍然只有 fold 一份，`blueprintAccount` 只搬结论不再校验一次。

| 变异 | 红在 |
|---|---|
| `repo.go` 丢掉返回值（`agentResult.BlueprintAccount =` → `_ =`） | `TestE2ERepoRunDoesNotWriteUnreadModule` 红（账目空串） |
| `FormatForDirector` 不附这一行 | `TestBlueprintAccountReachesDirectorTextAndNote` 红 |
| `writeDelegateNote` 不写尾注 | 同一条用例的另一半红 |
