# read_file 重构方案

> 状态：**已实施并提交**在 `feat-opt-read`（P1=`05725ba`、P2-P4=`e418f81`、P5=`a9e09d9`、P6=`e51eca4`）。四项决策见 §2，实施顺序见 §7，
> 落地结果、与方案的差异、性能数字与变异账全部在 §9。
> 所有 file:line 核对于 2026-10-26（HEAD `89c0391`）。**§1-§8 的坐标是改动前的**（描述的就是
> 那一份代码），§9 里的坐标是改动后的；两者相差的行号以 §9 为准。

---

## 1. 现状：这个工具的默认姿势是"读不全"

### 1.1 10KB 帽咬掉四分之一的源文件

本仓库自己的分布（565 个 `.go`，排除 `dist/` 与 `codeseek/`）：

```
字节  mean 7.8KB   p50 5.6KB   p90 17.9KB   p99 35.4KB   max 42.2KB
      >10KB: 142 个 (25.1%)     >50KB: 0      >100KB: 0
行数  p50 163      p90 574      p99 1061     max 1398
```

`MaxEntireFileContentBytes = 10KB`（`internal/tools/file_operations.go:119`）意味着
**25.1% 的源文件在 `should_read_entire_file=true` 时被静默截断**，模型拿到前 ~250 行 +
`truncated=true`，然后必须分页。10KB ≈ 2500 token，相对 120k 的默认压缩阈值是零头。

而每次分页是一次完整 LLM 步，上下文全量重发。粗算 30k 上下文读一个 1000 行文件：
4 次 2.5k 的分页 ≈ 132k 累计输入 token；1 次 13k ≈ 43k。**在"read_file 结果是 prio-0、
第一档压缩最先砍它"（`internal/toolspec/effect.go:161`、`internal/compression/truncate_results.go:85`）
这个既有设计下，多读是安全的，少读是贵的。** 系统自己已经声明"文件内容可重取"，
那就没理由在入口把每次读取掐到 10KB。这是整个方案的地基。

### 1.2 双模式的不对称

| | entire 模式 | range 模式 |
|---|---|---|
| 字节帽 | 10KB（`:238`） | **无**（`:247-249` 无条件 append） |
| 行数帽 | 1000（`MaxEntireFileLines`） | 1000（`MaxLineRangeSize`） |
| 文件大小前置检查 | 10MB 拒绝（`:180`） | 只有 500MB 绝对拒绝（`:165`） |
| 截断后的分页提示 | 有（`:287-290`） | **无** |
| 字节帽判定时机 | append **之前**（可超一整行） | — |

range 模式没有字节帽 + `MaxLineLength = 1MB`（`:122`）= 一次调用理论上可灌 1000×1MB。
实际会被压缩层事后砍掉，但那一次的 token 已经付了。

### 1.3 每次调用的成本形状

1. `scanner.Buffer(make([]byte, 0, MaxLineLength), MaxLineLength)`（`:207`）——**每次调用固定分配 1MiB**，
   与文件大小无关；4 路并行（`max_parallel_readonly_tools` 默认 4）= 4MiB/批。
2. 至少三份全文拷贝：`Scanner.Text()` 每行 copy → `[]string` 增长 → `strings.Join`（`:303`）→
   `json.Marshal`（`internal/tools/adapter.go:121`）再复制一份带转义的。
3. **收满区间后不早退**：`:250-253` 置 `rangeComplete` 后继续逐行扫，只为算 `total_lines`；
   `:298` 那行 `_ = rangeComplete // 保留以备未来优化` 就是这件事的欠条。读 400MB 文件的
   第 1–50 行 = 用 Scanner 完整走一遍 400MB。

### 1.4 三条静默失败

| 症状 | 根因 | 回执 |
|---|---|---|
| minified bundle 整个读不了，即使只要第 1–10 行 | 单行 >1MB → `bufio.ErrTooLong` → `:257` 返回真错误 | Go error（`Adapter.Call` 返回 `("", err)`，`adapter.go:117-119`） |
| CRLF 文件上"照抄 read_file 输出"的编辑必然失败 | `bufio.ScanLines` 剥掉行尾 `\r`，输出非字节保真；而 `search_replace_in_file` 是字面 `strings.Replace`（`internal/tools/file_edit.go:146`，全包无 CRLF 归一化） | 只有 `"old_string not found in file"`（`file_edit.go:134`），模型看不出是行尾问题 → 重试循环 |
| 读 PNG/二进制把乱码灌进 context | 无 NUL / 编码嗅探 | 无——正文照常返回，token 已付，事后才被 prio-0 砍掉 |

### 1.5 测试现状：零回归网

`ExecuteReadFile` 在 `internal/tools` **没有任何直接单测**（`file_operations_test.go` 只有两条
`TestExecutePrintDirTree*`）。外部引用只有三类，都不校验读取语义：
`internal/toolspec/probe.go:10` 的探针桩、`internal/compression/context_compressor_test.go` 拿它当
prio-0 夹具（只用工具名）、`internal/agents/parallel_admission_test.go:20`（只钉"可并行"）。
所以今天改 1000 行/10MB/2MB/CRLF/超限行/error-payload 形状，**不会有测试变红**。

---

## 2. 四项决策

| # | 决策 | 被否决的选项与理由 |
|---|---|---|
| D1 | **删掉 entire/range 双模式**，参数收敛为 `target_file`（必填）+ `start_line` + `max_lines`（均可选） | ① "只抬上限不动参数"：双模式分支、10MB 前置检查、4 个必填参数的输出 token 税、以及 §1.2 的不对称全部保留。② "加 `cat -n` 行号前缀"：每行 +2~3 token（1000 行 ≈ +2500 token/次），而本仓的编辑工具是 `old_string` 字面匹配、**不消费行号**，这笔税换不到编辑正确性。（初判把"输出无行号"当缺口，复核后认定它是取舍而非缺陷。） |
| D2 | **单次预算 = min(1500 行, 50KB)**，两种情况同一套 | 32KB 覆盖本仓 p98 但 p99(35.4KB) 以上仍要两次；100KB 单次可达 ~25k token，一批并行就能独立触发压缩。50KB 覆盖本仓 100%（max 42.2KB / 1398 行），代价见 §8.1。 |
| D3 | **`total_lines` 保持精确**，收满区间后改用 `bytes.Count` 数换行（memchr 速度、零分配） | "收满即停 + 报下界 ≥N"能让 500MB 文件零尾扫，但回执多一个三态字段，模型在"还剩多少"上只能拿到下界，分页与 `file:line` 引用准确性下降。500MB 尾扫实测预算 ~100-200ms，换契约不变是划算的。 |
| D4 | **read_file 输出字节保真（保留 `\r`）+ `search_replace_in_file` 加 CRLF 容错匹配**，两个独立 commit | "只做 read_file 侧保真"：模型抄写时可能自己丢掉 `\r`，死循环只是概率降低而非消除。"本次不碰行尾"：这是今天真实存在的重试循环来源。 |

---

## 3. 新契约

### 3.1 参数（`internal/toolspec/schemas.json`）

| 参数 | 类型 | 必填 | 语义 |
|---|---|---|---|
| `target_file` | string | 是 | **键名不变**（理由见 §5.3） |
| `start_line` | integer | 否 | 1-indexed，含；`<1` 归一为 1。默认 1 |
| `max_lines` | integer | 否 | 本次最多返回多少行，钳到 `MaxReadLines`。默认 = `MaxReadLines` |
| `explanation` | string | 否 | 不变 |

`should_read_entire_file`、`start_line_one_indexed`、`end_line_one_indexed_inclusive` 全部删除。
"读整个文件"= 只传 `target_file`，拿到预算内的全部内容 + `truncated` 告诉你还有没有。
**不留兼容分支**：旧参数被静默忽略，行为退化成"从第 1 行读到预算上限"，无害。

### 3.2 回执

```json
{
  "content": "…",              // 字节保真：CRLF 文件的 \r 原样保留
  "lines": 37,
  "start_line": 1,
  "end_line": 37,
  "total_lines": 37,           // 精确（D3）
  "lines_before_range": 0,     // 名字不变：两份 prompt 已在教它
  "lines_after_range": 0,      // 同上
  "file_size_bytes": 5632,
  "truncated": false,
  "warning": "…"               // 只在截断或非法 UTF-8 时出现
}
```

删除 `read_mode`、`max_lines_returned`、`max_content_bytes`（模式特有，已核实零外部消费者）。
保留 `lines_before_range` / `lines_after_range`：虽然可由 `start_line-1` 与 `total_lines-end_line`
推出，但 `internal/agents/repo.prompt.md:93` 与 `internal/agents/coding.prompt.md:128` 已经把它们当
规划依据在教，省下的是模型的一次减法错误，成本 ~8 token。

**截断时的 `warning` 必须给出确切的下一步**（今天只有 entire 模式给，range 模式不给）：

```
Truncated at 50.0 KB / 1500 lines (file has 4231 lines). Continue with start_line=1501.
```

### 3.3 限制阶梯

| 常量 | 现在 | 之后 |
|---|---|---|
| `MaxFileSizeForRangeRead` | 500MB 绝对拒绝 | **保留**（防的是扫描成本，不是输出成本） |
| `MaxFileSizeForEntireRead` | 10MB 整读拒绝 | **删除**（输出已有字节帽，文件多大不再影响输出体积）。⚠️ 见 §5.2 的第二个消费者 |
| `SoftFileSizeLimit` | 2MB 软警告 | **删除**——它建议的正是唯一剩下的模式，且回执已带 `file_size_bytes` + `total_lines` |
| `MaxEntireFileLines` 1000 / `MaxLineRangeSize` 1000 | 两个 | 合并为 `MaxReadLines = 1500` |
| `MaxEntireFileContentBytes` 10KB | 只约束 entire | 改名 `MaxReadContentBytes = 50 * 1024`，约束所有读取 |
| `MaxLineLength` 1MB | Scanner 缓冲上限，超了整读失败 | **删除**——手写切分下超长行由字节帽自然截断 |

字节帽判定改为 **append 之后不超**（今天 `:238` 是 append 之前判，可超一整行）：
`contentBytes + len(line) <= budget` 才收。

**首行就超预算的特例**（minified bundle）：不能返回 0 行——那比返回一个 50KB 前缀更糟。
规则：若一条行都还没收就撞上字节帽，收它的前 `budget` 字节并置 `truncated=true`。

### 3.4 两种失败语义（保持不变）

- **给模型看的 error**：`return map{"error":…, "suggestion":…}, nil`（今天 `:166`、`:181`、`:268`）。
  Go error 为 nil → 工具日志记 `success=true`、TUI 不报红。这是"教模型换姿势"的刻意设计，保留。
  新增两处走这条路：500MB 拒绝（已有）、二进制拒读（新增）。
- **真错误**：`util.WrapError` → `Adapter.Call` 返回 `("", err)`。只剩 Stat/Open/IO 失败。
  **`scanner.Err()` 这条路径整体消失**（不再有 Scanner）。

---

## 4. 实现要点

### 4.1 读取器：手写 chunked 切分替代 `bufio.Scanner`

```
64KB chunk 循环读入复用的 buf
  bytes.IndexByte 找 \n（memchr，Go 里有 SIMD 优化）
  跨 chunk 的行：累积进 lineBuf，但最多累积到 budget+1 字节，
                之后只找换行、丢弃内容（行号照数）→ 永不 ErrTooLong
  命中 [start_line, start_line+max_lines-1] 且未超预算 → 写进单个 []byte 累加器（含 \r）
  收满（行数或字节）→ 停止收集，转入尾扫
最后一次 string(acc) → 一份输出，不再有 []string + Join
```

消除的开销：1MiB/次的固定分配、每行一次 copy、`[]string` 增长、`strings.Join` 的整份复制。
预期从"至少三份全文"降到"一份"。

### 4.2 尾扫（D3）

收满后不再逐行处理，对剩余 chunk 直接 `bytes.Count(chunk, newline)` 累加。
`total_lines` 保持精确，成本是 memchr 带宽（数百 MB/s 到 GB/s 级），无分配。

### 4.3 二进制与编码

- 前 8KB 含 NUL → error-payload 拒读：`"binary file (NUL bytes detected); use search_by_regex or an appropriate viewer"`。
- 非法 UTF-8 → **只 warn，不拒**（`utf8.Valid` 检查已收集内容）：
  `"content contains invalid UTF-8; bytes may be mojibake"`。
  理由：latin-1 之类的文本文件是合法的，一刀切拒读会误伤。

### 4.4 测试 seam

`MaxFileSizeForRangeRead` 与 `MaxReadContentBytes` 写成 `var` 而不是 `const`，
照 `runBashWaitDelay`（`internal/tools/system_operations.go:22`）与 `maxShellOutputBytes`（`:26`）的既有先例。
否则"500MB 拒绝"这条分支在测试里无法触发——而 `scripts/check_test_hygiene.sh` 的纪律是
**新写一条校验就同时写一条"它必须报"的夹具**。

---

## 5. 波及面

### 5.1 必改

| 文件 | 改什么 |
|---|---|
| `internal/tools/file_operations.go:104-335` | 常量表（§3.3）+ `ExecuteReadFile` 重写（§4） |
| `internal/toolspec/schemas.json` read_file 条目 | description 重写（今天的描述整段在讲双模式与 10MB/2MB 规则）、properties 换成 §3.1、`required` 缩成 `["target_file"]` |
| `internal/tools/specs_file.go:41` | `Brief` 改成与新契约一致的一句话 |
| `internal/tools/file_edit.go:129-146` | CRLF 容错匹配（§5.4，独立 commit） |
| `internal/messaging/consumers/tui.go:371-377` | 补 `target_file` 键——**只影响 HTTP 模式写 stdout 的旧 consumer**（`internal/http/task_executor.go:56`、`websocket.go:215`）；真 TUI 自己的 `extractToolSummary`（`internal/tui/tui_render.go:517,524`）早就认它 |
| `internal/agents/coding.prompt.md:118,126-138` | 双模式那整段规则换成新参数 + 新预算 |
| `internal/agents/repo.prompt.md:73-79,93` | 同上；`:93` 的两个字段名保留 |
| `internal/agents/director.prompt.md:121` | Rule 1 的直读门槛写死 "<200 lines, <10KB" → 与新预算对齐 |
| `internal/agents/devops.prompt.md:21` | 只提工具名，核对后大概率不用改 |

### 5.2 ⚠️ `MaxFileSizeForEntireRead` 有第二个活消费者

`internal/tools/system_operations.go:26`：

```go
var maxShellOutputBytes = MaxFileSizeForEntireRead   // run_bash 输出封顶
```

删常量会直接编译失败——这是好事（不是静默漂移），但**必须先解耦**：
`maxShellOutputBytes` 拿到自己的字面量 `10 * 1024 * 1024` 与自己的注释。
两个数字从今天起表达的是两件不同的事（read_file 的单次输出预算 vs run_bash 的排空缓冲上限），
共用一个常量正是 `internal/tools/AGENTS.md` 硬约束 14 那句"数值口径住在这两个文件"要盯的形状。

**顺带发现，本次不修**：run_bash 的输出上限是 10MB 且同样逐字进 context
（`internal/agents/executor.go:843-855` 无单条上限）。它也是 `TruncFirst`
（`internal/tools/specs_file.go:188`），所以事后能被第一档砍掉，但那一次的 token 突发比
read_file 新预算大两个数量级。要不要给 run_bash 也定一个"进 context 的"预算，是独立议题。

### 5.3 明确不动（含理由）

- **`target_file` 键名不动**：`internal/agents/blueprint_evidence.go:25-29` 的 `pathArgKeys`
  按**参数键名**收集"这轮真打开过哪些文件"，作为蓝图提交的证据集。改键名不报错，
  只会让 read_file 的路径从证据集里静默消失 → 提交被判"没读就说"而拒绝。
- **`internal/agents/testdata/tool_surfaces.golden` 不动**：它只钉工具名 + `tier/ro/serial/auth`
  能力标签，不钉 schema。参数改动不触发 golden 重写。（`ComputeToolDefsHash` 的日志值会变，属预期。）
- **`internal/toolspec/probe.go:10`** 探针桩签名不变。
- **`internal/diff`** 不变（它只服务 `search_replace_in_file` 的回显装饰）。
- **不加配置键**：预算是常量，住 `file_operations.go`（硬约束 14 的既定单一口径）。
  加键就得配一个"改值有可观察行为变化"的测试（根 AGENTS.md §9.2），不值得。
- **不给 read_file 加工作区守卫**：`EffectFSRead` + `NeedsAuth=false` → `workspace_guard.go:64`
  直接放行，绝对路径原样使用（`file_operations.go:28-30`）。加授权会制造持续弹窗；
  本仓对 `run_bash` 的既定立场也是"无黑名单、防御即可见性"。留作独立议题。

### 5.4 CRLF 容错匹配（D4 的第二半，独立 commit）

`file_edit.go:131-146` 今天的路径是：`strings.Contains` → `strings.Count` 唯一性 → `strings.Replace`。
新增的回退只在**精确匹配失败时**才走，所以 LF 文件行为逐字不变：

```
1. 精确匹配成功 → 原路径，零变化
2. 失败且 content 是"一致 CRLF"（每个 \n 都紧跟在 \r 后）：
     norm := 去掉所有 \r\n 的 \r；oldNorm 同样处理
     要求 Count(norm, oldNorm) == 1（唯一性判据走同一归一化）
     替换后把结果整体转回 CRLF，回执加 "line_endings": "crlf-normalized"
3. 失败且文件是混合行尾 → 保持 "old_string not found"，但补一句
     "file has mixed line endings; old_string did not match byte-for-byte"
```

只在"一致 CRLF"时自动转换、混合行尾时显式报错，是为了不把行尾改坏——
静默重写整个文件的行尾比一次编辑失败更糟。回执里那个 `line_endings` 字段就是
根 AGENTS.md §11 坑 43 要求的"被拦下来/被兜住的那一次，谁看得见"。

### 5.5 AGENTS.md 棘轮账（实测）

`bash scripts/check_agents_md_size.sh` 当前 OK（18 份 / 2747 行 / 冻结 3 项）。

| 文档 | 现在 | 余量 | 要写什么 |
|---|---|---|---|
| 根 `AGENTS.md` | **608/608** | **0** | §5.4 "大小限制"那一行（今天写的是"编辑双向 10MB；整读 10MB、软警告 2MB、单次 1000 行"）必须换成新阶梯。零余量 → **逐行换置，不许新增行**；抬 PENDING 数字要你点头 |
| `internal/tools/AGENTS.md` | 140/200 | 60 | 硬约束 14 重写（常量名单变了 + `maxShellOutputBytes` 解耦）；闸门节补 §6 的用例名 |
| `internal/agents/blueprint_evidence.go` 相关文档 | — | — | 不动（键名未变），但要在硬约束里点明"改 read_file 参数名会静默丢证据"这条陷阱 |

---

## 6. 测试与变异清单

### 6.1 回归网（新建 `internal/tools/file_operations_read_test.go`）

| 用例 | 钉什么 | 为什么能失败 |
|---|---|---|
| `_ReturnsWholeFileWithinBudget` | 小文件一次读完，`truncated=false` | 预算被改小即红 |
| `_ByteBudgetIsHardCeiling` | `len(content) <= 50KB`，含"首行就超预算"的 minified 形状 | 把判定挪回 append 之前即红（今天的行为） |
| `_LineBudgetIsHardCeiling` | 1500 行上限 | 调 `MaxReadLines` 即红 |
| `_PreservesCRLF` | CRLF 文件的 `\r` 原样出现在 content 里 | 换回 `ScanLines` 即红 |
| `_NoTrailingNewlineStillCountsLastLine` | 末尾无换行的文件 `total_lines` 正确 | 尾扫 off-by-one 即红 |
| `_TotalLinesExactWhenRangeIsSmall` | 读 5000 行文件的第 1–10 行，`total_lines == 5000` | 改成早退报下界即红（D3 的反面） |
| `_HugeSingleLineDoesNotFail` | 单行 5MB 的文件读第 1 行不返回 Go error | 恢复 `ErrTooLong` 路径即红 |
| `_BinaryRefusedWithSuggestion` | 含 NUL → error-payload + `suggestion` | 去掉嗅探即红 |
| `_InvalidUTF8WarnsButReturns` | 非法 UTF-8 只 warn | 改成拒读即红（误伤 latin-1） |
| `_StartBeyondEOF` | error-payload 带 `total_lines` 与合法区间提示 | 去掉后置校验即红 |
| `_OversizeFileRefused` | 用 seam 把上限调到几 KB，触发 500MB 分支 | 没有 seam 这条根本测不到 |
| `_PaginationHintOnTruncation` | 截断时 `warning` 含确切的下一个 `start_line` | 提示写错即红 |
| `_EmptyFile` | 0 字节文件的回执形状 | — |

`search_replace_in_file` 侧（另一个 commit）：
`_CRLFFallbackMatchesAndRestoresEndings`、`_MixedEndingsRefuseWithHint`、
`_LFFilesUnaffectedByFallback`（第三条钉"精确匹配成功时不走回退"，防止回退吃掉唯一性判据）。

### 6.2 变异验证（每条都要真做，确认当场红）

1. 字节帽判定挪到 append 之前 → `_ByteBudgetIsHardCeiling` 红
2. 保留 `\r` 的切分换回 `ScanLines` → `_PreservesCRLF` 红
3. 尾扫 `bytes.Count` 换成收满即 break → `_TotalLinesExactWhenRangeIsSmall` 红
4. 去掉 NUL 嗅探 → `_BinaryRefusedWithSuggestion` 红
5. file_edit 的 CRLF 回退无条件生效（不先试精确匹配）→ `_LFFilesUnaffectedByFallback` 红
6. 混合行尾也自动转换 → `_MixedEndingsRefuseWithHint` 红

### 6.3 性能探针（before/after 同一条）

`go test ./internal/tools -run=XXX -bench=ReadFile -benchmem`，夹具三档：
5.6KB/p50 源文件、42KB/max 源文件、200MB 稀疏日志（读第 1–1000 行）。
记录 `ns/op` 与 `B/op`、`allocs/op` 三列，改前改后各跑一次，数字写进 §9。
**不写"等真实超时"的计时断言**（`check_test_hygiene.sh` 与根 AGENTS.md §8.3 的纪律）；
benchmark 只做记录，不做门禁。

---

## 7. 实施顺序

| 步 | 内容 | 独立验证 |
|---|---|---|
| P1 | `maxShellOutputBytes` 解耦成自己的字面量（§5.2） | `go build ./... && go test ./internal/tools` 全绿，行为零变化 |
| P2 | §6.1 的回归网**先按今天的行为写一遍**（不含 CRLF/二进制那几条） | 绿——这一步是"确认夹具真的在跑被测分支" |
| P3 | 读取器重写 + 新契约（§3、§4）+ P2 的用例改钉新契约 + 新增 CRLF/二进制用例 | 全部用例绿 + 6.2 变异 1-4 各自红 |
| P4 | schema + 4 份 prompt + Brief | `TestSchemasLoadAndDescribeEveryEntry`（`internal/toolspec/registry_test.go:418`，基线条数钉在 `:423` = 50，本次不增删工具所以不变）、golden 不变 |
| P5 | `search_replace_in_file` CRLF 容错（独立 commit） | 6.1 后三条用例 + 变异 5、6 |
| P6 | 文档与棘轮结算（§5.5）+ HTTP consumer 补键 + benchmark 数字写进 §9 | `bash scripts/check_all.sh`；`RACE=true` 再跑一次（read_file 是可并行只读工具） |

P3 与 P4 必须同一个 commit（根 AGENTS.md §4："改行为要同时改 prompt 与代码"）。

---

## 8. 风险与代价（如实说）

### 8.1 并行批次的 token 突发

`max_parallel_readonly_tools` 默认 4、上限 8，read_file 是只读可并行工具。
一批 4 次读满 = 200KB ≈ 50k token，能把默认 120k 的压缩阈值一次顶过去。
**补偿**：read_file 是 prio-0，第一档会先把它砍到 `tool_result_keep_tokens`（默认 200），
所以最坏情况是"多付一次昂贵输入"，不是上下文崩掉。这条账要在 §9 用真实会话验一次。

### 8.2 模型习惯旧参数

prompt 改了，但模型可能凭训练习惯继续发 `should_read_entire_file`。
新执行器忽略未知键 → 退化成"从第 1 行读到预算上限"，无害但会让 `truncated` 意外为真。
**不加兼容分支**（仓库既定立场：不写 backwards-compat shim）；如果实测频繁，
在 schema description 里点名"there is no should_read_entire_file parameter"即可。

### 8.3 老会话重放

重放不重跑工具（`agent_checkpoint` 只在终止路径落一条，恢复走 rollout 重建消息），
所以旧 rollout 里的 `should_read_entire_file` 参数只是历史文本，不触发新代码路径。无需处理。

### 8.4 零余量的根 AGENTS.md

608/608。§5.4 那一行必须逐行换置；如果新阶梯写不下，就得在别处删一行，
或者显式抬 `check_agents_md_size.sh` 的 PENDING 数字（= 承认一笔债，要你点头）。

---

## 9. 实施记录

（实施于 2026-10-26，按 §7 的 P1→P6 顺序；`check_all.sh` 与 `RACE=true` 版本均全绿。
四个 commit 逐个在临时 worktree 里验过独立 `go build ./...` + `go test`，历史里没有断掉的中间态。）

### 9.1 落地范围

| 步 | 结果 |
|---|---|
| P1 | `maxShellOutputBytes` 拿到自己的 10MB 字面量（`system_operations.go:28`；这条注释扩了两行，把该文件后面 6 处引用整体推了 +2，已在 `internal/tools/AGENTS.md` 同步） |
| P2 | `file_operations_read_test.go` 先按**今天的行为**写了 14 条，全绿——证明夹具真的在跑被测分支（两条中途废弃的夹具见 §9.3 第 5 项） |
| P3 | `readFileInRange` 手写切分 + 新契约；P2 的用例翻到新契约并补到 23 条 |
| P4 | schema（描述重写、`required` 缩到 1 个）+ Brief + 3 份 prompt 同步（devops 那份只列工具名，核对后没改） |
| P5 | `applyReplace` / `isUniformCRLF` + 7 条闸门（`file_edit_crlf_test.go`） |
| P6 | `consumers/summary_test.go`（那个包此前**零测试**）+ 文档结算 + 探针 |

### 9.2 性能探针（同一条夹具与调用点，`-benchmem`，默认 benchtime）

| 夹具 | BEFORE（HEAD `89c0391`） | AFTER | 倍数 |
|---|---|---|---|
| p50 源文件 5.6KB/300 行 | 116,331 ns · 1,072,135 B · 321 allocs | 23,977 ns · 30,896 B · 29 allocs | 4.9x 快 · 内存 34.7x 少 |
| max 源文件 42KB/1400 行 | 162,327 ns · 1,125,547 B · 1435 allocs | 71,222 ns · 262,128 B · 36 allocs | 2.3x 快 · **而且这次不再截断**（旧写法 10KB 帽只给前 1000 行） |
| 21MB 日志只读前 1000 行 | 11,863,408 ns · 25,161,196 B · 300,046 allocs | 2,154,312 ns · 271,400 B · 45 allocs | 5.5x 快 · 分配次数 6,668x 少 |

before 一侧是 `git archive HEAD` 的干净副本跑的。1.07MB/次 那个数就是 §1.3 第 1 条的 1MiB Scanner 缓冲；
30 万 → 45 次分配是"每行一次 copy + `[]string` 增长 + Join"三处一起消掉的结果。

### 9.3 与方案的差异（都是实施中发现方案写错或写漏）

1. **常量改名 + 改成 var**。方案写的是 `MaxReadLines`/`MaxReadContentBytes` 且保留
   `MaxFileSizeForRangeRead`；实际落地是 `maxReadLines`/`maxReadContentBytes`/`maxFileSizeForRead`
   三个包内 var（`file_operations.go:107-113`）。理由：§4.4 要它们当测试 seam，导出常量做不到；
   而 `RangeRead` 这个词在 D1 之后已经没有指代对象。全仓无外部引用（已 grep 确认）。
2. **§3.3 的"首行就超预算收下前缀"特例是死代码**。变异 M6 抓出来的：`addSeg` 已经把每一行
   封顶在 budget 以内，所以 `len(line) > budget` 恒假、那个分支永远进不去。删掉；
   行为由 `addSeg` 的行内封顶承担（M6 现在会让两条用例红，见 §9.4）。
3. **§5.4 漏了一整类事故：精确命中并不等于行尾安全**。`old_string` 是单行时它必然精确命中，
   而 `new_string` 是多行 LF 文本 —— 直接写进 CRLF 文件就当场造出混合行尾。这是**改动前就存在**的
   bug（与 read_file 无关），现已补上 `crlf-applied` 一支。所以 `applyReplace` 的返回值从
   方案设想的 bool 变成 `(内容, 行尾记账, 错误)` 三值。
4. **`truncated` 需要一个方案没写的判据**。§3.2 的样例把"缺省读撞行帽"和"显式区间装满"混为谈：
   前者该 `truncated=true`（要继续），后者不该（模型要的就是这几行）。落地是 `cappedWindow`
   （缺省 `max_lines`、或显式要了超过行帽的行数 → true），只有它撞行帽才置位。
   这条判据由变异 M5 钉住。
5. **两条 P2 夹具被自己否掉**：① 稀疏零文件当">2MB 软警告"夹具会踩 `ErrTooLong`（根本没有换行符，
   整文件是一行）；② "1200 行 × 6B" 想测字节帽其实只测到行帽。前者随 `SoftFileSizeLimit` 一起消失，
   后者拆成 `_LineCapBinds` + `_ByteCapIsHardCeiling` 两条各自带前置断言。
6. **多加了一条方案没列的闸门**：`consumers/summary_test.go`。P6 要补的那个键落在一个
   零测试的包里，不补就等于下一次还是静默空着。
7. `e2e_blueprint_test.go` 三处夹具里的 `should_read_entire_file` 一并删掉（参数已不存在，留着是误导）。

### 9.4 变异账（12 条，全部真做，逐条取 exit code）

M1 字节帽判定挪回 append 之前 → `_ByteCapIsHardCeiling` 红 ·
M2 剥掉 `\r` → `_PreservesCRLF` 红 ·
M3 尾扫不数换行 → 6 条红 ·
M4 去掉 NUL 嗅探 → 2 条红 ·
M5 truncated 丢掉 `cappedWindow` → `_ReturnsRequestedLineRange` 红 ·
M6 `addSeg` 不给单行封顶 → 2 条红 ·
M7 行帽不再停止收集 → 6 条红 ·
M8 CRLF 回退无条件生效 → 2 条红 ·
M9 混合行尾也自动转换 → `_MixedEndingsRefuseWithoutRewriting` 红 ·
M10 精确命中不转 `new_string` 行尾 → `_ExactMatchStillKeepsFileUniformCRLF` 红 ·
M11 回退不查唯一性 → `_CRLFFallbackStillRequiresUniqueMatch` 红 ·
M12 回退不还原成 CRLF → `_CRLFFallbackMatchesAndRestoresEndings` 红。

顺带发现一条**与本次无关的既有 flaky**：`TestKillLiveProcessGroupsEndsInFlightCall` 整包并发跑
在 HEAD 干净副本上 5 次红 2 次、单跑 10 次全绿。已在 `internal/tools/AGENTS.md` 第 17 条闸门处点名，
免得下次有人红了自己的改动却去查这条。

### 9.5 文档结算

根 `AGENTS.md` §5.4 那一行逐行换置，**仍是 608/608**（没抬任何 PENDING 数字）；
`internal/tools/AGENTS.md` 硬约束 14 重写、16 补解耦说明、**新增硬约束 18**（行尾契约）
与两条闸门，164/200。§11 坑索引没动（这次的教训都落在包内文档，不是跨模块判据）。

### 9.6 仍待验证（只有真终端能跑）

1. **模型会不会继续发旧参数**（§8.2）。新执行器忽略未知键、退化成"从第 1 行读到预算上限"，
   不报错，但 `truncated` 会意外为真。跑几个真实任务看回执里有没有这个形状。
2. **§8.1 的 token 突发账**：一批 4 次并行读满 = 200KB ≈ 50k token，会不会比改动前更早触发压缩。
   看 TUI 的 ⧉ 面板与 `context_*_compressed` 事件计数。
3. **CRLF 仓库上的编辑成功率**：需要一份 Windows 检出的 CRLF 仓库才能验，本机上测不到回退那条链路
   （单测是真夹具，但真实模型行为只有跑起来才知道）。
4. run_bash 的 10MB 输出照样逐字进 context（§5.2 末段），本次刻意没动。
