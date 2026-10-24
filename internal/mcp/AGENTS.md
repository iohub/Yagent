# internal/mcp — 一个 JSON-RPC 客户端 + 一个多 server Manager

> 范围：本目录不可派生的约束。设计与判据见 `docs/skills-and-mcp-design.md`；
> 全局摘要见根 `AGENTS.md` §5.3 与 §11 坑 44/45。

## 边界

`client.go` 是传输层（stdio 上的 JSON-RPC 2.0），`manager.go` 是生命周期层（多 server、发现、重连、状态账）。
本包只认识标准库 + `internal/supervise`：**不 import** `toolspec`/`tools`/`config`/`messaging` ——
它只吐 `DiscoveredTool`/`ServerHealth` 这种纯数据，翻译由 `internal/app` 与 `internal/tools` 做。

## 硬约束

**1. 传输层只增不改。** `sendJSON`（持 `writeMu`）、`doRequest`（deadline 取调用方 ctx 与
`RequestTimeout` 的较小值）、`readLoop`（乱序按 ID 配对）、`IsAlive`（只读两个原子位）不许重写。
`ListTools` 与 `Instructions` 是新增方法，用的仍是同一条 `doRequest`。

**2. `RequestTimeout <= 0 → 30s` 的兜底只在 `NewMCPClient` 一处。**
上游（app / Manager / config 模板）都不许再抄一个 30 —— 两处各写一个就是一件事的两种真相。

**3. hint 用 `*bool`。** `readOnlyHint: false` 与"没声明"在能力判定里走向不同分支（前者明确有副作用，
后者 fail-safe 按写处理），裸 `bool` 分不清。

**4. `Start` 只同步 spawn，绝不等发现。** 分两半是硬约束：`app.Init` 在 `Start` 返回后立刻
`Client("codeseek")` —— 6 个手写桥在装配期捕获那个指针，客户端晚一步登记就是永久 nil
（比慢更糟）；而 initialize 可能几分钟（冷索引），等它就是"终端启动卡在 MCP"。
发现阶段的两条超时各管一件事：`ReadyTimeout`（默认 15 分钟）等 initialize，超时**只留在
starting**、不杀进程；`StartupTimeout` 管 `tools/list`，失败且进程还活着 ⇒ degraded。
`WaitForReady` 上界是调用方的 ctx，装配路径不调它。

**4.1 状态迁移在锁内装配、锁外发通知。** `Call` 绝不持 `m.mu`；`Health()` 返回快照；
重连 goroutine 一律 `supervise.From/Group.Go("mcp/reconnect-<name>")`。

**5. 退避有 seam。** 包级 `reconnectBackoffBase` 与 `ManagerOptions.ReconnectBackoff` 是测试的入口——
重连类测试不许 `time.Sleep` 等真实退避。

**6. list_changed 回调不能在 readLoop 里同步跑**（回调会发 `tools/list`，要等 readLoop 读响应 = 自锁死）；
连接落定前收到的通知由 `listChangedPending` 记住、`markConnected` 事后补一次。
两件事都有测试（`TestManager_ListChangedRefreshesTools`），改一处必须同时想另一处。

**7. 重新发现只发生在两处**：重连成功（`markConnected`）与 `tools/list_changed`。
工具集真的变了才调 `OnToolsChanged`（空 diff 重算一遍 sha256 没有意义）。

**8. `tools/list` 必须跟到 `nextCursor` 为空，20 页封顶就报错**（`client.go:266-268,287,308`）。
静默截断与"这个 server 就这些工具"在状态行与 `/mcp` 面板上同形；封顶存在是因为服务端游标回环
会永远转圈，报错才留下一条账。第二个返回值是 initialize 缓存的服务端说明（`client.go:275`）。

**9. `instructions` 用独立 `atomic.Value`，绝不并进 `IsAlive` 的读数**（`client.go:63-65`）。
那两个原子位是任意线程的查询点（第 1 条），把第三条状态塞进去等于给查询加副作用。
它经 `ListTools` 返回值进 `ServerHealth.Instructions`（`manager.go:96,430`）；线上字段
`protocol.go:64-66` —— codeseek 真的会发，以前整个丢掉。

**10. `DiscoveredTool` 是线上形状：server 归属由 `ServerTool` 包一层**（`protocol.go:122-128`、
`manager.go:99-103`）。往里加 yagent 侧字段就是让解码结果撒谎，而工具计数与 `Curated` 记账都从它算。

**11. 状态五档，两处 fail-safe**（`manager.go:56-85,495-509`）。`CountsTools()` 不含 starting——
把还没发生的事报成 "ready — 0 tools" 是让状态行说谎；degraded 与 ready 分开是因为合并后的表达是
"这个 server 没有工具"；`ServerStateFromString` 认不出的串回落 offline（回落 ready 的表现是界面说
"可用"而工具不来），TUI 从事件 payload 还原状态用的就是它（`internal/tui/tui_update_tasks.go:1049`）。

**12. 状态账只有一段文本、三处出口，截断必须写进正文**（`manager.go:440-442,471-473`）。
出口 = 启动日志（`internal/app/app.go:250`）、`/mcp` 面板（`internal/tui/tui_update_commands.go:492`）、
Director 的 system prompt（`internal/agents/external.go:33,42`）；`StatusMaxLines` 超出时末尾补一行
`+N more servers`，`<= 0` 不限。

**13. 两条 Shutdown 路径都必须 `drainDomain`**（`client.go:351,390,358-362`；Manager 侧
`Close`/`ForceClose` 都 `drain()`，`manager.go:555-559`）。`grp` 拥有 readLoop 与后台 initialize 两条：
readLoop 还抓着 stdout 管道时子进程可能因 stdin 未断而不退出，不等就是日志里留下一句
"MCP client shut down"的假话。

**14. 未 initialize 的 `CallTool` 返回 `ErrIndexBuilding` 哨兵 + 整句替代方案**
（`client.go:20-22,220-222`）。调用方按 `errors.Is` 判定并把文案原样回给模型
（`internal/tools/repo_operations.go:148-149`）——改文案就是改六个 repo 工具的回执；
"进程根本不在"必须是另一个错误（`client_process_test.go:577-578`）。这是本包唯一的 codeseek 特例，
其余代码不认识具体 server。

**15. 发现有两个版本：首启异步、重连同步。** `discover`（`manager.go:263-291`）把发现丢后台，
`awaitDiscovery`（`manager.go:812-827`，由 `tryConnect:785-810` 调）就地等 —— 重连循环需要
"这一轮到底成没成"的答复才能决定退避或放弃。转 offline 不轮询：只在看状态时顺带读一次 `IsAlive`
（`manager.go:690-710`），`Call` 失败且进程没了也立刻转 offline（`manager.go:388-391`），
别让下一次调用再撞一次死管道。

**16. server 重名的拒收不在本包。** 同名 map 写入会静默覆盖两份配置，唯一拒收点是
`config.validateMCPServers`（`internal/config/mcp.go:192-194`，`manager.go:186-187` 明写不再校验）；
本包也不读 config 与环境变量，取值由装配层算好传进来（`manager.go:29`）。
`ServerConfig.Curated` 在本包**只数不滤**（`manager.go:50-51,432-436`，摘要里的
`external = Tools - Curated` 也在 `:462`）：真正"手写桥不再以 `mcp__codeseek__*` 注册第二次"
发生在 `internal/tools`（`internal/tools/mcp_specs.go:107-109`），名单本体在
`internal/app/mcp.go:21`。

## 假服务器

假服务器 = **重新执行测试二进制**（`TestMain` 认 `YAGENT_TEST_FAKE_MCP_SERVER`，`client_process_test.go`）。
模式：`plain` / `junk-first` / `reverse` / `slow` / `hang` / `die-on-call` / `tools-list` /
`tools-list-paged` / `tools-list-endless` / `empty-list` / `list-changed` / `slow-list` /
`no-initialize`（永不回 initialize，用来钉"慢启动不该被判死"）。
不要改成 `go run` 或外部脚本：那会把编译时间算进单测，`-race` 下也起不了进程。

## 入口

`client.go`（传输 + `ListTools`）、`protocol.go`（线上类型）、`manager.go`（Manager / ServerHealth /
`RenderStatusLines` / `FormatServerLine`）。

## 闸门

- `TestMCPClient_ListToolsPaginates`（`manager_test.go:390`）/ `TestMCPClient_ListToolsPageCapErrors`
  （`:408`）：分页必须跟到底，上限必须报错。`TestMCPClient_ListToolsRequiresReady`（`:416`）：
  没 initialize 就不许假装拿到空清单。
- `TestMCPClient_ListToolsCarriesAnnotations`（`manager_test.go:424`）：`readOnlyHint` 的
  true / false / 没声明 三形必须可区分（钉第 3 条；*bool 退化成 bool 即红）。
- `TestNewMCPClientDefaults`（`client_process_test.go:621`）：钉第 2 条 —— 兜底只在这一处。
- `TestIsAliveStaysRaceFreeWhileShutdownReapsChild`（`client_process_test.go:498`，`-race`）：
  第 1 条那两个原子位与第 13 条的 join 都在这里。
- `TestCallToolBeforeReadyIsIndexBuilding`（`client_process_test.go:558`）：哨兵包装链断了、
  或"进程不在"也返回 `ErrIndexBuilding`，即红（钉第 14 条）。
- `TestManager_DisabledServerStaysOutOfFaces`（`manager_test.go:103`）/
  `TestManager_CallToOfflineServerExplains`（`:205`）：状态五档的每一面（钉第 11 条）。
- `TestManager_ReconnectsAfterCrash` / `_GivesUpAfterMaxAttempts` / `_DegradedOnEmptyList`：
  三条状态迁移。
- `TestManager_StartDoesNotBlockOnDiscovery` / `_SlowInitializeStaysStarting` /
  `_LiveServerWithFailedListIsDegradedNotRestarted` / `_StartingIsNotReportedAsReady`：
  启动不阻塞、慢初始化不重开、没有清单不谎报 ready。
- `TestManager_CallRoutesToRightServer`：串台会把 A 的结果发给 B。
- `TestManager_StatusLinesIsSingleSource`：`StatusLines()` 与 `RenderStatusLines(Health())` 逐字相同。
- `TestManager_ConcurrentCallsStayFramed`（`-race`）：并发调用不交错。
- 跨包闸门（不在本包，但改了本包的形状就会红）：状态行注入侧的**上限只有一份实现**
  （`internal/agents/injection_test.go:134,148`，钉第 12 条）、
  curated 名单必须指向真的注册过的静态工具（`internal/app/mcp_test.go:11`，钉第 16 条）。