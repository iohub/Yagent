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

**4. 状态迁移在锁内装配、锁外发通知。** `Call` 绝不持 `m.mu`；`Health()` 返回快照；
重连 goroutine 一律 `supervise.From/Group.Go("mcp/reconnect-<name>")`。

**5. 退避有 seam。** 包级 `reconnectBackoffBase` 与 `ManagerOptions.ReconnectBackoff` 是测试的入口——
重连类测试不许 `time.Sleep` 等真实退避。

**6. list_changed 回调不能在 readLoop 里同步跑**（回调会发 `tools/list`，要等 readLoop 读响应 = 自锁死）；
连接落定前收到的通知由 `listChangedPending` 记住、`markConnected` 事后补一次。
两件事都有测试（`TestManager_ListChangedRefreshesTools`），改一处必须同时想另一处。

**7. 重新发现只发生在两处**：重连成功（`markConnected`）与 `tools/list_changed`。
工具集真的变了才调 `OnToolsChanged`（空 diff 重算一遍 sha256 没有意义）。

## 假服务器

假服务器 = **重新执行测试二进制**（`TestMain` 认 `YAGENT_TEST_FAKE_MCP_SERVER`，`client_process_test.go`）。
模式：`plain` / `junk-first` / `reverse` / `slow` / `hang` / `die-on-call` / `tools-list` /
`tools-list-paged` / `tools-list-endless` / `empty-list` / `list-changed` / `slow-list`。
不要改成 `go run` 或外部脚本：那会把编译时间算进单测，`-race` 下也起不了进程。

## 入口

`client.go`（传输 + `ListTools`）、`protocol.go`（线上类型）、`manager.go`（Manager / ServerHealth /
`RenderStatusLines` / `FormatServerLine`）。

## 闸门

- `TestMCPClient_ListToolsPaginates` / `_PageCapErrors`：分页必须跟到底，上限必须报错。
- `TestManager_ReconnectsAfterCrash` / `_GivesUpAfterMaxAttempts` / `_DegradedOnEmptyList`：
  三条状态迁移。
- `TestManager_CallRoutesToRightServer`：串台会把 A 的结果发给 B。
- `TestManager_StatusLinesIsSingleSource`：`StatusLines()` 与 `RenderStatusLines(Health())` 逐字相同。
- `TestManager_ConcurrentCallsStayFramed`（`-race`）：并发调用不交错。