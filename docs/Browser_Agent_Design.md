# Browser-Agent 设计方案：go-rod 浏览器自动化集成

> **状态**: 设计阶段  
> **版本**: v1.0  
> **日期**: 2025-07  
> **依赖**: [go-rod/rod](https://github.com/go-rod/rod)  

---

## 目录

1. [概述](#1-概述)
2. [根因分析](#2-根因分析)
3. [候选架构对比](#3-候选架构对比)
4. [推荐架构设计](#4-推荐架构设计)
5. [分阶段实施路线图](#5-分阶段实施路线图)
6. [文件结构规划](#6-文件结构规划)
7. [配置设计](#7-配置设计)
8. [浏览器工具定义](#8-浏览器工具定义)
9. [安全设计](#9-安全设计)
10. [数据流设计](#10-数据流设计)
11. [关键代码模式](#11-关键代码模式)
12. [TUI 与 HTTP/WebSocket 集成](#12-tui-与-httpwebsocket-集成)
13. [生命周期与韧性](#13-生命周期与韧性)
14. [测试策略](#14-测试策略)
15. [回滚与监控](#15-回滚与监控)

---

## 1. 概述

### 1.1 背景

**CodeActor Agent** 是一个基于 Hub-and-Spoke 多 Agent 架构的 AI 驱动自主编程助手，使用 Go 语言构建。当前系统包含以下 Agent：

| Agent | 职责 |
|-------|------|
| **Conductor** | 中央调度器：任务分类、计划制定、Agent 委派、结果审核 |
| **Repo-Agent** | 代码分析：语义搜索、代码骨架、函数片段 |
| **Coding-Agent** | 代码编写、文件修改、Shell 执行、测试、自调试 |
| **Chat-Agent** | 技术解释、通用问答 |
| **DevOps-Agent** | 系统管理、Shell 命令、日志检查、进程管理 |
| **Meta-Agent** | 运行时创建自定义专用 Agent |

### 1.2 目标

集成 **go-rod** 浏览器自动化库，创建 **Browser-Agent**，使系统具备以下能力：

- 🌐 **网页自动化**：导航、点击、表单填写
- 📸 **视觉捕获**：截图、PDF 生成
- 📊 **数据提取**：文本、HTML、结构化数据抓取
- 🔧 **JS 执行**：页面内脚本执行（需用户确认）
- 🍪 **会话管理**：Cookie 读取/设置
- 📈 **监控检测**：网站健康检查、内容变更监控

---

## 2. 根因分析

| 维度 | 结论 |
|------|------|
| **核心问题** | 当前系统缺乏浏览器交互能力，所有 Agent 仅限于 OS/文件/代码层操作 |
| **影响范围** | 无法执行网页自动化、数据抓取、表单填写、视觉测试、网站监控等任务 |
| **根本原因** | 项目初始范围聚焦于代码分析，未纳入浏览器自动化需求 |
| **关键洞察** | 问题不仅是添加库依赖，而是需要按照 Hub-and-Spoke 模式设计完整的 Agent，同时保持安全性、资源管理和工具化交互的一致性 |

### 系统影响评估

| 组件 | 影响程度 | 说明 |
|------|----------|------|
| `internal/agents/` | **高** | 新增 `BrowserAgent` 结构体、注册、任务执行循环 |
| `internal/tools/` | **高** | 新增 15 个浏览器专用工具 |
| `internal/llm/` | **无** | 复用现有 LLM 抽象层 |
| `pkg/messaging/` | **中** | 新增浏览器任务/结果消息主题 |
| `internal/config/` | **中** | 新增 `[browser]` 配置段 |
| `internal/http/` | **低** | 可能需要新增 workspace 文件服务端点 |
| `internal/tui/` | **低** | 浏览器输出文本/文件路径在聊天 UI 中显示 |
| `main.go` | **中** | Agent 注册、依赖注入、生命周期钩子 |
| `go.mod` | **低** | 添加 `github.com/go-rod/rod` |
| `codebase/` (Rust) | **无** | 不受影响 |

---

## 3. 候选架构对比

### 候选 1：完全嵌入 Agent（紧耦合）

```
Browser-Agent 直接实例化 go-rod
```

| 优点 | 缺点 |
|------|------|
| 实现简单、开销低 | 浏览器实例无法跨任务共享 |
| Agent 完全控制 | 每个任务需冷启动浏览器 |

### 候选 2：浏览器服务 + Agent 前端（解耦）

```
BrowserService（独立服务） ←→ Browser-Agent（命令翻译）
```

| 优点 | 缺点 |
|------|------|
| 关注点分离清晰 | 额外的通信层 |
| 浏览器生命周期可跨 Agent 复用 | 并发控制更复杂 |

### 候选 3：混合模式 — Agent 持有 rod，生命周期由单例提供者管理 ⭐

```
Browser-Agent ←→ BrowserManager（单例）←→ Chrome 进程
                                      ←→ Page 上下文（每任务独立）
```

| 优点 | 缺点 |
|------|------|
| 最优资源使用（单 Chromium 进程） | 需仔细同步控制 |
| Agent 代码简洁 | 浏览器崩溃影响所有待处理任务 |
| 避免冷启动成本 | 需实现健康检查和恢复机制 |
| 通过信号量控制并发 | — |

---

## 4. 推荐架构设计

### 4.1 选择：候选 3 — 混合模式

**理由**：遵循"浏览器进程为单一事实来源"原则，通过 `BrowserManager` 单例管理整个应用生命周期的浏览器实例。与现有 Hub-and-Spoke 模式高度契合。

### 4.2 架构图

```
                        ┌─────────────────┐
                        │   Conductor     │  ← 中央协调器
                        │  (Orchestrator) │
                        └────────┬────────┘
                                 │
                                 │ delegate_browser 工具调用
                                 │ 发布 BrowserTask 消息
                                 ▼
                        ┌─────────────────┐
                        │ Browser-Agent   │  ← LLM 推理 + BrowserToolSet
                        │ (浏览器专家)     │
                        └────────┬────────┘
                                 │
                                 │ 工具执行调用
                                 ▼
                        ┌─────────────────┐
                        │ BrowserManager  │  ← 单例，浏览器生命周期管理
                        │ (浏览器管理器)   │
                        └────────┬────────┘
                                 │
                                 │ go-rod (Chrome DevTools Protocol)
                                 ▼
                        ┌─────────────────┐
                        │ Chrome Browser  │  ← Headless Chrome 进程
                        │ (无头浏览器)     │
                        └────────┬────────┘
                                 │
                                 │ 为每个任务创建独立页面
                                 ▼
                        ┌─────────────────┐
                        │ Page Context    │  ← 每个任务独立的标签页
                        │ (页面上下文)     │
                        └─────────────────┘
```

### 4.3 核心组件职责

| 组件 | 职责 |
|------|------|
| **Conductor** | 分类浏览器任务 → `delegate_browser` 调用 |
| **Browser-Agent** | 接收任务 → LLM 推理 → 工具调用 → 返回结果 |
| **BrowserManager** | 浏览器启动/关闭、页面获取/释放、健康检查、安全策略 |
| **BrowserToolSet** | 15 个浏览器操作工具，供 LLM 调用 |
| **WorkspaceGuard** | 复用现有机制，确保文件写入工作区内 |

---

## 5. 分阶段实施路线图

### Phase 1：基础设施 — 依赖、配置、BrowserManager

| 步骤 | 内容 | 优先级 |
|------|------|--------|
| 1.1 | `go.mod` 添加 `github.com/go-rod/rod` 依赖 | P0 |
| 1.2 | `internal/config/config.go` 扩展 `[browser]` 配置段 | P0 |
| 1.3 | 创建 `internal/browser/manager.go` — 浏览器生命周期管理 | P0 |
| 1.4 | 创建 `internal/browser/security.go` — 安全策略实现 | P0 |
| 1.5 | 创建 `internal/browser/config.go` — Chrome 命令行标志生成 | P1 |

### Phase 2：浏览器工具定义

| 步骤 | 内容 | 优先级 |
|------|------|--------|
| 2.1 | 创建 `internal/tools/browser/` 包目录 | P0 |
| 2.2 | 实现导航类工具：`navigate`, `go_back`, `go_forward`, `reload`, `get_current_url` | P0 |
| 2.3 | 实现交互类工具：`click`, `input`, `scroll`, `wait_element`, `wait` | P0 |
| 2.4 | 实现提取类工具：`extract_text`, `extract_html` | P1 |
| 2.5 | 实现输出类工具：`screenshot`, `pdf` | P1 |
| 2.6 | 实现高级工具：`evaluate_js`, `get_cookies`, `set_cookies` | P2 |
| 2.7 | 创建 `registry.go` — `BrowserTools()` 注册函数 | P0 |

### Phase 3：Browser-Agent 实现

| 步骤 | 内容 | 优先级 |
|------|------|--------|
| 3.1 | 创建 `internal/agents/browser_agent.go` | P0 |
| 3.2 | 实现 LLM 推理循环（接收任务 → 获取页面 → 工具调用 → 返回结果） | P0 |
| 3.3 | 编写浏览器 Agent 系统提示词 | P0 |
| 3.4 | 实现 Agent 工厂函数和注册逻辑 | P1 |

### Phase 4：Conductor 集成

| 步骤 | 内容 | 优先级 |
|------|------|--------|
| 4.1 | 在 `conductor.go` 中新增 `delegate_browser` 工具 | P0 |
| 4.2 | 扩展 Conductor 任务分类规则（识别浏览器类意图） | P1 |
| 4.3 | 通过消息系统实现 Conductor ↔ Browser-Agent 通信 | P0 |

### Phase 5：TUI 与 HTTP/WebSocket 支持

| 步骤 | 内容 | 优先级 |
|------|------|--------|
| 5.1 | 确保 Browser-Agent 消息流入 TUI 显示 | P1 |
| 5.2 | HTTP 服务器添加 workspace 文件服务端点 `GET /workspace/{filepath}` | P1 |
| 5.3 | WebSocket 消息中包含文件路径引用 | P2 |

### Phase 6：生命周期与韧性

| 步骤 | 内容 | 优先级 |
|------|------|--------|
| 6.1 | 实现 `BrowserManager.HealthCheck()` 健康检查 | P1 |
| 6.2 | 浏览器崩溃自动重启机制 | P1 |
| 6.3 | 空闲超时自动关闭浏览器释放资源 | P2 |
| 6.4 | `main.go` 优雅关闭时清理浏览器进程 | P1 |

### Phase 7：测试与验证

| 步骤 | 内容 | 优先级 |
|------|------|--------|
| 7.1 | 浏览器工具单元测试（使用 `httptest` 本地服务器） | P1 |
| 7.2 | Browser-Agent 集成测试（模拟 LLM 输出） | P2 |
| 7.3 | 安全测试（`file://` 拦截、工作区外下载阻止） | P1 |
| 7.4 | 并发压力测试（信号量正确序列化） | P2 |
| 7.5 | 端到端系统测试 | P2 |

---

## 6. 文件结构规划

```
codeactor-agent/
├── main.go                              # 🔄 修改：初始化BrowserManager，注册BrowserAgent
├── go.mod                               # 🔄 修改：+ go-rod 依赖
├── config.example.toml                  # 🔄 修改：更新 [browser] 配置段
│
├── internal/
│   ├── agents/
│   │   ├── browser_agent.go             # 🆕 Browser-Agent 实现
│   │   ├── conductor.go                 # 🔄 修改：delegate_browser 工具
│   │   └── agent_registry.go            # 🔄 修改：BrowserAgent 注册
│   │
│   ├── tools/
│   │   └── browser/                     # 🆕 浏览器工具包
│   │       ├── navigate.go              #   导航到指定 URL
│   │       ├── click.go                 #   元素点击
│   │       ├── input.go                 #   表单输入
│   │       ├── extract.go              #   文本/HTML 提取
│   │       ├── screenshot.go           #   截图
│   │       ├── pdf.go                   #   PDF 生成
│   │       ├── evaluate_js.go          #   JavaScript 执行
│   │       ├── wait_element.go         #   等待元素出现
│   │       ├── cookies.go             #   Cookie 管理
│   │       ├── scroll.go              #   页面滚动
│   │       ├── history.go             #   浏览器历史导航
│   │       └── registry.go            #   BrowserTools() 注册函数
│   │
│   ├── browser/                         # 🆕 浏览器管理包
│   │   ├── manager.go                   #   浏览器生命周期、页面获取/释放
│   │   ├── config.go                    #   配置结构和 Chrome flags 生成
│   │   ├── security.go                  #   域名过滤、file:// 拦截、下载边界
│   │   └── manager_test.go             #   BrowserManager 测试
│   │
│   ├── config/
│   │   └── config.go                    # 🔄 修改：BrowserConfig 结构体
│   │
│   └── http/
│       └── fileserver.go               # 🆕 可选：workspace 文件服务端点
```

> 🆕 = 新增文件 | 🔄 = 修改现有文件

---

## 7. 配置设计

### 7.1 配置结构体

```go
// BrowserConfig 浏览器配置
type BrowserConfig struct {
    Headless           bool     `toml:"headless"`             // 无头模式
    BrowserPath        string   `toml:"browser_path"`         // 浏览器路径（空=自动下载）
    UserDataDir        string   `toml:"user_data_dir"`        // 用户数据目录（空=临时目录）
    ViewportWidth      int      `toml:"viewport_width"`       // 视口宽度
    ViewportHeight     int      `toml:"viewport_height"`      // 视口高度
    AllowedDomains     []string `toml:"allowed_domains"`      // 允许域名列表（空=全部允许）
    BlockedDomains     []string `toml:"blocked_domains"`      // 阻止域名列表
    TimeoutSeconds     int      `toml:"timeout_seconds"`      // 单个操作超时
    MaxConcurrentPages int      `toml:"max_concurrent_pages"` // 最大并发页面数
    AutoLaunch         bool     `toml:"auto_launch"`          // 首次请求时自动启动
    IdleTimeout        string   `toml:"idle_timeout"`         // 空闲超时（如 "5m"）
    AllowNoSandbox     bool     `toml:"allow_no_sandbox"`     // 允许--no-sandbox（Docker中需要）
    ExtraArgs          []string `toml:"extra_args"`           // 额外的Chrome命令行参数
}
```

### 7.2 配置示例

```toml
[browser]
headless = true
browser_path = ""
user_data_dir = ""
viewport_width = 1280
viewport_height = 720
allowed_domains = ["example.com", "api.example.com"]
blocked_domains = ["malware.test"]
timeout_seconds = 30
max_concurrent_pages = 4
auto_launch = true
idle_timeout = "5m"
allow_no_sandbox = false   # Docker 中设为 true
extra_args = []
```

### 7.3 Chrome 启动标志（安全强化）

```go
var secureChromeFlags = []string{
    "--headless=new",             // 新版无头模式
    "--disable-gpu",              // 禁用GPU
    "--no-first-run",             // 跳过首次运行向导
    "--disable-default-apps",     // 禁用默认应用
    "--disable-extensions",       // 禁用扩展
    "--disable-background-networking", // 禁用后台网络
    "--disable-sync",             // 禁用同步
    "--disable-translate",        // 禁用翻译
    "--hide-scrollbars",         // 隐藏滚动条
    "--metrics-recording-only",   // 仅记录指标
    "--mute-audio",              // 静音
    "--disable-dev-shm-usage",   // 使用 /tmp 而非 /dev/shm
    "--no-sandbox",              // 仅容器环境（由 allow_no_sandbox 控制）
}
```

---

## 8. 浏览器工具定义

### 8.1 工具总览

| # | 工具名 | 功能 | 参数 | 返回值 |
|---|--------|------|------|--------|
| 1 | `navigate` | 页面导航 | `url` (string), `timeout_seconds` (int?) | title, url, status |
| 2 | `click` | 元素点击 | `selector` (string), `button` (string?) | 操作结果 |
| 3 | `input` | 表单输入 | `selector` (string), `text` (string) | 操作结果 |
| 4 | `extract_text` | 提取文本 | `selector` (string), `max_chars` (int?) | 文本内容 |
| 5 | `extract_html` | 提取HTML | `selector` (string) | outerHTML（截断） |
| 6 | `screenshot` | 截图 | `selector` (string?), `whole_page` (bool), `output_file` (string?) | 文件路径 |
| 7 | `pdf` | 生成PDF | `output_file` (string?) | 文件路径 |
| 8 | `evaluate_js` | 执行JS | `code` (string) | 执行结果 JSON |
| 9 | `wait_element` | 等待元素 | `selector` (string), `timeout_seconds` (int?) | 是否出现 |
| 10 | `get_cookies` | 获取Cookie | 无 | Cookie列表 |
| 11 | `set_cookies` | 设置Cookie | `cookies` ([]map) | 操作结果 |
| 12 | `scroll` | 页面滚动 | `x` (int), `y` (int) | 操作结果 |
| 13 | `go_back` | 后退 | 无 | 操作结果 |
| 14 | `go_forward` | 前进 | 无 | 操作结果 |
| 15 | `reload` | 刷新 | 无 | 操作结果 |
| 16 | `get_current_url` | 获取当前URL | 无 | URL字符串 |
| 17 | `wait` | 等待毫秒 | `milliseconds` (int) | 操作结果 |

### 8.2 工具安全分类

| 安全等级 | 工具 | 限制 |
|----------|------|------|
| 🟢 安全 | `navigate`, `click`, `input`, `extract_text`, `extract_html`, `screenshot`, `pdf`, `wait_element`, `scroll`, `go_back`, `go_forward`, `reload`, `get_current_url`, `wait`, `get_cookies`, `set_cookies` | URL 验证 + 工作区边界 |
| 🟡 需确认 | `evaluate_js` | 触发 `ask_user_for_help` 用户确认流程 |

---

## 9. 安全设计

### 9.1 多层安全防护

```
┌─────────────────────────────────────────────────────┐
│                   第1层：URL 验证                      │
│  仅允许 http:// 和 https:// scheme                    │
│  拦截 file://、data:、javascript:、chrome:// 等       │
├─────────────────────────────────────────────────────┤
│                   第2层：域名过滤                      │
│  HijackRequests 实现允许/阻止列表                      │
│  支持域名模式匹配（如 *.example.com）                   │
├─────────────────────────────────────────────────────┤
│                   第3层：Chrome 沙箱                   │
│  --headless=new, --disable-extensions, etc.          │
│  --no-sandbox 仅在容器环境下启用                        │
├─────────────────────────────────────────────────────┤
│                   第4层：工作区边界                     │
│  截图/PDF/下载 强制写入 workspace 目录                 │
│  复用现有 WorkspaceGuard 进行路径检查                   │
├─────────────────────────────────────────────────────┤
│                   第5层：用户确认                      │
│  evaluate_js 等高风险操作需用户明确批准                 │
│  通过 ask_user_for_help 机制实现                      │
├─────────────────────────────────────────────────────┤
│                   第6层：资源限制                      │
│  --js-flags="--max-old-space-size=256"              │
│  --renderer-process-limit=4                         │
│  操作超时 30s（可配置）                                │
└─────────────────────────────────────────────────────┘
```

### 9.2 BrowserManager 安全职责

```go
// BrowserManager 安全相关方法
type BrowserManager struct {
    // ...
    securityPolicy *SecurityPolicy
    workspaceGuard *WorkspaceGuard
}

type SecurityPolicy struct {
    AllowedDomains []string   // 允许访问的域名
    BlockedDomains []string   // 禁止访问的域名
    AllowFileAccess bool      // 是否允许 file://（默认 false）
    AllowDataURL    bool      // 是否允许 data: URL（默认 false）
}

// ValidateURL 验证 URL 安全性
func (m *Manager) ValidateURL(rawURL string) error

// SetupPageSecurity 为页面设置安全路由器
func (m *Manager) SetupPageSecurity(page *rod.Page) error

// ValidateFilePath 验证文件路径在工作区内
func (m *Manager) ValidateFilePath(path string) error
```

---

## 10. 数据流设计

### 10.1 完整请求流

```
用户输入: "请截图 https://example.com 首页"
    │
    ▼
┌──────────────────────────────────────────────────────────┐
│ Conductor                                                │
│  1. 任务分类 → 识别为浏览器任务                           │
│  2. 调用 delegate_browser(task="截图 example.com 首页")   │
│  3. 发布 BrowserTask 消息到消息总线                       │
└──────────────┬───────────────────────────────────────────┘
               │
               ▼
┌──────────────────────────────────────────────────────────┐
│ Browser-Agent                                            │
│  1. 从消息总线接收 BrowserTask                           │
│  2. 创建任务上下文（含超时）                               │
│  3. 从 BrowserManager 获取页面                           │
│  4. 开始 LLM 推理循环：                                  │
│     ┌─────────────────────────────────────────┐         │
│     │ LLM: 调用 navigate("https://example.com")│         │
│     │ Tool: 验证URL → 导航 → 等待加载           │         │
│     │ Result: {title:"Example Domain", url:...}│         │
│     │                                          │         │
│     │ LLM: 调用 screenshot(whole_page=true)     │         │
│     │ Tool: 截图 → 保存到 workspace/browser/    │         │
│     │ Result: {path:"browser/screenshots/x.png"}│        │
│     │                                          │         │
│     │ LLM: 调用 finish("截图已完成")            │         │
│     └─────────────────────────────────────────┘         │
│  5. 释放页面                                              │
│  6. 发送 BrowserResult 消息                              │
└──────────────┬───────────────────────────────────────────┘
               │
               ▼
┌──────────────────────────────────────────────────────────┐
│ Conductor                                                │
│  1. 接收 BrowserResult                                  │
│  2. 汇总结果返回给用户                                    │
└──────────────────────────────────────────────────────────┘
```

### 10.2 消息格式

**BrowserTask（Conductor → Browser-Agent）**：
```json
{
  "task_id": "uuid-xxx",
  "prompt": "截图 https://example.com 首页",
  "context": "用户需要整页截图",
  "timeout_seconds": 60
}
```

**BrowserResult（Browser-Agent → Conductor）**：
```json
{
  "task_id": "uuid-xxx",
  "status": "success",
  "summary": "截图已完成",
  "files": [
    {
      "type": "screenshot",
      "path": "browser/screenshots/abc123.png",
      "description": "example.com 首页截图"
    }
  ]
}
```

---

## 11. 关键代码模式

### 11.1 BrowserManager 页面获取

```go
// AcquirePage 获取一个浏览器页面（受信号量控制）
func (m *Manager) AcquirePage(ctx context.Context) (*rod.Page, func(), error) {
    // 信号量控制并发
    select {
    case m.sem <- struct{}{}:
    case <-ctx.Done():
        return nil, nil, ctx.Err()
    }

    m.mu.Lock()
    defer m.mu.Unlock()

    // 懒启动浏览器
    if m.browser == nil {
        if err := m.launch(); err != nil {
            <-m.sem
            return nil, nil, fmt.Errorf("启动浏览器失败: %w", err)
        }
    }

    // 创建新页面
    page, err := m.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
    if err != nil {
        <-m.sem
        return nil, nil, fmt.Errorf("创建页面失败: %w", err)
    }

    // 设置安全策略
    m.setupPageSecurity(page)

    // 释放函数
    release := func() {
        page.Close()
        <-m.sem
        m.lastUsed = time.Now()
    }

    return page, release, nil
}
```

### 11.2 浏览器工具示例（navigate）

```go
// NavigateTool 页面导航工具
type NavigateTool struct {
    workspaceGuard *WorkspaceGuard
}

func (t *NavigateTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
    url, ok := params["url"].(string)
    if !ok {
        return nil, fmt.Errorf("参数 'url' 必须为字符串")
    }

    // URL 安全验证
    uri, err := url.Parse(url)
    if err != nil || (uri.Scheme != "http" && uri.Scheme != "https") {
        return nil, fmt.Errorf("仅允许 http/https URL，收到: %s", url)
    }

    // 获取页面上下文
    page, ok := ctx.Value(PageCtxKey).(*rod.Page)
    if !ok {
        return nil, errors.New("页面上下文不可用")
    }

    // 超时控制
    timeout := 30 * time.Second
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    // 导航
    if err := page.Timeout(timeout).Navigate(url); err != nil {
        return nil, fmt.Errorf("导航失败: %w", err)
    }

    page.WaitLoad()

    info, _ := page.Info()
    return map[string]string{
        "title":  info.Title,
        "url":    info.URL,
        "status": "success",
    }, nil
}
```

### 11.3 Browser-Agent LLM 推理循环

```go
func (ag *BrowserAgent) handleTask(taskMsg BrowserTask) {
    ctx, cancel := context.WithTimeout(context.Background(), ag.timeout)
    defer cancel()

    // 获取页面
    page, release, err := ag.browserMgr.AcquirePage(ctx)
    if err != nil {
        ag.reportError(taskMsg, err)
        return
    }
    defer release()

    // 将页面注入上下文
    ctx = context.WithValue(ctx, PageCtxKey, page)

    // 构建对话
    msgs := []llm.Message{
        {Role: "system", Content: browserSystemPrompt},
        {Role: "user", Content: taskMsg.Prompt},
    }

    // LLM 推理循环
    for {
        response, err := ag.llm.Chat(ctx, msgs, ag.tools)
        if err != nil {
            ag.reportError(taskMsg, err)
            return
        }

        // 处理工具调用
        for _, call := range response.ToolCalls {
            result := ag.executeTool(ctx, call)
            msgs = append(msgs, llm.Message{
                Role:    "tool",
                Content: result,
            })
        }

        if response.FinishReason == "stop" {
            ag.sendResult(taskMsg, response.Content)
            return
        }
    }
}
```

---

## 12. TUI 与 HTTP/WebSocket 集成

### 12.1 TUI 模式

- 浏览器 Agent 的文本输出通过消息系统流入 TUI，与现有 Agent 消息显示方式一致
- 截图/PDF 文件路径以文本形式显示（如 `📸 截图已保存: browser/screenshots/abc.png`）
- 无需额外 TUI 组件修改

### 12.2 HTTP/WebSocket 模式

- **文件服务端点**：`GET /workspace/{filepath}` → 流式返回二进制文件
- **WebSocket 消息**：Browser-Agent 结果消息中包含文件引用
- **前端行为**：收到文件路径后通过 HTTP 端点获取文件内容渲染

```json
// WebSocket 消息示例
{
  "type": "browser_result",
  "task_id": "uuid-xxx",
  "summary": "截图已完成",
  "attachments": [
    {
      "type": "image/png",
      "url": "/workspace/browser/screenshots/abc123.png",
      "description": "example.com 首页截图"
    }
  ]
}
```

### 12.3 工作区文件服务安全

```go
// 文件服务端点必须验证路径在工作区内
func (s *Server) ServeWorkspaceFile(c *gin.Context) {
    filepath := c.Param("filepath")
    fullPath := filepath.Join(s.workspaceDir, filepath)

    // WorkspaceGuard 验证
    if !s.workspaceGuard.IsPathInWorkspace(fullPath) {
        c.Status(http.StatusForbidden)
        return
    }

    c.File(fullPath)
}
```

---

## 13. 生命周期与韧性

### 13.1 状态机

```
    ┌─────────┐    首次 AcquirePage     ┌──────────┐
    │  Idle   │ ──────────────────────→ │  Active  │
    │ (空闲)   │                         │ (活跃)    │
    └────┬────┘                         └─────┬─────┘
         │                                    │
         │  空闲超时                           │ 页面全部释放
         │  (idle_timeout)                    │ + 无等待者
         │                                    │
         ▼                                    ▼
    ┌─────────┐     崩溃/健康检查失败      ┌──────────┐
    │  Closed │ ←────────────────────────  │  Crashed │
    │ (已关闭) │                            │ (已崩溃)  │
    └─────────┘                            └─────┬─────┘
                                                 │
                                                 │ 自动重启
                                                 │
                                                 ▼
                                            ┌──────────┐
                                            │  Active  │
                                            │ (重新活跃) │
                                            └──────────┘
```

### 13.2 健康检查

```go
// HealthCheck 通过 CDP ping 检查浏览器是否存活
func (m *Manager) HealthCheck() error {
    if m.browser == nil {
        return nil // 尚未启动
    }
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    return m.browser.Ping(ctx)
}
```

### 13.3 优雅关闭

```go
// Close 优雅关闭浏览器管理器
func (m *Manager) Close() error {
    m.mu.Lock()
    defer m.mu.Unlock()

    if m.browser != nil {
        if err := m.browser.Close(); err != nil {
            return fmt.Errorf("关闭浏览器失败: %w", err)
        }
        m.browser = nil
    }

    // 清理临时 user-data-dir
    if m.tempDir != "" {
        os.RemoveAll(m.tempDir)
    }

    return nil
}
```

---

## 14. 测试策略

### 14.1 测试层次

```
┌──────────────────────────────────────────┐
│          E2E 系统测试                      │
│   真实用户请求 → Conductor → Browser-Agent │
│         → Chrome → 真实网站               │
├──────────────────────────────────────────┤
│          集成测试                          │
│   Browser-Agent + 模拟 LLM + 真实 Chrome   │
├──────────────────────────────────────────┤
│          单元测试                          │
│   工具级测试（httptest 本地服务器）          │
├──────────────────────────────────────────┤
│          安全测试                          │
│   URL拦截、文件边界、JS确认流程             │
└──────────────────────────────────────────┘
```

### 14.2 测试用例清单

| 类别 | 测试用例 | 预期结果 |
|------|----------|----------|
| **工具测试** | `navigate` 正常URL | 返回页面标题和URL |
| | `navigate` file:// URL | 返回错误 |
| | `click` 有效选择器 | 点击成功 |
| | `click` 无效选择器 | 返回超时错误 |
| | `screenshot` 默认路径 | 文件保存到 workspace |
| | `screenshot` 自定义路径（工作区外） | 返回权限错误 |
| | `extract_text` 大页面 | 按 max_chars 截断 |
| **Agent测试** | 模拟LLM工具调用序列 | 正确执行并返回结果 |
| | 页面获取超时 | 返回超时错误 |
| **安全测试** | `file:///etc/passwd` 导航 | 被URL验证拦截 |
| | `evaluate_js` 未确认 | 等待确认/超时 |
| | 下载到工作区外 | WorkspaceGuard拦截 |
| **并发测试** | 超过 max_concurrent_pages 的请求 | 第5个请求阻塞直到释放 |
| **生命周期测试** | 空闲超时后新请求 | 浏览器重启并正常服务 |

---

## 15. 回滚与监控

### 15.1 Feature Flag

```toml
# 紧急回滚开关
[agents]
enable_browser_agent = true   # 设为 false 可禁用 Browser-Agent
```

- 设为 `false` 时：`BrowserManager` 不初始化，`delegate_browser` 工具返回"不可用"
- 不影响现有 Agent 的任何功能
- `go-rod` 依赖可保留在 `go.mod` 中（不影响编译产物大小）

### 15.2 监控指标

| 指标 | 来源 | 说明 |
|------|------|------|
| 浏览器存活状态 | `GET /health/browser` | `{"status":"ok","pages":2}` |
| 活跃页面数 | BrowserManager | 当前使用的标签页数 |
| 工具调用计数 | 日志 | 每种工具的调用频率 |
| 错误率 | 日志 | 工具执行失败/超时比例 |
| 浏览器崩溃次数 | 日志 | 自动恢复计数 |
| 内存使用 | Chrome进程监控 | 防止内存泄漏 |

### 15.3 日志记录

```go
// 关键事件日志
log.Info("浏览器启动", "flags", flags)
log.Info("页面获取", "available_pages", available)
log.Info("页面释放", "remaining_pages", remaining)
log.Warn("浏览器崩溃", "error", err, "restart_attempt", attempt)
log.Error("导航失败", "url", sanitizedURL, "error", err)
log.Info("浏览器空闲超时，自动关闭")
```

---

## 附录

### A. 依赖项

```
github.com/go-rod/rod          # 浏览器自动化核心库
```

### B. 参考资源

- [go-rod 官方文档](https://go-rod.github.io/)
- [Chrome DevTools Protocol](https://chromedevtools.github.io/devtools-protocol/)
- [CodeActor Agent 架构文档](./ARCHITECTURE.md)

### C. 术语表

| 术语 | 说明 |
|------|------|
| CDP | Chrome DevTools Protocol — Chrome 调试协议 |
| Hub-and-Spoke | 中心辐射型架构 — Conductor 为 Hub，Agent 为 Spoke |
| BrowserManager | 浏览器管理器 — 管理浏览器实例的单例 |
| WorkspaceGuard | 工作区守卫 — 确保文件操作在安全边界内 |
| Semaphore | 信号量 — 控制并发页面数的同步原语 |
| HijackRequests | Rod 的请求拦截功能 — 用于域名过滤和安全策略 |

---

> **文档维护者**: CodeActor Team  
> **最后更新**: 2025-07  
> **状态**: 待审查 → 待实施
