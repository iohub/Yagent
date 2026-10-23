# internal/blueprint — 仓库蓝图

> 范围：本目录内不可派生的约束。完整设计见 `docs/repo-blueprint-design.md` v4 + §21 实施记录，
> 全局摘要见仓库根 `AGENTS.md` §6.4。
> 注意：`section.go:1-11` 的包文档还写着"本包只做 R1 的骨架"，与 R1–R6 已落地不符——那是注释过期，
> 不是功能缺失。

## 边界

蓝图的**存储与算法层**：零 LLM 扫描器派生结构事实、把 `agent_exit.blueprint[]` 提交的散文折成
原子条目落盘、渲染常驻索引。不负责装配与开关（`internal/app` + `config.BlueprintEnabled()`）、
不负责 prompt 与委派、`read_blueprint` 工具本体在 `internal/tools`。

## 硬约束

**1. 叶子包。** 只准 import 标准库 + `util` + `projectid`，由 `scripts/check_blueprint_leaf.sh` 守。
仓库读数（gitinfo）与 token 计数器（tokenutil）由装配层**做成参数传进来**：
`RepoToken`（`derive.go:47-51`）、`CountTokens`（`index.go:37-39`）、Git 三项（`fold.go:60-61`）。
启用判据**不在本包**，本包也不读环境变量（`section.go:9`，I8）。

**2. 派生的不落盘，落盘的不派生。** front-matter 的计数由 Render 从正文重算（`doc.go:29-30,106-110`）；
derived 段不在 `AssertedSections`（`section.go:30-39`）；模块边界由扫描器定，**模型不许造模块**
（`fold.go:246-249`）。结构层不落盘 ⇒ 模型没有任何途径写坏它。

**3. fold 是确定性纯函数。** `Merge` 不碰文件系统（`fold.go:101`）；幂等靠"短 ID 由内容派生 +
段内按 `(where, claim)` 稳定排序 + 同 `module+field` 非空才覆盖"（`merge.go:12-17`，I20）。
排序漂走的后果不只是"字节变了"，还会让 provider 端前缀缓存与人类 review 同时失效。

**4. 正文逐字没变就不算一次 fold（NoOp）**（`fold.go:160-166`），`Commit` 把"全被拒收"也归 NoOp
（`commit.go:48-56`）。理由：I20 的"字节不变"与 I18 的"没更新 ≠ 更新失败"会打架。

**5. 第五道校验不拒收，只标 stale。** 前四道在 `validate()`（`fold.go:213`），锚点复核只把状态标出来
（`:138-154`）。每个 `RejectReason` 都必须有一条能读出来的账（`:15-32`，I18）——新增拒收原因必须
同时有夹具，`TestEveryRejectionReasonIsAccounted`（`fold_test.go:320`）会盯。

**6. 散文路径校验判不了就弃权**（`prosepath.go`，I14）。曾经的形状判定把中文技术散文里的
`Name/Brief/Effect` 斜杠列表和写短的前缀路径全判成违规，一条拒丢一条 claim、全丢就是 NoOp ⇒
**蓝图文件永不出现**，而症状与"模型根本没提交"逐字相同（根 AGENTS.md §11 坑 42/43）。
存在性由 `where` 锚点管，那是第 5 条的活。

**7. 乐观 CAS，不用锁文件。** 读 → fold → 写前重读 → 变了就对着最新内容重放同一个 fold；
两次冲突返回 `ErrFoldConflict`（`commit.go:15-34`）。**调用方必须把它当"下次自然补上"，
不是任务失败**——把降级写成 error，Director 就以为委派失败了，于是把已完成的工作重做一遍。
归档（`revisions/`）写不下去不拦本次提交（`commit.go:79-85`），但 GC 删孤儿时归档失败就**不删**（`gc.go:63-66`）。

**8. stale 是粘滞的。** 没有后台进程会补写，一片过期之后只有下一次 Repo 真的覆盖它才修——
所以索引行必须写成**行动理由**（`index.go:227-231`），把"值不值得为新鲜度花一次委派"留给有任务
上下文的一方。指纹不符时保留散文（`freshness.go:192-219`）：清空会把可用信息变成不可用。
可见集合 == 派生集合，改名/删除的旧片既渲染不出也读不到，90 天才从文件摘除（`gc.go:22`）。

**9. 两条根护栏。** 拒 `$HOME` / `/` / 非 git 且无 manifest（`guard.go:40-65`）；**存储根不得落在
项目内**（`guard.go:66-89`，I7）——进了工作区就会被 `git status` 算成 dirty，而
`[git_checkpoint] stash_dirty_worktree` 用的是 `--include-untracked`，会把真源收进 stash。
拿不到 home 时报错而不是退回 CWD（`store.go:40-56`）。`projectID` 唯一来源 `projectid.Compute`（I12）。

**10. 快照不可变 + 唯一写者。** `atomic.Pointer`（`derive.go:10-16`、`scan_types.go:12`）；
`Request` 永不阻塞（`derive.go:147`）；扫描失败**不清空旧快照**（`:196-204`）；成本模式只在第一次
取样时定（`:210-215`，150ms 分界自带到期条件 `:26-33`）。schema 写了就必须有人执行：
`MaxSubmitItems=8` 在 `Merge` 里强制（`fold.go:117-132,265-276`）。
默认常数与 `config` 的模板值靠 `internal/config/blueprint_defaults_test.go` 钉同源。
落盘只走 `util.AtomicWrite` 一处（`doc.go:155-167`）。

## 入口

`doc.go`（文档模型，以及两条 CAS 相关函数为何分开）、`fold.go`（校验与拒收账目）、
`derive.go`（派生快照）、`index.go`（形状②渲染与四态）、`scan.go`（扫描器入口）。

## 闸门

- `fold_test.go:128,159,185,239`：幂等 / 乱序输入产出同一份字节 / CAS 重放 / 放弃——
  改排序或 ID 派生即红。
- `fold_prose_test.go:25,40,60,71`：坑 42 的四种散文形状（含"与违规串同形的正常散文"）。
- `freshness_test.go:103`：指纹不符时删散文即红。
- `index_test.go:73,131,166`：四态必显、token 硬顶、前缀字节稳定。
- `guard_test.go:51`、`store_test.go:36`、`gc_test.go:53` + `prose_test.go:32`、
  `derive_test.go:65,128,239`、`doc_test.go:37,66`。
- `scan_golden_test.go:68,77`：43 片 / 137 条边的自扫 golden（逐条归因写在该测试头上的注释里；
  2026-10-22 随知识机制删除从 45/144 变来，2026-10-23 因删掉 `internal/tui/anim` 整包从 44 片变到 43 片）。
  **改扫描器前先跑 `gofmt -l internal/blueprint/testdata`**——`testdata/` 是 Go 工具链的盲区，
  `go build/vet/test` 都不看它（根 AGENTS.md §11 坑 41）。
- 端到端验收在**另一个包**：`internal/agents/e2e_blueprint_test.go:127 TestE2ERepoRunWritesBlueprintFile`
  （真实 Repo 循环 + 真实扫描器 + 真实 guard，断言就是"文件出现"）。
