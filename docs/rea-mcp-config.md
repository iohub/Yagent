# 给 yagent 配 REA MCP server（配方 + 探针脚本）

> **读者**：下次要配 REA（或任何第三方 MCP server）的人或 agent。本文是**配方**，不是键表——
> `[[mcp.servers]]` 每个键的语义在 `docs/config-guide.md` §5.5a，包内判据在
> `internal/mcp/AGENTS.md`（硬约束 3/8/11）与 `internal/toolspec/AGENTS.md` 5.1/6.1。
>
> **数字基准**：2026-10-25 在 `/Users/ydl/.yagent/config/config.toml` 上实测，REA 6.7.0，
> 207 条工具。行号会漂，**按符号定位，不要按行号盲改**。文中路径短写时相对 `internal/`
> （如 `mcp_specs.go` = `internal/tools/mcp_specs.go`）。

---

## 0. 一分钟版

```bash
# 1) 存下 §3 的脚本，跑一次探针（它自己找 command/env、自己分类）
python3.12 /tmp/mcp_probe.py rea > /tmp/rea_block.toml && cat /tmp/rea_block.toml
# 2) 把输出整块粘进 ~/.yagent/config/config.toml，位置紧跟 [mcp] 段那几行标量之后
# 3) 把 max_tools_per_server 抬到"这个 server 有多少工具"之上（默认 40，见 §1.3）
# 4) 重启 yagent，认 §4 那两行账
```

现成结果已经在 `~/.yagent/config/config.toml` 里（`name = "rea"`，132 条 `read_only`）。
换机器只需要改 4 条路径，见 §5。

---

## 1. 三条判据：不读懂就一定会配错

### 1.1 服务端没声明 `readOnlyHint=true` 的工具一律是"写类"，而写类默认**一条都不注册**

`externalEffect`（`internal/tools/mcp_specs.go:176-192`）的优先级是硬编码的：

```
1) 名字在 read_only 名单里                      ⇒ EffectExternalRead
2) annotations.readOnlyHint == true            ⇒ EffectExternalRead
3) readOnlyHint == false 或 destructiveHint == true ⇒ EffectExternalWrite
4) 什么都没声明                                  ⇒ EffectExternalWrite（fail-safe）
```

被判成写类的工具会同时拿到 `Mutates + Serial + NeedsAuth`（`mcp_specs.go:206-208`），
并且在 `AllowWrite=false` 时**直接被丢弃**、只留一行 `write-not-allowed` 账
（`mcp_specs.go:119-122`）。这条不是理论风险：

> **实测**：REA 207 条**全部带 annotations**，其中只有 **5 条** `readOnlyHint=true`。
> 所以照默认配置（不写 `allow_write`、不填 `read_only`）挂上 REA，注册数是 **5**，
> 而状态行会若无其事地报 `rea: ready — 207 tools`。`open_binary` 都不在里面。

（顺带：codeseek 13 条**全无** annotations ⇒ 走第 4 条，非 curated 的 7 条被丢弃。这正是
期望行为——`knowledge_*` 那套在 Go 层已删除，`app/mcp.go:54-64` 也刻意不给 codeseek 设 `AllowWrite`。）

### 1.2 `read_only` 名单**优先于** annotations，它买的是两样东西

名单是第 1 步判据，所以能覆盖服务端的 `false`（`mcp_specs.go:177-179`）。分类一变，
后果不止"注册/不注册"：

| 分类 | 并发 | 谁能看见 |
|------|------|----------|
| ExternalRead | 可进并行批次（`toolbatch`，默认 4 路） | Director / Coding / Repo / DevOps（`toolspec/profiles.go:31,53,67,94`） |
| ExternalWrite | 恒串行 | **只有 Coding**（`profiles.go:53` 是唯一 grant `EffectExternalWrite` 的画像） |

所以 `read_only` 不是"少弹一次窗"的糖，它决定 REA 的能力落在哪个 Agent 上。
**别把它当白名单滥用**：`execute_rizin_command` / `cutter_command` / `gdb_console` 是任意命令执行，
`open_binary` / `start_*` / `goto_address` 动的是共享会话状态（游标、session），并发放大
`inspect_*` 那类纯读查询才是要的东西。留在写类的那 75 条 = 会话控制 + 设备控制 + 调试器命令 +
改名/注释/导出 + 全部 `trace_*` + `batch_decompile`。

### 1.3 `max_tools_per_server` 按**字典序**截断，默认 40

`mcp_specs.go:147-161`：先按最终名字排序，保留前 N 条，其余记 `over-max` 账。REA 207 条在
N=40 时留下的是 `address_name … cutter_command`（a–c），后面整个 `inspect_* / procedure_* /
read_* / open_binary` 全没了。**截断有账可查，但界面上和"这个 server 没这些工具"同形**——
所以配完必须去读那行账，别只看状态行的 ready。

配套的两条：工具名上限 64 字符（`toolspec/spec.go:234` 的 `^mcp__[A-Za-z0-9_-]{1,59}$`），
REA 最长名加前缀 48 字符，安全；server 名要过 `^[a-z0-9][a-z0-9_-]{0,31}$` 且不能用保留名
`codeseek`（`config/mcp.go:99-101,194-213`，重名的唯一拒收点在 config 不在 mcp 包）。

---

## 2. 这台机器上的实际取值

```toml
max_tools_per_server = 210        # [mcp] 段，全局共享的帽

[[mcp.servers]]
name = "rea"
command = "/Users/ydl/.nvm/versions/node/v24.22.1/bin/rea"   # nvm 版本目录，见 §5
args = ["mcp"]
allow_write = true
request_timeout = 150             # 执行器 180s 会先动手：agents/executor.go:90
read_only = [ ...132 条... ]      # §3 的脚本生成；与现配置文件逐名相等已核对
[mcp.servers.env]                 # 六个：GHIDRA/HOPPER/JAVA_HOME + REA_ANALYSIS_PROVIDER
                                  #       + REA_IDA_MCP_CONFIG + REA_JADX_MCP_JAR
```

`working_dir` 故意不写 = 继承 yagent 的 CWD（= 项目根）。REA 的临时产物写在它自己的目录下。
TOML 里 `[mcp.servers.env]` 必须放在 `read_only` **之后**——子表头之后的键就归子表了。

---

## 3. 探针脚本（下次直接跑）

它不猜路径：**command 与 env 从 Qoder 已登记的 `~/.qoder-cn/settings.json` 的 `mcpServers.<name>` 里读**，
然后按 §1.1 的判据分类、打印可直接粘贴的 TOML。逻辑与 §2 那份配置同源（实测两边名单逐名相等）。

```python
#!/usr/bin/env python3
"""python3.12 mcp_probe.py rea —— 生成 yagent 的 [[mcp.servers]] 块。"""
import json, os, subprocess, sys

NAME = sys.argv[1] if len(sys.argv) > 1 else "rea"
e = json.load(open(os.path.expanduser("~/.qoder-cn/settings.json")))["mcpServers"][NAME]
cmd, args, env = e["command"], e.get("args", []), e.get("env", {})

# §1.2 的判据：这些前缀/名字的 REA 工具是"对已加载目标做只读分析"。换 server 要重列。
PRE = ("inspect_", "procedure_", "list_", "get_", "read_", "compare_", "find_",
       "decode_", "search_")
EXACT = {"analyze_function", "current_address", "current_document", "current_procedure",
         "next_address", "prev_address", "address_name", "address_to_file_offset",
         "binary_overview", "binary_session", "demangle_swift", "identify_flutter_build",
         "extract_artifact", "correlate_static_and_runtime", "evaluate_reconstruction_coverage",
         "verify_reconstruction", "verify_managed_native_boundaries", "verify_unknown_resolution",
         "build_call_path", "build_reconstruction_obligation_ledger", "resolve_adb_packages",
         "resolve_containing_procedure", "resolve_native_call_targets", "discover_webmcp_tools",
         "frida_session_status", "gdb_session_status", "rizin_debug_session_status", "xrefs"}

p = subprocess.Popen([cmd, *args], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                     stderr=subprocess.DEVNULL, text=True, bufsize=1, env={**os.environ, **env})
def send(o): p.stdin.write(json.dumps(o) + "\n"); p.stdin.flush()
def recv(i):
    while True:
        l = p.stdout.readline()
        if not l: return None
        try: m = json.loads(l)
        except Exception: continue
        if m.get("id") == i: return m

send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
    "protocolVersion": "2024-11-05", "capabilities": {},
    "clientInfo": {"name": "yagent-config-probe", "version": "0"}}})
info = (recv(1) or {}).get("result", {}).get("serverInfo", {})
send({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})
tools, cursor, page = [], None, 0
while page < 20:                                    # 与 client.go 的 maxToolsListPages 同界
    send({"jsonrpc": "2.0", "id": 2 + page, "method": "tools/list",
          "params": {} if cursor is None else {"cursor": cursor}})
    r = recv(2 + page)
    if not r or "result" not in r: break
    res = r["result"]; tools += res.get("tools", [])
    cursor, page = res.get("nextCursor"), page + 1
    if not cursor: break
p.kill()

isro = lambda n: n in EXACT or any(n.startswith(x) for x in PRE)
rd = sorted(t["name"] for t in tools if isro(t["name"]) or (t.get("annotations") or {}).get("readOnlyHint") is True)
wr = sorted(t["name"] for t in tools if t["name"] not in rd)
print(f"# {info} | {len(tools)} tools/{page} page | read {len(rd)} / write {len(wr)} "
      f"| longest model name {max(len(n) for n in rd+wr) + len('mcp__%s__' % NAME)} (cap 64)")
print(f"# write-class: {', '.join(wr[:25])}")
print("\n[[mcp.servers]]")
print(f'name = "{NAME}"\ncommand = "{cmd}"\nargs = {json.dumps(args)}')
print("allow_write = true        # 不写 ⇒ 只有 read_only 那些注册")
print("request_timeout = 150     # 执行器 180s 先动手")
print("read_only = [")
for i in range(0, len(rd), 4):
    print("    " + ", ".join(f'"{n}"' for n in rd[i:i+4]) + ("," if i + 4 < len(rd) else ""))
print("]\n\n[mcp.servers.env]")
for k in sorted(env): print(f'{k} = "{env[k]}"')
```

**这份名单只对你探到的那个 REA 版本负责。** 升级 REA 后重跑脚本 diff 一下 `read_only`；
新增工具若落在 9 个前缀之外，它默认进写类——这是**安全侧**的失效（少能力，不失控）。

---

## 4. 验收：只认那两行账

启动日志两处（`internal/app/app.go:250` 的状态账 + `app/mcp.go:122` 的注册账）。下面这两行是
**按 §4 的算法从实测发现数推出来的预期值**——写本文时没有真跑 yagent，第一次启动后请以实际输出为准：

```
mcp: 2 servers, 220 tools (214 external + 6 curated), 0 offline
mcp tools: 220 discovered, 207 registered, 13 excluded; curated: codeseek_callees, codeseek_callers, codeseek_callgraph, codeseek_search, codeseek_skeleton, codeseek_snippet; write-not-allowed: codeseek_init, codeseek_list, codeseek_status, knowledge_add, knowledge_delete, knowledge_list, knowledge_search
```

（`curated:` 那行只列**真的被服务端发现**的 6 个手写桥服务端名，`ExternalAccount.String`
按理由分组、组内按名字排序，名字一律是**服务端名**不是 `mcp__rea__*`，见 `mcp_specs.go:55-73`。）

**第一行的数字是"发现数"，不是"注册数"**——`RenderStatusLines`（`mcp/manager.go:446-475`）把
每个 ready/degraded server 的 `h.Tools` 直接相加，它不知道 `allow_write`/`read_only` 的筛选。
真正的注册数在第二行的 `registered`，两条差值必须能被 `excluded` 的理由逐条解释清楚；
差值解释不了 = 名单或帽写错了。`/mcp` 面板（`tui/tui_update_commands.go:479,492`）读的是同一份
`Health()` 快照，不会另说一套。

再加一条端到端确认：让 Director 委派一次 Coding 去 `load_tool` 某个 `mcp__rea__inspect_macho`，
回执里应有参数 schema；`YAGENT_MCP=0` 起一次则应该一句都不提 rea。

---

## 5. 换机器 / 换版本要改的就这几处

| 位置 | 为什么 | 怎么取 |
|------|--------|--------|
| `command` | 写死在 nvm 版本目录里，`nvm install` 新 node 就断 | `ls -l $(which rea)`，或跑 §3 的脚本让它自己读 |
| `[mcp.servers.env]` 三条路径 | Ghidra 版本号目录、Hopper 的 `.app` 位置、temurin JDK 路径 | 从 `~/.qoder-cn/settings.json` 抄，别手写 |
| `REA_ANALYSIS_PROVIDER` | 决定走 ghidra 还是 hopper/ida，换提供者等于换一套能力 | 同上 |
| `max_tools_per_server` | REA 涨工具数就会撞上字典序截断（§1.3） | 看探针第一行的 `{N} tools` |

Qoder 那边如果改过 REA 的 env，记得两边都要改——**这两份配置不会互相同步**，yagent 侧只有
`[[mcp.servers]]` 这一份事实。

---

## 6. 成本与裁剪

外部工具一律 `Cold`（`mcp_specs.go:202-210`），所以每步固定成本 = 冷工具目录里的行数
（`tools/surface.go:269-282`，一行 = `- mcp__rea__<name>: ` + 描述首句截 40 rune，
`catalogBrief` / `FirstSentence` 见 `tools/surface.go:571-577`、`toolspec/text.go:11-17`）。
按 REA 6.7.0 的真实描述量出来：

| 画像 | 目录行数 | ≈ token/步 |
|------|---------|-----------|
| Director / Repo / DevOps（只有 read 那 132 条） | 132 | 2.4k |
| Coding（read + write 全 207 条） | 207 | 3.8k |

嫌贵就三个杠杆，按性价比排序：① 从 `read_only` 里删掉这台机器用不上的族（`inspect_adb_*` /
`list_adb_*` / `inspect_packet_*` / `inspect_pcap_*` / `read_adb_*` 对纯静态 RE 无用），Director 侧
立刻省一大截；② 用 `exclude` 整族拉黑，连 Coding 也不给；③ 保持帽不变、只用 `tools` 白名单——
但白名单会静默少注册，务必读 §4 那行账。

反方向也有一条：**冷工具一旦被 `load_tool` 物化，就从一行目录换成完整 description**，
REA 最长的一条 6865 字符（`inspect_analysis_view`）≈ 1.7k token。物化是模型自己决定的，
裁剪名单时把这类"长描述 + 低频"的排掉最划算。

---

## 7. 已知的雷（都在代码里，不在配置能解决的范围内）

- **外部写类工具目前不弹授权。** `WorkspaceGuard.Check`
  （`internal/tools/workspace_guard.go:84-95`）只对 Shell / FSWrite / BrowserEval 分派授权，
  `EffectExternalWrite` 没有分支——`NeedsAuth=true` 声明了（`mcp_specs.go:208`）但没人消费。
  所以 `allow_write = true` 的真实含义是"Coding 可以无人值守调用这 75 条"，
  而 `read_only` 名单**不是**安全边界，它只是并发与可见性开关。要收紧就得先补那个分支。
- **180s 是硬上限。** `defaultToolTimeout`（`agents/executor.go:90`）套在每次工具调用上，
  `request_timeout` 写多大都改变不了。`batch_decompile` 这类长任务要么切小步，要么等
  外部工具的超时豁免出现，别指望调配置能救。
- **状态行注入 Director**（`agents/external.go:12-17` + `[mcp] status_line = true`）：一次配坏
  的 server 会把 `offline — …` 那句话印进每次委派的 prompt。不想要就 `status_line = false`。
- **`startup_timeout`（30s）只管 `tools/list`**，等 `initialize` 的预算是另一个默认 15 分钟的
  `ReadyTimeout`（`mcp/manager.go:26`），且**没有配置键**。REA 秒回，codeseek 冷索引时才需要它。
- **改这份配置不会让 golden 变红**：外部工具不进 `internal/toolspec/schemas.json`，也不进
  `internal/agents/testdata/tool_surfaces.golden`——探针那份 Deps 由
  `toolspec/profile.go:35-69` 的 `testDeps()` 逐字段写死（入口是 `Profile.ProbeDeps()`，
  `toolspec/deps.go:199-202`），里面**根本没有** `ExternalSpecs` 这个键；唯一的填充点在
  `internal/agents/deps.go:28`。所以 `TestAgentSurfacesGolden` 看不见任何 MCP 能力，
  它是运行期事实，验收只走 §4 那两行账。
