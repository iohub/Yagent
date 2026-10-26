# run_bash 前台调用永久卡死 — 定位与修复方案

> 状态：P0 与 P1 均已定案（P1 选 **a：杀组 + 接管信号**），待实施。
> 现场：2026-10-26 10:45，DevOps-Agent 一次 `nohup node proxy.mjs … &` 调用。

## 1. 症状

`~/.yagent/data/rollout/ws_0352237f/20261026_104454_…_devops.jsonl` 的第 36 行（最后一行）是那次
`run_bash` 的 `function_call`，**没有配对的 `function_call_output`**；`~/.yagent/logs/tool-2026-10-26.log`
是 0 字节——`logToolCall` 在 `ad.Call` 返回之后才调（`internal/agents/executor.go:163`），所以它一次都没跑过。
TUI 侧表现为这一步永远转圈，Esc / `-Q` 都解不开。

那次调用的参数（模型自己写的）：

```json
{"command": "cd /Users/ydl/projects/ws/qoder-oracle && nohup node proxy.mjs --port 8799 --verbose > /tmp/qoder_proxy.log 2>&1 & echo \"PID=$!\"",
 "is_background": false, "is_dangerous": true}
```

会话被手工终止后，进程还留在机器上：

```
79808  ppid=1  S+  bash -c cd … && nohup node proxy.mjs --port 8799 … & echo "PID=$!"
79809  ppid=79808 S+ node proxy.mjs …          # 仍在 LISTEN 127.0.0.1:8799
lsof -p 79808:  1 PIPE ->0x62b2…   2 PIPE ->0x62b2…    # 握着 Go 在读的那根管道
```

`/tmp/qoder_proxy.log` 里服务其实 67ms 就启动成功了。卡住的不是命令，是**收结果的那一侧**。

## 2. 根因

三段，互相独立，必须分开修：

**(1) `Wait()` 的等待条件里含"管道 EOF"，而 EOF 由孙进程决定。**
`is_background: false` 走 `internal/tools/system_operations.go:64-67` 的
`exec.CommandContext` + `CombinedOutput()`。`&` 的作用域是整个 `cd … && nohup node …` 列表，
被后台化的是那个 bash 子 shell，它在 `waitpid` 一个常驻服务。直接子进程打完 `echo` 就退了，
但管道写端仍被子 shell（以及 node）持有 → `Wait()` 里的 I/O 拷贝 goroutine 永远等不到 EOF。

**(2) ctx 看门狗在这个形状下打在空气上。**
Go 的 `exec` 只在"ctx 结束时进程还没退"时才调 `Cancel`/kill。这里进程**已经退了**，所以
`internal/agents/executor.go:151` 的 `defaultToolTimeout`(180s)、Director 侧
`internal/agents/director.go:852` 的 120s、`activity` 的 idle/total 看门狗、用户按 Esc 触发的取消
——全部只取消 ctx，而阻塞点根本不看 ctx。**这条卡死是无界的**，不是"最多等 180 秒"。

这直接推翻了仓库里四处注释的前提（`internal/pause/gate.go:14`、`internal/tui/tui_view.go:465`、
`internal/tui/tui_update_commands.go:165`、`internal/agents/executor_parallel_test.go:530`
都写着"一个在飞的工具最长要等完 `defaultToolTimeout`"）。

**(3) 回执口径不诚实。** 就算超时能生效，今天的形状是：`CombinedOutput` 返回
`err = "signal: killed"`，`errors.Is(err, context.DeadlineExceeded) == false`（实测），
于是 `executor.go:157` 那条"tool execution timed out after N seconds"分支**对 run_bash 从来不触发**，
模型只看到 `{"success":false,"error":"signal: killed"}`。

### 实测数据

同一根探针（deadline 3s，命令形状 `… & echo "PID=$!"`）：

| 变体 | 返回时刻 | 结果 |
|------|----------|------|
| 现状（`CombinedOutput`，无 WaitDelay） | **>12s 仍未返回** | 永久阻塞，deadline 无效 |
| `+ WaitDelay=500ms` | 505ms | `err=exec.ErrWaitDelay`，`out="PID=82199\n"`，`ProcessState.ExitCode()=0`，**孙进程存活** |
| `+ Setpgid + Cancel(kill -pgid)`，子进程不退出，deadline 2s | 2.001s | `err=signal: killed`，**进程组零残留** |
| 现状，子进程不退出，deadline 2s | 2.001s | `err=signal: killed`，`DeadlineExceeded=false`，`exitCode=-1`，`exited=false` |

第二行是关键：**加 `WaitDelay` 之后，模型那次"写错的"调用会得到它真正想要的结果**——
拿到 `PID=`、服务活着、调用返回。第三行说明"杀干净整个进程组"需要 `Setpgid`，
而在第二行那个形状里 `Cancel` 根本不会被调用。两者是两个独立机制，不是一个。

## 3. P0 — 把等待变成有界的（必做，无回归风险）

只动 `internal/tools/system_operations.go` 的前台分支。

```go
// 测试 seam（与 agents.defaultToolTimeout 同理）：不改它的话每条用例都要付 2 秒。
var runBashWaitDelay = 2 * time.Second

// 与"一次工具结果的体量上限"同源，不新造数字（internal/tools/AGENTS.md 硬约束 14）。
const maxShellOutputBytes = MaxFileSizeForEntireRead

// 前台执行
cmd := exec.CommandContext(ctx, "bash", "-c", command)
cmd.Dir = t.workingDir
// 直接子进程退出后 exec 的看门狗不再触发 Cancel，能解开"孙进程握着输出管道"这道 EOF
// 等待的只有 WaitDelay。
cmd.WaitDelay = runBashWaitDelay
out := &util.CappedBuffer{Max: maxShellOutputBytes}
cmd.Stdout = out
cmd.Stderr = out
runErr := cmd.Run()
```

`WaitDelay = 2s` 的取值理由：正常命令在进程退出那一刻 I/O 就 EOF 了，**一秒都不多付**；
只有"孙进程握着管道"这一形状付 2s，而这 2s 正好收得到服务的启动行（实测那个 proxy 67ms 就打印完了）。

### 回执按事实分四类

```go
switch {
case runErr == nil:
    // 现状不变
case errors.Is(runErr, exec.ErrWaitDelay):
    // 命令自己退了（真 exit code 就在 ProcessState 里），只是还有子进程握着输出流
    return map[string]any{
        "success":   cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0,
        "exit_code": exitCodeOf(cmd.ProcessState),
        "output":    out.String(),
        "truncated": out.Over(),
        "note":      "命令已退出，但仍有子进程持有输出流（`&`/`nohup` 的形状）；" +
                     "已返回截至此刻的输出。若这是长驻服务，请改用 is_background=true。",
    }, nil
case ctx.Err() != nil:
    // 超时与取消要如实分开说：模型的下一步动作不同
    reason := "被取消"
    if errors.Is(ctx.Err(), context.DeadlineExceeded) {
        reason = "超时"
    }
    return map[string]any{"success": false, "output": out.String(),
        "error": fmt.Sprintf("命令%s，已终止；output 是截至终止时刻的部分输出", reason)}, nil
default:
    // 现状不变（非零退出码）
}
```

这一层是 §11 坑 43 的直接应用：**被我们截断/改写的那一次，必须有人看得见**。
`note` 是同一步的信号（`Trunc: TruncFirst` 意味着这条回执是第一优先被 Rule 压缩截掉的），
它的用途就是纠正模型的**下一次**调用，所以不进持久层是刻意的。

### `CappedBuffer` 放在哪

`internal/browserctl/spawn.go:88-109` 已经有一份同形实现（`cappedBuffer`：到顶后**继续接受写入**，
这样管道不会被写满、子进程不会反过来阻塞；上限常数是 `spawnHardCap`，用在 `:58-59`）。
两份同形实现按 §9.2 的纪律必须各自有输入进聚合测试，所以**上移到 `internal/util/cappedbuffer.go`**
（导出 `CappedBuffer{Max}` + `String()` + `Over()`；今天只有 `String()` 和一个私有 `over` 字段，
`Over()` 是新增的读方法），browserctl 改成用它、`spawnHardCap` 留在原处不动——
util 是叶子包，tools 与 browserctl 都能 import。

> 若你想让 P0 只碰一个文件：退路是在 tools 里复制一份并各自加用例，代价是从此有两份要一起改。

### P0 之后各形状的行为

| 形状 | 今天 | P0 之后 |
|------|------|---------|
| 前台，直接子进程退出、孙进程握管道（`nohup … &`） | 永久卡死，超时/取消全失效 | 2s 返回，输出与真 exit code 照给，服务留着，回执点名 |
| 前台，子进程不退出（`npm run dev` 忘了 `is_background`） | 180s 后只杀 bash，孙进程残留 | 180s 后杀 bash，回执说"超时"而不是"signal: killed"；**孙进程仍残留**（→ P1） |
| 前台，正常命令 | ok | ok，零变化 |
| 用户取消（TUI Esc → 任务 ctx cancel） | 不生效，永久卡住 | ≤ WaitDelay(2s) 返回"被取消"；只杀得到直接子进程 |
| 输出无界增长 | 全进内存 | 10MB 封顶且继续排空，`truncated:true` |

表里"只杀得到直接子进程 / 孙进程仍残留"这两处由 §4 的 P1-a 收掉。

## 4. P1 — 杀组 + 接管信号（已定案：P1-a）

**决定**：`Setpgid` + `Cancel(kill -pgid)` + 新增 SIGINT/SIGTERM 处理器取消任务 ctx。
理由是四条路（工具超时、用户取消、终端 Ctrl+C、外部 `kill -2|-15`）都收干净，
并且顺带修掉"信号打死 yagent 时 rollout 收尾 / `FlushAll` / 蓝图 CAS 全都不跑"。

### 4.0 先决事实（全部实测）

- **正常返回后再 cancel ctx，不会触发 `cmd.Cancel`**（exec 的看门狗在 `Wait` 返回后就停了）。
  实测：`返回: err=exec.ErrWaitDelay out="PID=82994" exit=0 daemonAlive=true` →
  `cancel()` → 400ms 后 `Cancel 未被触发 → daemonAlive=true`。
  这条是 P1-a 能成立的前提：杀组只发生在调用**在飞**时的超时/取消，
  刚放行的 daemon（`executor.go:154-155` 紧接着就 cancel 那个 toolCtx）不会被误杀。
- **任务 ctx 只有两个来源**（接线点四个，见 4.2）：`--prompt` 是 `main.go:324`，
  TUI 与 HTTP 共用 `internal/http/task_gate.go:28` 的 `NewTaskContext`（6 个调用点）。
  两处都从同一个进程级 root ctx 派生，暂停闸门与任务并发域（`task_gate.go:36-39`）自动跟着走。
- **TUI 模式下 Ctrl+C 是按键不是信号**（bubbletea raw mode，`tui_update_keys.go:45-48` → `tea.Quit`）。
  所以信号处理器管的是 `--prompt` / `http` / 外部 `kill`；TUI 的退出路径是同一个洞的另一半，见 4.4。

### 4.1 新包 `internal/shutdown`（叶子，只准标准库）

```go
// Install 注册 SIGINT/SIGTERM：第一次信号取消 Root()，第二次直接硬退出。
// 没调过 Install（测试、库用法）时 Root() == context.Background()，行为与今天逐字一致。
func Install(onHardExit func()) context.Context
func Root() context.Context
```

- 第一次信号 → cancel root（`signal.NotifyContext` 语义）→ 所有任务 ctx 取消 →
  执行循环在 step 边界停下 → **现有**收尾序列照跑（`dataManager.FlushAll`、`yagent.Close()`、
  `supervise.Root().Drain(3s)`、rollout `Close`、蓝图 CAS）。不新造收尾逻辑。
  `stop_reason=cancelled` 这个词表值今天只有 TUI Esc 能走到，P1-a 让它成为信号的落点。
- 第二次信号 → `onHardExit()`（= `tools.KillLiveProcessGroups()`，见 4.3）→ `os.Exit(code)`。
  必须有这条：P0 之后最坏的一个在飞工具仍可能吃掉 180s + WaitDelay，用户不会等。
  `os.Exit` 跳过所有 defer，所以硬退出路径要自己负责杀子进程组。
- 退出码：SIGINT → 130，SIGTERM → 143（今天没有约定；`--prompt` 失败是 1，配置问题是 2）。
- **可测性**：`signal.NotifyContext` 不能在用例里发真信号（会打到整个测试进程）。
  按仓库既有做法抽 seam——把 notify 那一步做成包级 var（同 `agents.defaultToolTimeout`、
  `datamanager.taskFlushDebounce`），用例直接往 channel 里塞一个 `os.Interrupt`。

### 4.2 接线（四个点 + 顺序契约）

生产上任务 ctx 一共两个来源，实测（`grep NewTaskContext|CancelFunc`）：

- `internal/http/task_gate.go:32,34`：`context.Background()` → `shutdown.Root()`。
  **改这一处就覆盖了 6 个调用点**：`tui_tasks.go:80`、`tui_resume.go:713`、`websocket.go:139`、
  `task_manager.go:40`、`server.go:185,226`。`task_gate.go:25-27` 那句"闸门与并发域都只从这里返回
  ctx 这一个通道出去"因此继续成立——暂停闸门与任务域自动在 root 之下。
- `main.go:324`（`runPrompt`）：`ctx := context.Background()` → `ctx := shutdown.Root()`。
  这是 `--prompt` 模式唯一的任务 ctx（`NewTaskRequest(ctx, taskID)`，`main.go:366`）。
- `internal/tui/tui_update_tasks.go:1059`：取消之后重建的占位 ctx 也是
  `context.WithCancel(context.Background())`（它自己的注释就写着"它挂在 context.Background() 下"）。
  今天它只挂在一个 `Status="finished"` 的任务上、不跑东西，但**任何将来的复用都会绕过 root**。
  顺手改成 `http.NewTaskContext(0)`，连暂停闸门一起补齐。
- `main.go` 三个入口在 `logging.Init` 之后调 `shutdown.Install(tools.KillLiveProcessGroups)`。
  **顺序是契约**：Install 必须在第一个任务 ctx 派生之前，否则那个任务不在 root 之下——
  漏了不报错，只是"信号来了它不停"。

### 4.3 `run_bash` 两个分支都 `Setpgid`

前台分支加 §3 那套 + `Cancel: kill(-pgid, SIGKILL)`；后台分支（`system_operations.go:38`）
也加 `Setpgid`，让 daemon 真的独立于终端——现有注释已经声明"后台命令的寿命比这次委派长"，
但今天它和 yagent 同组，终端信号会带走它，那句话没有机制保证。

活着的进程组登记在 tools 侧（`liveGroups`：Start 时登记 pgid、调用返回时注销），
**唯一消费者是 4.1 的硬退出路径**。不要把它塞进 `internal/supervise`：
那是并发原语叶子包，装 OS 进程策略是错的层（`check_supervise_leaf.sh` 也不会拦你，所以要靠这条判据）。

### 4.4 TUI 的退出路径（同一个洞的另一半）

今天 ctrl+c / `:quit` 在任务还在跑时直接 `tea.Quit`，**不取消任务 ctx**：`StartTUI` 返回 →
`FlushAll` → `Close()` → `Drain(3s)` 报"退出时仍有 goroutine 在跑"
（2026-10-25 的日志里就是那四条 lease），子进程也没人杀。
修法：quit 之前先调当前任务的 cancel——TUI 已经有这个能力，
`tui_update_tasks.go:1050-1066` 的取消路径就在用它——再走退出。

### 4.5 风险与代价（如实记）

- `Setpgid` 之后子进程收不到终端信号：Ctrl+\ / Ctrl+C 打不到它，只能经我们的 ctx 取消路径。
  这是刻意的（否则"谁拥有子进程"有两个答案），但必须写进 `internal/tools/AGENTS.md`。
- HTTP 模式：root 取消会取消**所有**任务；gin 的监听端口不会优雅关闭
  （`server.go:502-506` 是 `router.Run`，没有 `http.Server`）。P1 只做任务取消，端口停机进 P2。
- `--prompt` 模式下 TUIConsumer 正在写 stdout，取消后的收尾输出会与它交错——可接受，不额外加锁。
- 第二次信号硬退出会跳过 `defer yagent.Close()`：MCP 子进程（codeseek）由
  `mcpMgr.Close()` 收，硬退出时它会留下孤儿。今天 `kill -9` 就是这个结果，不是回归；
  要收就把 codeseek 的 pgid 也登记进 4.3 那张表——**本次不做**，记账在 P2。

## 5. P2 — 模型侧与同族站点

1. **`is_background` 的描述**（`internal/toolspec/schemas.json`）：现在是
   "Run the command in the background."，太薄。改成明确点名 `&` / `nohup` / 长驻服务必须置 true，
   并说明前台调用在超时时会被整组终止。改描述不改条目数（仍 50 条），
   `tool_surfaces.golden` 里没有 description（实测 `grep -c description` = 0），**不触发 golden 重写**；
   代价是 provider 侧前缀缓存失效一次（§5.1）。
2. **`internal/browserctl/spawn.go:53`** 同族：`cmd.Stdout = writer` 同样是"管道 + 拷贝 goroutine"，
   同样没有 `WaitDelay`、没有 `Setpgid`。它的子进程是会退出的 CLI，风险低，但形状一样。
   P0 把 `CappedBuffer` 上移之后，这里补 `WaitDelay` 是 1 行。**做完 P0 再决定，不要一次改两处。**
3. **`internal/tui/tui_fzf.go:83,111`**：`find` / `fzf` 完全没有 ctx 也没有超时。死挂载点上
   `find` 卡住 = 文件选择器永久无响应（TUI 本身还活着）。独立小 bug，单开一条。
4. **授权等待吃掉了工具预算**：`run_bash` 是 `tier=hot auth`（不是 `blocks`），所以它套 180s 工具超时；
   而 `Adapter.Call` 里 `guard.RequestAuth` 的阻塞（`internal/tools/user_confirm.go:76-82`）
   用的是同一个 ctx。用户在授权弹窗前想超过 180s，这次调用就被"取消"了。
   授权等待有自己的 select-on-ctx，所以不会永久卡死，但口径是错的。修法要么给授权单独预算，
   要么把"等授权"这段时间从工具超时里扣掉——两者都不小，**本次不做**，只记账。
5. **HTTP 优雅停机**：`internal/http/server.go:502-506` 是 `router.Run`，没有 `http.Server`，
   所以 root ctx 取消只停任务、不关监听端口。改成 `http.Server` + `Shutdown(ctx)` 是独立小改。
6. **codeseek 的进程组**：§4.5 最后一条——硬退出时 MCP 子进程会留孤儿。
   要收就把它的 pgid 也登记进 §4.3 那张表。

## 6. 明确不做

**按命令文本形状自动改派后台**（识别 `&` / `nohup` 就转 `is_background` 分支）。
实测数据说明它没必要：P0 之后那个形状**已经**得到模型想要的结果（PID + 服务存活 + 输出）。
而把执行语义绑到命令字符串的形状上，与 §11 坑 42"按形状识别 + 违规即丢弃"是同族风险
（`echo "a & b"`、heredoc 里的 `&`、`&&` 误判）。回执点名（P0）+ 描述改写（P2-1）
已经覆盖了"让模型下次不犯"，不需要执行层猜。

## 7. 测试与闸门

`internal/tools/system_operations_test.go` **已经存在**（108 行，表驱动 `TestExecuteRunBash` 三条：
foreground success / foreground failure / background start，外加一个 `terminateProcess` 辅助）。
新用例扩展这张表或另起 `system_operations_hang_test.go`，**复用 `terminateProcess`**
（同包只准一个 ANSI 剥离辅助那条纪律的同族：别再写第二份杀进程辅助）。

那三条现有用例就是 P0 的回归基线，必须逐字照旧绿——尤其 `foreground failure` 断言
`output == "boom\n"` 精确相等（capped buffer 之后仍要精确），以及 `background start` 断言
`res["pid"].(int)`（P1 给后台分支加 `Setpgid` 之后，`terminateProcess` 杀直接子进程仍然有效）。

计时纪律：不 `time.Sleep`，用 `runBashWaitDelay` seam + 短 ctx；"有没有返回"用 `select` +
`time.After(有界窗口)` + 显式 `t.Errorf`（`check_test_hygiene.sh` 按名字认断言，自定义等待辅助不算）。
进程命名空间自持：断言用的 PID 一律从命令输出里取（`echo $!`），不用 `pgrep` 计数，
`t.Cleanup` 负责杀掉它们。

| 用例 | 钉住什么 | 变异（必须变红） |
|------|----------|------------------|
| `TestExecuteRunBash_GrandchildHoldingPipeReturns` | `sleep 5 & echo "PID=$!"`：在有界窗口内返回、输出含 `PID=`、`exit_code=0`、`note` 在场 | 去掉 `cmd.WaitDelay` → 永不返回 → 断言红 |
| `TestExecuteRunBash_TimeoutReceiptNamesTheReason` | `sleep 30` + 200ms ctx：回执写"超时"而不是 `signal: killed` | 去掉 ctx 分类分支 → 红 |
| `TestExecuteRunBash_CancelReceiptSaysCancelled` | 父 ctx 主动取消：回执写"被取消" | 把两类合并成一个文案 → 红 |
| `TestExecuteRunBash_OutputCapKeepsDraining` | 产出 > 10MB：结果 ≤ 上限、`truncated:true`、命令仍以 0 退出（证明我们在排空而不是把子进程写死） | `CappedBuffer.Write` 到顶返回 error → 子进程收 SIGPIPE → 退出码非 0 → 红 |
| `TestExecuteRunBash_TimeoutKillsWholeProcessGroup` | `sleep 30 & p=$!; sleep 30 & q=$!; echo "$p $q"; sleep 30` + 300ms ctx：返回后两个孙进程 PID 都 `ESRCH` | 去掉 `Setpgid` / `Cancel` → 孙进程存活 → 红 |
| `TestExecuteRunBash_ReturnedCallSurvivesLaterCancel` | 正常返回（ErrWaitDelay 那条路）**之后**再 cancel ctx，daemon 仍活——把 §4.0 那条实测钉成闸门 | 把杀组挂到返回之后（defer 里）→ 红 |

`internal/util` 侧：`CappedBuffer` 的三条（到顶截断、到顶后仍接受写入、`Over()` 语义）。
注意 browserctl 那份**今天零用例**（`grep cappedBuffer --include=*_test.go` 零命中），
所以这三条是新增覆盖而不是搬运；`internal/browserctl` 那五个测试文件经 `Spawn` 间接用到它，
上移之后必须照旧绿。

`internal/shutdown` 侧（用例一律走 §4.1 的 notify seam，**不许给测试进程发真信号**）：

| 用例 | 钉住什么 | 变异 |
|------|----------|------|
| `TestShutdown_FirstSignalCancelsRoot` | 塞一个 `os.Interrupt` → `Root().Done()` 关闭 | 去掉 NotifyContext → 红 |
| `TestShutdown_SecondSignalRunsHardExitHook` | 第二个信号 → `onHardExit` 被调用（`os.Exit` 本身用 hook 代替，不在用例里退进程） | 只处理第一个信号 → 红 |
| `TestShutdown_UninstalledRootIsBackground` | 没 `Install` 时 `Root()` 不随信号取消——钉住测试/库用法零回归 | 让 `Root()` 恒带信号语义 → 红 |
| `TestNewTaskContextDerivesFromShutdownRoot`（在 `internal/http`） | root 取消 → 任务 ctx 取消，且 `Task.Gate()` 仍从同一个 ctx 读回 | 改回 `context.Background()` → 红 |
| `TestCancelledTaskReplacementStaysUnderRoot`（在 `internal/tui`） | 取消后重建的那个 ctx 也在 root 之下、且带暂停闸门（钉住 §4.2 第三点） | 留着 `WithCancel(context.Background())` → 红 |
| `TestQuitCancelsRunningTask`（在 `internal/tui`） | 任务在跑时 quit → 该任务的 cancel 被调用 | 去掉 §4.4 那一行 → 红 |

`TestExecuteRunBash_ReturnedCallSurvivesLaterCancel` 与
`TestExecuteRunBash_TimeoutKillsWholeProcessGroup` 是一对方向相反的闸门：
只留一条的话，"杀组"和"别误杀"任意一边漂走都是绿的。

## 8. 文档与棘轮账（实测行数，`bash scripts/check_agents_md_size.sh` 当前 OK / 18 份 / 冻结 3 项）

| 文档 | 现在 | 余量 | 要写什么 |
|------|------|------|----------|
| `internal/tools/AGENTS.md` | 106/200 | 94 | **硬约束 16**：前台 `run_bash` 的等待必须有界——`WaitDelay` 是唯一能解开管道 EOF 等待的东西，因为直接子进程退出后 `exec` 的 ctx 看门狗不再触发 `Cancel`。**硬约束 17**：`Setpgid` 之后子进程收不到终端信号，"谁杀它"只有 ctx 取消这一条路；活着的进程组登记表唯一消费者是硬退出。硬约束 14 那句"数值口径住在这两个文件"要加第三个消费者（`maxShellOutputBytes` 复用 `MaxFileSizeForEntireRead`）。闸门节补 §7 的用例名 |
| `internal/AGENTS.md`（导览） | 194/200 | **6** | 新增 `shutdown` 小节（叶子包 / `Install` 必须在第一个任务 ctx 派生之前 / 硬退出跳过 defer 所以要自己杀组 / notify seam）。**4-6 行装不下就先在别处删一行** |
| `internal/tui/AGENTS.md` | 226/226 | **0** | §4.4 那条"quit 之前先取消在跑的任务"。零余量：**写进去之前必须先删一行** |
| `internal/http/AGENTS.md` | 64/200 | 136 | `NewTaskContext` 从 `shutdown.Root()` 派生（暂停闸门与任务域因此自动在 root 之下） |
| 根 `AGENTS.md` | 608/608 | **0** | §11 坑 46 + §6.7 退出路径一行 + §9.2 的守卫清单（若加第十一把）。零余量：**删两行，或把 `check_agents_md_size.sh` 的 `AGENTS.md:608` 显式抬高并在注释里写明理由**（棘轮纪律是"只许减不许增"，抬数字 = 承认一笔债，要你点头）。另外两处字面 "45 条"（`:7` 与 `:542`）同步改 46 |

坑 46 正文一句话：`CombinedOutput` 的等待条件里含管道 EOF，而 EOF 由孙进程决定；ctx 超时救不了
"直接子进程已退出"这一形状，`WaitDelay` 才是那道闸门 → `internal/tools` 硬约束 16。

**要不要第十一把守卫**：`internal/shutdown` 按 `supervise`/`blueprint` 的先例应该是叶子包
（只准标准库），配一条 `check_shutdown_leaf.sh`（`go list -deps` 里不许出现任何 `yagent/` 包）。
加了就要同时改 `scripts/check_all.sh` 与根 §9.2 那张清单（又是零余量文件里的一行）。
**建议加**：这个包一旦被业务依赖污染，"信号 → 取消 root"就会变成一条有循环依赖风险的路径。

`internal/blueprint` 会自动多出 `shutdown` 这个模块（派生态，不需要写任何文档）。

## 9. 验收

1. `bash scripts/check_all.sh`（build + vet + 守卫 + 全量单测；加第十一把守卫后是十一把）；
   改到并发/退出路径，所以 **`RACE=true bash scripts/check_all.sh` 这次必须跑**。
2. **变异验证**：按 §7 的两张表逐条做变异，确认每条真的变红——"声称闸门能失败"必须真做一遍。
   变异用 `/tmp` 下的最小副本，不要在工作树里 `git checkout` 还原未提交的文件。
3. 真终端复跑（只有你能做，我这边无 TTY）：`./build.sh` 后让 DevOps 跑原样的
   `nohup node proxy.mjs --port 8799 … & echo "PID=$!"`，确认 (a) 约 2s 内拿到回执与 PID，
   (b) 会话继续往下走，(c) 服务真的在监听。
4. P1-a 的四条真终端验收（都只能你跑）：
   - `./yagent -p "起一个 sleep 300 的前台命令"`，等它卡在工具上时**在另一个终端** `kill -2 <pid>`：
     任务应当停下、`~/.yagent/data/rollout/…` 里出现 `stop_reason=cancelled` 的终止事件与
     `agent_checkpoint`，退出码 130，`pgrep -f "sleep 300"` 为空。
   - 同上但 `kill -15`：退出码 143，其余相同。
   - 连按两次信号：第二次立即退出（不等 180s），且 `pgrep` 为空（硬退出杀组生效）。
   - TUI 里任务在跑时按 ctrl+c：不再出现"退出时仍有 goroutine 在跑"那条 WARN，
     子进程也没留下（§4.4）。
5. HTTP 模式抽查：`./yagent http --port 0` 起一个任务，`kill -TERM` → 任务停、端口仍不优雅关闭
   （那是 P2-5，不是回归）。

## 10. 落地顺序

分支 `fix/run-bash-hang`，六个 commit，每个都能独立回滚、每步都跑 `check_all.sh`：

1. `util.CappedBuffer` 上移 + browserctl 改用 + 补上它今天缺的三条用例（搬运本身编译期就能证明等价）。
2. **P0**：`system_operations.go` 前台分支（WaitDelay + capped 输出 + 四类回执）+ 4 条用例
   + `internal/tools/AGENTS.md` 硬约束 16。到这里卡死已经消失。
3. `internal/shutdown` 新包 + `check_shutdown_leaf.sh` + 四处接线（`task_gate.go`、`runPrompt`、
   TUI 取消后重建的占位 ctx、三个入口的 `Install`）+ 5 条用例。
4. `run_bash` 两个分支 `Setpgid` + `Cancel(kill -pgid)` + `liveGroups` 登记 + 硬退出杀组
   + 那对方向相反的用例 + 硬约束 17。
5. TUI quit 先取消在跑的任务（§4.4）+ `TestQuitCancelsRunningTask`。
6. 文档/棘轮结算：坑 46、导览 `shutdown` 节、`internal/http` 与 `internal/tui` 各一行、
   两处 "45 条" → 46、以及根文件的删行/抬棘轮决定。

P2 六条各自单独开，不要塞进这个分支。

## 附：探针程序

```go
const shellCmd = `cd /tmp/bashhang && nohup sleep 300 > /tmp/bashhang/out.log 2>&1 & echo "PID=$!"`

func mk(ctx context.Context, s string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", "-c", s)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd
}
```

`Setpgid` / `syscall.Kill` 是 Unix-only，但 release 矩阵只有 darwin×2 + linux
（`.github/workflows/release.yml`，全仓 `windows` 零命中），所以**不需要 build tag**。

## 待你处置

机器上还留着那次事故的孤儿：`node proxy.mjs`（PID 79809，监听 127.0.0.1:8799）与它的父
bash（79808）。要杀我可以杀，但它可能还是你在用的代理，所以没动。
