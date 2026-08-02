# 在 CodeActor Agent 系统中内嵌 VS Code Server 技术方案

> 版本：v1.0 | 更新日期：2025-07-17

## 目录

1. [项目现状分析](#1-项目现状分析)
2. [方案对比](#2-方案对比)
3. [推荐方案：Sidecar 全功能 IDE](#3-推荐方案sidecar-全功能-ide)
4. [总体架构设计](#4-总体架构设计)
5. [详细组件设计](#5-详细组件设计)
   - 5.1 配置系统扩展
   - 5.2 HTTP Server 修改
   - 5.3 IDE 生命周期管理
   - 5.4 WebSocket 消息扩展
   - 5.5 VS Code 扩展增强
   - 5.6 Agent 系统集成
   - 5.7 文件系统共享约定
6. [分阶段实施计划](#6-分阶段实施计划)
7. [关键决策点](#7-关键决策点)
8. [与现有 VS Code 扩展的兼容性](#8-与现有-vs-code-扩展的兼容性)
9. [失败与恢复策略](#9-失败与恢复策略)
10. [总结](#10-总结)

---

## 1. 项目现状分析

### 1.1 当前架构快照

| 维度 | 现状 |
|------|------|
| **项目语言** | Go 1.25（主项目）+ Rust（代码分析引擎） |
| **HTTP 框架** | Gin + Melody WebSocket |
| **运行模式** | TUI（终端交互）/ HTTP（Web API） |
| **前端** | React WebUI（`webui/build` 已构建但未服务） |
| **端口分配** | HTTP Server（从 9800 自动分配）+ CodeSeek MCP（stdio） |
| **已有 VS Code 集成** | `vscode/` 扩展，通过 WebView 嵌入 WebUI |
| **缺失能力** | ❌ 浏览器中浏览/编辑代码 ❌ 文件树/语法高亮 ❌ IDE ↔ Agent 协作 |

### 1.2 现有路由表

```
GET  /ws               → WebSocket (Melody)
POST /api/start_task   → 启动编程任务
GET  /api/task_status  → 查询任务状态
GET  /api/memory       → 获取对话记忆
DELETE /api/memory     → 清空对话记忆
POST /api/cancel_task  → 取消任务
GET  /api/memory/:type → 按类型获取消息
GET  /api/history      → 历史任务列表
POST /api/load_task    → 加载历史任务
```

### 1.3 技术栈核心依赖

| 依赖 | 用途 |
|------|------|
| `github.com/gin-gonic/gin` | HTTP 服务器框架 |
| `github.com/olahol/melody` | WebSocket 连接管理 |
| `charm.land/bubbletea/v2` | TUI 框架 |
| `react` + `react-scripts` | 前端 WebUI |

### 1.4 现有架构示意图

```
main.go
├── TUI 模式 (codeactor tui)
│   └── Bubbletea 终端交互界面
└── HTTP 模式 (codeactor http)
    └── Gin HTTP Server
        ├── REST API（任务控制、记忆管理）
        └── WebSocket（实时通信）
```

---

## 2. 方案对比

### 2.1 方案 A：Sidecar 全功能 IDE（code-server）

| 维度 | 评估 |
|------|------|
| **核心思路** | 启动 code-server 作为子进程，Gin 反向代理 `/vscode/*`，扩展增强实现 Agent ↔ IDE 协作 |
| **用户体验** | ⭐⭐⭐⭐⭐ 完整 VS Code 体验（终端/Git/调试/扩展市场） |
| **资产复用** | ⭐⭐⭐⭐⭐ 直接复用现有 VS Code 扩展 |
| **实施难度** | ⭐⭐⭐ 中等（反向代理 + 生命周期管理） |
| **资源开销** | ~150~300MB 额外内存 |
| **长期扩展性** | ⭐⭐⭐⭐⭐ 生态丰富，社区活跃 |

### 2.2 方案 B：嵌入式 Monaco Editor

| 维度 | 评估 |
|------|------|
| **核心思路** | 在 WebUI 中嵌入 `@monaco-editor/react`，后端新增文件 REST API |
| **用户体验** | ⭐⭐⭐ 仅编辑器基础功能，无终端/调试/Git |
| **资产复用** | ⭐ 无法复用 VS Code 扩展 |
| **实施难度** | ⭐⭐ 较低（纯 Go+React） |
| **资源开销** | ~30MB 额外内存 |
| **长期扩展性** | ⭐ 需自建所有 IDE 功能，天花板明显 |

### 2.3 综合评估矩阵

| 评估项 | 方案 A（code-server） | 方案 B（Monaco Editor） |
|--------|----------------------|------------------------|
| 功能完整度 | 完整 IDE | 基础编辑器 |
| 复用现有扩展 | ✅ 完美复用 | ❌ 无法复用 |
| 实施工作量 | 中（3~4周） | 中（2~3周） |  
| 长期维护成本 | 低（依赖社区） | 高（需自建） |
| 资源占用 | 较高（+150~300MB） | 较低（+30MB） |
| 安全性风险 | 中（多进程） | 低 |
| 可配置开关 | ✅ 完全可控 | ✅ 完全可控 |

### 2.4 ✅ 推荐方案

**选择方案 A：Sidecar 全功能 IDE（基于 code-server）**

**核心决策理由**：

1. **需求匹配度**：用户明确要求"VS Code Server 提供 Web IDE"，方案 A 直接命中需求
2. **资产复用**：已投入的 VS Code 扩展可直接升级，避免重复建设编辑器 UI
3. **生态优势**：code-server 的插件生态允许未来集成更多开发工具（Linter、Debugger、Git），而无需修改 CodeActor 核心
4. **用户体验**：提供熟悉的 VS Code 界面，降低用户接受门槛，终端/Git/调试开箱即用
5. **可隔离性**：通过 `ide.enabled = false` 配置开关完全关闭，不影响现有 Chat 模式

---

## 3. 推荐方案：Sidecar 全功能 IDE

### 3.1 核心策略

```
Gin HTTP Server (主进程)
    │
    ├── /api/*       → REST API（不变）
    ├── /ws          → WebSocket（改进：支持客户端类型注册）
    ├── /            → WebUI 静态文件（新增）
    └── /vscode/*    → 反向代理（新增）
                            │
                    httputil.ReverseProxy (支持 WebSocket)
                            │
                    code-server (子进程) :9801
                            │
                    ┌───────┴───────┐
                    │  VS Code 扩展 │
                    │  (增强版)     │
                    │  WebSocket → │
                    │  Agent 后端   │
                    └───────────────┘
```

### 3.2 协作协议

```
Agent System                    VS Code Extension
    │                                  │
    │  1. register {client: "editor"}  │
    │◄─────────────────────────────────│
    │                                  │
    │  2. agent_editor_command         │
    │  {action: "open_file", path,     │
    │   line: 42}                      │
    │─────────────────────────────────►│
    │                                  │  vscode.window.showTextDocument(uri)
    │                                  │  editor.revealRange(line 42)
    │                                  │
    │  3. user_edit                    │
    │  {file, changes: [...]}          │
    │◄─────────────────────────────────│  onDidChangeTextDocument
    │                                  │
    │  4. agent_editor_command         │
    │  {action: "insert_text", ...}    │
    │─────────────────────────────────►│  editor.edit(builder => ...)
    │                                  │
```

---

## 4. 总体架构设计

```
┌───────────────────────── Port 9800 (Gin HTTP Server) ──────────────────────────┐
│                                                                                  │
│  ┌──────────────────────────────────────────────────────────────────────────┐   │
│  │  Route Table:                                                            │   │
│  │  /               → WebUI (webui/build 静态文件)    [新增]                  │   │
│  │  /ws             → WebSocket Hub (Melody)   [改进：支持客户端类型路由]     │   │
│  │  /api/*          → REST API (现有)                                        │   │
│  │  /vscode/*       → Reverse Proxy ─────────────────────┐                  │   │
│  └──────────────────────────────────────────────────────────┼────────────────┘   │
│                                                             │                    │
└─────────────────────────────────────────────────────────────┼────────────────────┘
                                                               │
                                              httputil.ReverseProxy (支持 WebSocket)
                                                               │
┌──────────────────── Port 9801 (code-server sidecar) ───────────────────────────┐
│                                                                                  │
│  code-server --bind-addr 127.0.0.1:9801 --auth password                         │
│             --user-data-dir ./.codeactor/user-data                               │
│             --extensions-dir ./.codeactor/extensions                             │
│             --disable-telemetry --disable-update-check                           │
│                                                                                  │
│  ┌──────────── VS Code Extension Host ──────────────────────────────────┐       │
│  │  CodeActor 扩展 (增强版)                                              │       │
│  │  • 建立独立 WebSocket → ws://localhost:9800/ws?client=editor         │       │
│  │  • 监听 agent_editor_command → 执行 VS Code API                      │       │
│  │  • 发送 user_edit 事件 → Agent 感知用户修改                          │       │
│  └──────────────────────────────────────────────────────────────────────┘       │
│                                                                                  │
└──────────────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────────────────┐
│  共享文件系统                                                                    │
│  Agent (CodingAgent) 与 code-server 直接读写同一工作区目录                        │
│  约定：Agent 写文件前先通知 IDE 关闭/保存，写完后通知 IDE 重新打开/刷新           │
└──────────────────────────────────────────────────────────────────────────────────┘
```

---

## 5. 详细组件设计

### 5.1 配置系统扩展

#### `config/config.toml` 新增段

```toml
[ide]
# 是否启用 IDE 功能（默认关闭，不占用额外资源）
enabled = false

# IDE 服务类型：目前仅支持 "code-server"
server_type = "code-server"

# 绑定地址（建议仅 localhost 以保证安全）
bind_addr = "127.0.0.1"

# IDE 服务端口（0 = 自动分配）
port = 0

# 工作区路径（相对于项目根目录或绝对路径）
workspace = "."

# 访问密码（空字符串 = 自动生成随机密码并打印到日志）
password = ""

# 扩展安装目录
extensions_dir = "./.codeactor/extensions"

# 用户数据目录
user_data_dir = "./.codeactor/user-data"

# code-server 可执行文件路径（空 = 从 PATH 查找）
code_server_path = ""

# 是否自动安装 CodeActor VS Code 扩展
auto_install_ext = true
```

#### Go 结构体定义（`internal/config/config.go`）

```go
// IDEConfig contains configuration for the embedded IDE server
type IDEConfig struct {
    Enabled          bool   `toml:"enabled"`
    ServerType       string `toml:"server_type"`
    BindAddr         string `toml:"bind_addr"`
    Port             int    `toml:"port"`
    Workspace        string `toml:"workspace"`
    Password         string `toml:"password"`
    ExtensionsDir    string `toml:"extensions_dir"`
    UserDataDir      string `toml:"user_data_dir"`
    CodeServerPath   string `toml:"code_server_path"`
    AutoInstallExt   bool   `toml:"auto_install_ext"`
}

// 在 Config 结构体中新增字段
type Config struct {
    // ... 现有字段 ...
    IDE              IDEConfig `toml:"ide"`
}
```

### 5.2 HTTP Server 修改（`internal/http/server.go`）

#### 新增反向代理处理器

```go
import (
    "net/http"
    "net/http/httputil"
    "net/url"
    "fmt"
)

// createCodeServerProxy 创建 code-server 的反向代理处理器
func createCodeServerProxy(addr string) gin.HandlerFunc {
    target, _ := url.Parse("http://" + addr)
    proxy := httputil.NewSingleHostReverseProxy(target)

    originalDirector := proxy.Director
    proxy.Director = func(req *http.Request) {
        originalDirector(req)
        req.Header.Set("X-Forwarded-Proto", "http")
        req.Header.Set("X-Forwarded-Host", req.Host)
    }

    proxy.ModifyResponse = func(resp *http.Response) error {
        // 对于 code-server 的某些响应头进行调整（如 X-Frame-Options）
        return nil
    }

    return func(c *gin.Context) {
        proxy.ServeHTTP(c.Writer, c.Request)
    }
}
```

#### 路由注册（`setupRoutes()` 方法中）

```go
func (s *Server) setupRoutes() {
    // ... 现有 CORS 中间件 ...

    // 1) WebSocket 路由（保持不变）
    s.router.GET("/ws", func(c *gin.Context) {
        s.melody.HandleRequest(c.Writer, c.Request)
    })

    // 2) API 路由（保持不变）
    // ... start_task, task_status, memory, cancel_task, history, load_task ...

    // ----- 以下为新增路由 -----

    // 3) WebUI 静态文件服务
    s.router.StaticFS("/", http.Dir("webui/build"))

    // 4) IDE 反向代理（仅在配置启用时注册）
    if s.codeActor.GetClient().Config.IDE.Enabled {
        proxyHandler := createCodeServerProxy(
            fmt.Sprintf("127.0.0.1:%d", s.codeActor.GetClient().Config.IDE.Port))

        // 捕获 /vscode/* 路径并代理到 code-server
        s.router.Any("/vscode/*proxy", proxyHandler)

        // /vscode → /vscode/ 重定向
        s.router.GET("/vscode", func(c *gin.Context) {
            c.Redirect(http.StatusMovedPermanently, "/vscode/")
        })
    }
}
```

#### 静态文件服务注意事项

WebUI 的 `build/index.html` 中包含了浏览器模式检测脚本，当 `window.vscode` 不存在时会自动显示"浏览器预览模式"提示。直接服务静态文件即可正常工作。

### 5.3 IDE 生命周期管理（新文件 `internal/ide/manager.go`）

```go
package ide

import (
    "context"
    "fmt"
    "log/slog"
    "math/rand"
    "os"
    "os/exec"
    "path/filepath"
    "syscall"
    "time"

    "codeactor/internal/config"
)

// Instance 表示一个 IDE 服务器实例
type Instance struct {
    Cmd      *exec.Cmd
    Port     int
    Password string
    Config   *config.IDEConfig
}

// Start 启动 IDE 服务器
func Start(ctx context.Context, cfg *config.IDEConfig, agentPort int) (*Instance, error) {
    if !cfg.Enabled {
        return nil, nil
    }

    // 1. 查找可用端口
    port := cfg.Port
    if port == 0 {
        p, err := findAvailablePort(9801)
        if err != nil {
            return nil, fmt.Errorf("failed to find port for IDE: %w", err)
        }
        port = p
    }

    // 2. 准备密码
    password := cfg.Password
    if password == "" {
        password = generatePassword(16)
    }

    // 3. 确保目录存在
    os.MkdirAll(cfg.ExtensionsDir, 0755)
    os.MkdirAll(cfg.UserDataDir, 0755)

    // 4. 解析工作区路径
    workspace := cfg.Workspace
    if !filepath.IsAbs(workspace) {
        wd, _ := os.Getwd()
        workspace = filepath.Join(wd, workspace)
    }

    // 5. 查找 code-server 可执行文件
    csPath := cfg.CodeServerPath
    if csPath == "" {
        var err error
        csPath, err = exec.LookPath("code-server")
        if err != nil {
            return nil, fmt.Errorf("code-server not found in PATH: %w", err)
        }
    }

    // 6. 构建命令行
    args := []string{
        "--bind-addr", fmt.Sprintf("%s:%d", cfg.BindAddr, port),
        "--auth", "password",
        "--password", password,
        "--user-data-dir", cfg.UserDataDir,
        "--extensions-dir", cfg.ExtensionsDir,
        "--disable-telemetry",
        "--disable-update-check",
        workspace,
    }

    cmd := exec.CommandContext(ctx, csPath, args...)
    cmd.Env = append(os.Environ(),
        fmt.Sprintf("CODEACTOR_AGENT_PORT=%d", agentPort),
    )
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr

    // 7. 启动进程
    slog.Info("Starting code-server IDE",
        "port", port,
        "workspace", workspace,
        "extensions_dir", cfg.ExtensionsDir,
    )
    if err := cmd.Start(); err != nil {
        return nil, fmt.Errorf("failed to start code-server: %w", err)
    }

    inst := &Instance{
        Cmd:      cmd,
        Port:     port,
        Password: password,
        Config:   cfg,
    }

    // 8. 等待就绪
    if err := inst.waitForReady(ctx, 30*time.Second); err != nil {
        inst.Shutdown(ctx)
        return nil, fmt.Errorf("code-server failed to become ready: %w", err)
    }

    slog.Info("code-server IDE is ready",
        "url", fmt.Sprintf("http://localhost:%d", port),
        "password", password,
    )

    // 9. 自动安装扩展
    if cfg.AutoInstallExt {
        if err := inst.installExtension(ctx, agentPort); err != nil {
            slog.Warn("Failed to auto-install CodeActor extension", "error", err)
        }
    }

    return inst, nil
}

// Shutdown 优雅关闭 IDE 服务器
func (inst *Instance) Shutdown(ctx context.Context) error {
    if inst == nil || inst.Cmd == nil || inst.Cmd.Process == nil {
        return nil
    }

    slog.Info("Shutting down code-server IDE", "pid", inst.Cmd.Process.Pid)

    // 发送 SIGTERM
    if err := inst.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
        slog.Warn("Failed to send SIGTERM to code-server", "error", err)
        // 直接 SIGKILL
        return inst.Cmd.Process.Kill()
    }

    // 等待进程退出（最多 10 秒）
    done := make(chan error, 1)
    go func() {
        done <- inst.Cmd.Wait()
    }()

    select {
    case <-time.After(10 * time.Second):
        slog.Warn("code-server did not exit gracefully, killing")
        return inst.Cmd.Process.Kill()
    case err := <-done:
        return err
    }
}

// Health 检查 IDE 服务器是否健康
func (inst *Instance) Health() bool {
    if inst == nil || inst.Cmd == nil || inst.Cmd.Process == nil {
        return false
    }
    // 检查进程是否存活
    return inst.Cmd.Process.Signal(syscall.Signal(0)) == nil
}

// waitForReady 轮询等待 IDE 服务器就绪
func (inst *Instance) waitForReady(ctx context.Context, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        // 尝试连接 healthz 端点
        resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", inst.Port))
        if err == nil {
            resp.Body.Close()
            return nil
        }

        time.Sleep(500 * time.Millisecond)
    }
    return fmt.Errorf("timeout waiting for code-server to become ready")
}

// installExtension 安装 CodeActor VS Code 扩展
func (inst *Instance) installExtension(ctx context.Context, agentPort int) error {
    // 方法1：通过 code-server CLI 安装（如果安装了）
    // code-server --install-extension /path/to/codeactor.vsix

    // 方法2：直接复制到 extensions-dir 并解压
    // 需要确保 extensionsDir 中存在 codeactor 扩展目录

    return nil // 待实施
}

// findAvailablePort 查找可用端口
func findAvailablePort(startPort int) (int, error) {
    for port := startPort; port < startPort+100; port++ {
        addr := fmt.Sprintf("127.0.0.1:%d", port)
        ln, err := net.Listen("tcp", addr)
        if err == nil {
            ln.Close()
            return port, nil
        }
    }
    return 0, fmt.Errorf("no available port found from %d", startPort)
}

// generatePassword 生成随机密码
func generatePassword(length int) string {
    const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
    result := make([]byte, length)
    for i := range result {
        result[i] = chars[rand.Intn(len(chars))]
    }
    return string(result)
}
```

### 5.4 WebSocket 消息扩展

#### 客户端注册协议

在现有 Melody WebSocket 基础上增加客户端类型识别：

```go
// internal/http/websocket.go — 在 HandleConnect 和 HandleMessage 中增强

// WebSocket 连接建立时，初始化客户端类型
s.melody.HandleConnect(func(s *melody.Session) {
    s.Set("client_type", "unknown")
    slog.Info("WebSocket client connected")
})

// 注册消息处理
s.melody.HandleMessage(func(s *melody.Session, msg []byte) {
    var baseMsg struct {
        Type   string          `json:"type"`
        Event  string          `json:"event,omitempty"`
        Client string          `json:"client,omitempty"`
        Data   json.RawMessage `json:"data,omitempty"`
    }
    if err := json.Unmarshal(msg, &baseMsg); err != nil {
        slog.Error("Failed to parse message", "error", err)
        return
    }

    // 处理客户端注册
    if baseMsg.Type == "register" {
        s.Set("client_type", baseMsg.Client)
        s.Set("client_name", baseMsg.Data) // 可选的客户端名称
        slog.Info("Client registered", "type", baseMsg.Client)
        return
    }

    // 处理编辑器命令结果/用户编辑事件
    if baseMsg.Type == "user_edit" {
        handleUserEdit(s, baseMsg.Data)
        return
    }

    // 原有事件处理（start_task, chat_message 等）
    // ...
})
```

#### 编辑器命令广播

```go
// internal/http/server.go — 新增广播方法

// BroadcastEditorCommand 广播编辑器命令给所有 IDE 客户端
func (s *Server) BroadcastEditorCommand(cmd types.EditorCommand) {
    msg := map[string]interface{}{
        "type":    "agent_editor_command",
        "payload": cmd,
    }
    data, _ := json.Marshal(msg)

    s.melody.BroadcastFilter(data, func(s *melody.Session) bool {
        clientType, _ := s.Get("client_type")
        return clientType == "editor"
    })
}
```

#### 编辑器命令协议定义

```go
// internal/protocol/editor_command.go — 新文件

package protocol

// EditorCommand Agent 发给 IDE 的编辑器操作指令
type EditorCommand struct {
    Action   string    `json:"action"`             // open_file, insert_text, replace_range, show_diff, close_file, refresh_file
    Path     string    `json:"path"`                // 文件路径
    Line     int       `json:"line,omitempty"`       // 跳转行号（1-indexed）
    Text     string    `json:"text,omitempty"`       // 插入/替换的文本内容
    Position *Position `json:"position,omitempty"`   // 插入位置
    Range    *TextRange `json:"range,omitempty"`     // 替换范围
}

// Position 编辑器中的位置
type Position struct {
    Line      int `json:"line"`      // 0-indexed
    Character int `json:"character"` // 0-indexed
}

// TextRange 文本范围
type TextRange struct {
    Start Position `json:"start"`
    End   Position `json:"end"`
}

// UserEdit 用户编辑事件（IDE → Agent）
type UserEdit struct {
    File    string       `json:"file"`
    Changes []TextChange `json:"changes"`
}

// TextChange 文本变更
type TextChange struct {
    Range TextRange `json:"range"`
    Text  string    `json:"text"`
}
```

### 5.5 VS Code 扩展增强（`vscode/src/extension.ts`）

以下是扩展激活后的核心逻辑：

```typescript
import * as vscode from 'vscode';

let agentSocket: WebSocket | null = null;

export function activate(context: vscode.ExtensionContext) {
    // ===== 1. 建立与 Agent 后端的独立 WebSocket 连接 =====
    const agentPort = process.env.CODEACTOR_AGENT_PORT || '9800';
    const wsUrl = `ws://127.0.0.1:${agentPort}/ws`;

    connectToAgent(wsUrl);

    // ===== 2. 监听用户编辑事件（可选，用于双向协作） =====
    context.subscriptions.push(
        vscode.workspace.onDidChangeTextDocument(event => {
            if (!agentSocket || agentSocket.readyState !== WebSocket.OPEN) return;
            if (event.contentChanges.length === 0) return;

            // 发送用户编辑事件到 Agent
            agentSocket.send(JSON.stringify({
                type: 'user_edit',
                payload: {
                    file: event.document.fileName,
                    changes: event.contentChanges.map(c => ({
                        range: {
                            start: { line: c.range.start.line, character: c.range.start.character },
                            end: { line: c.range.end.line, character: c.range.end.character },
                        },
                        text: c.text,
                    })),
                },
            }));
        })
    );
}

// ===== WebSocket 连接管理 =====
function connectToAgent(url: string) {
    function connect() {
        if (agentSocket && agentSocket.readyState === WebSocket.OPEN) return;

        const socket = new WebSocket(url);

        socket.onopen = () => {
            console.log('[CodeActor] Connected to agent backend');
            agentSocket = socket;

            // 注册为 editor 客户端
            socket.send(JSON.stringify({
                type: 'register',
                client: 'editor',
                name: 'CodeActor Extension',
            }));
        };

        socket.onmessage = (event) => {
            try {
                const msg = JSON.parse(event.data);
                if (msg.type === 'agent_editor_command') {
                    handleEditorCommand(msg.payload);
                }
            } catch (e) {
                console.error('[CodeActor] Failed to parse message', e);
            }
        };

        socket.onclose = () => {
            console.log('[CodeActor] Disconnected from agent, retrying...');
            agentSocket = null;
            // 指数退避重连
            setTimeout(connect, 5000);
        };

        socket.onerror = (err) => {
            console.error('[CodeActor] WebSocket error', err);
            socket.close();
        };
    }

    connect();
}

// ===== 编辑器命令处理 =====
async function handleEditorCommand(cmd: any) {
    const action = cmd.action as string;

    switch (action) {
        case 'open_file': {
            const uri = vscode.Uri.file(cmd.path);
            const doc = await vscode.workspace.openTextDocument(uri);
            const editor = await vscode.window.showTextDocument(doc, { preview: false });

            // 跳转到指定行
            if (cmd.line !== undefined && cmd.line > 0) {
                const pos = new vscode.Position(cmd.line - 1, 0);
                editor.selection = new vscode.Selection(pos, pos);
                editor.revealRange(new vscode.Range(pos, pos), vscode.TextEditorRevealType.InCenter);
            }
            break;
        }

        case 'insert_text': {
            const editor = vscode.window.activeTextEditor;
            if (!editor || editor.document.fileName !== cmd.path) {
                // 文件未打开，先打开再插入
                await handleEditorCommand({ action: 'open_file', path: cmd.path });
                return handleEditorCommand(cmd);
            }

            await editor.edit(editBuilder => {
                if (cmd.position) {
                    const pos = new vscode.Position(cmd.position.line, cmd.position.character);
                    editBuilder.insert(pos, cmd.text);
                } else {
                    // 追加到文件末尾
                    const doc = editor.document;
                    const lastLine = doc.lineCount - 1;
                    const lastChar = doc.lineAt(lastLine).text.length;
                    editBuilder.insert(new vscode.Position(lastLine, lastChar), '\n' + cmd.text);
                }
            });
            break;
        }

        case 'replace_range': {
            const editor = vscode.window.activeTextEditor;
            if (!editor || editor.document.fileName !== cmd.path) return;

            await editor.edit(editBuilder => {
                if (cmd.range) {
                    const range = new vscode.Range(
                        cmd.range.start.line, cmd.range.start.character,
                        cmd.range.end.line, cmd.range.end.character
                    );
                    editBuilder.replace(range, cmd.text);
                }
            });
            break;
        }

        case 'show_diff': {
            // 使用 VS Code 内置的差异编辑器
            if (cmd.diff) {
                // 创建临时文件或使用 SCM 接口展示差异
                // 简化实现：直接打开原始文件和新内容
            }
            break;
        }

        case 'refresh_file': {
            // 文件已在外部修改（Agent 直接写磁盘），通知 VS Code 重新加载
            const uri = vscode.Uri.file(cmd.path);
            const doc = await vscode.workspace.openTextDocument(uri);
            if (doc.isDirty) {
                // 有未保存的修改，提示用户
                vscode.window.showWarningMessage(
                    `File ${cmd.path} has been modified by Agent but you have unsaved changes.`,
                    'Reload', 'Keep'
                ).then(selection => {
                    if (selection === 'Reload') {
                        // 重新打开会触发 reload
                    }
                });
            }
            break;
        }

        case 'close_file': {
            const uri = vscode.Uri.file(cmd.path);
            const doc = await vscode.workspace.openTextDocument(uri);
            // 关闭文档（如果修改过会提示保存）
            await vscode.commands.executeCommand('workbench.action.closeActiveEditor');
            break;
        }
    }
}

export function deactivate() {
    if (agentSocket) {
        agentSocket.close();
        agentSocket = null;
    }
}
```

### 5.6 Agent 系统集成

#### CodingAgent 新增方法

```go
// internal/agents/coding.go — 新增方法

// OpenFileInIDE 通知 IDE 打开指定文件并跳转到行
func (a *CodingAgent) OpenFileInIDE(path string, line int) {
    if a.GlobalCtx == nil || a.GlobalCtx.Publisher == nil {
        return
    }
    a.GlobalCtx.Publisher.Publish(messaging.Message{
        Type: "agent_editor_command",
        Data: protocol.EditorCommand{
            Action: "open_file",
            Path:   path,
            Line:   line,
        },
    })
}

// InsertTextInIDE 在 IDE 中插入文本
func (a *CodingAgent) InsertTextInIDE(path string, position protocol.Position, text string) {
    a.GlobalCtx.Publisher.Publish(messaging.Message{
        Type: "agent_editor_command",
        Data: protocol.EditorCommand{
            Action:   "insert_text",
            Path:     path,
            Position: &position,
            Text:     text,
        },
    })
}

// ShowDiffInIDE 在 IDE 中展示差异对比
func (a *CodingAgent) ShowDiffInIDE(originalPath, newContent string) {
    // 创建临时文件或直接展示原始 vs 新内容
}
```

#### 消息路由集成

在 HTTP Server 的消息分发中，增加对 `agent_editor_command` 类型的处理：

```go
// internal/http/server.go — 在消息分发中注册编辑器命令广播

// 在 Run 方法中或通过 MessageDispatcher 订阅
func (s *Server) startEditorCommandListener() {
    // 通过 MessagePublisher 订阅 agent_editor_command 消息
    // 当收到此类消息时，广播给所有 editor 客户端
    go func() {
        // 简化的监听循环（实际应使用 dispatcher 的订阅机制）
        for msg := range s.codeActor.GetPublisher().Subscribe("agent_editor_command") {
            s.BroadcastEditorCommand(msg.Data.(protocol.EditorCommand))
        }
    }()
}
```

### 5.7 文件系统共享约定

| 场景 | 操作流程 | 说明 |
|------|---------|------|
| **Agent 创建新文件** | Agent 直接写磁盘 → 通过 `open_file` 命令通知 IDE 打开 | 适用于代码生成结果 |
| **Agent 修改已打开文件** | Agent 发送 `close_file` → 等待确认 → 写磁盘 → 发送 `open_file` + `show_diff` | 避免 VS Code 内部状态不一致 |
| **Agent 批量修改** | Agent 完成所有写入 → 逐个发送 `refresh_file` 通知 IDE 重新加载 | 适用于大规模重构 |
| **用户在 IDE 中修改** | 扩展通过 WebSocket 发送 `user_edit` → Agent 根据需要重新读取文件 | Agent 可基于用户修改调整后续操作 |
| **文件锁冲突** | 先发送 `close_file` 确保 IDE 释放文件 → Agent 修改 → 重新打开 | 避免并发写入冲突 |

#### 共享工作区路径解析

```go
func resolveWorkspacePath(workspace, filePath string) string {
    if filepath.IsAbs(filePath) {
        return filePath
    }
    base := workspace
    if !filepath.IsAbs(base) {
        wd, _ := os.Getwd()
        base = filepath.Join(wd, base)
    }
    return filepath.Join(base, filePath)
}
```

---

## 6. 分阶段实施计划

### 6.1 Phase 1：基础集成（1~2 周）

| 编号 | 任务 | 涉及文件 | 说明 |
|------|------|---------|------|
| 1.1 | 配置系统扩展 | `internal/config/config.go` | 新增 `IDEConfig` 结构体 + TOML 解析 |
| 1.2 | IDE 管理器 | `internal/ide/manager.go`（新文件） | `Start()`, `Shutdown()`, `Health()` 实现 |
| 1.3 | 反向代理 | `internal/http/server.go` | `createCodeServerProxy()` + `/vscode/*` 路由 |
| 1.4 | 静态文件服务 | `internal/http/server.go` | `r.StaticFS("/", ...)` 服务 WebUI |
| 1.5 | 启动集成 | `main.go` | IDE 启动/关闭挂接到 HTTP 模式生命周期 |
| 1.6 | 构建配置 | `build.sh` | code-server 依赖检查或嵌入 |

**验证标准**：
- `codeactor http` 启动后，日志打印 IDE 密码和端口
- 访问 `http://localhost:9800/` 显示 WebUI
- 访问 `http://localhost:9800/vscode/` 显示 code-server 登录页，输入密码进入工作区

### 6.2 Phase 2：单向命令（1 周）

| 编号 | 任务 | 涉及文件 | 说明 |
|------|------|---------|------|
| 2.1 | WebSocket 客户端注册 | `internal/http/websocket.go` | 解析 `register` 消息，按类型路由 |
| 2.2 | 编辑器命令协议 | `internal/protocol/editor_command.go`（新文件） | 定义命令结构体 |
| 2.3 | 编辑器命令广播 | `internal/http/server.go` | `BroadcastEditorCommand()` 方法 |
| 2.4 | 扩展增强：WebSocket | `vscode/src/extension.ts` | 独立 WebSocket 连接 + 注册 |
| 2.5 | 扩展增强：命令处理 | `vscode/src/extension.ts` | `open_file`, `insert_text` 实现 |
| 2.6 | Agent IDE 方法 | `internal/agents/coding.go` | `OpenFileInIDE()`, `InsertTextInIDE()` |
| 2.7 | 扩展自动安装 | `internal/ide/manager.go` | `installExtension()` 实现 |

**验证标准**：
- 扩展激活后建立 WebSocket 连接，日志显示 `[CodeActor] Connected`
- Agent 调用 `OpenFileInIDE()` 后，IDE 自动打开文件并跳转到指定行
- Agent 调用 `InsertTextInIDE()` 后，IDE 中文件内容实时更新

### 6.3 Phase 3：双向协作（1 周）

| 编号 | 任务 | 涉及文件 | 说明 |
|------|------|---------|------|
| 3.1 | 用户编辑事件 | `vscode/src/extension.ts` | `onDidChangeTextDocument` → WebSocket |
| 3.2 | 用户编辑处理 | `internal/http/websocket.go` | `handleUserEdit()` 解析和转发 |
| 3.3 | 差异展示 | `vscode/src/extension.ts` | `show_diff` 命令实现 |
| 3.4 | WebUI 入口 | `webui/src/components/*` | "打开 IDE" 按钮 + 状态指示 |
| 3.5 | IDE 状态 API | `internal/http/server.go` | `GET /api/ide/status` 返回 IDE 连接状态 |

**验证标准**：
- 用户在 IDE 中修改文件，Agent 能收到 `user_edit` 事件
- Agent 根据用户修改自适应调整后续操作
- WebUI 显示 IDE 连接状态

### 6.4 Phase 4：生产加固（1 周）

| 编号 | 任务 | 说明 |
|------|------|------|
| 4.1 | 路径穿越防护 | IDE 工作区路径校验，拒绝访问工作区外的文件 |
| 4.2 | 密码管理 | 自动生成强密码、日志脱敏、临时 API 凭证传递 |
| 4.3 | 崩溃恢复 | IDE 进程监控 + 自动重启（最多 3 次） |
| 4.4 | 优雅关闭 | SIGTERM → 等待进程退出 → SIGKILL 兜底 |
| 4.5 | 构建集成 | `build.sh` 增加扩展打包 `vsce package` |
| 4.6 | 文档 | 配置说明、快速开始指南、架构图更新 |
| 4.7 | 集成测试 | 编写端到端测试用例 |

**验证标准**：
- 模拟 code-server 崩溃后自动重启，WebSocket 自动重连
- 路径穿越攻击被拦截
- 无密码在日志中明文泄露

---

## 7. 关键决策点

### 7.1 code-server 发行版选择

| 特性 | coder/code-server | gitpod-io/openvscode-server |
|------|-------------------|----------------------------|
| **协议** | MIT | MIT |
| **社区活跃度** | ⭐⭐⭐⭐⭐ (47k+ Stars) | ⭐⭐⭐ (6k+ Stars) |
| **arm64 支持** | ✅ 官方二进制 | ✅ 官方二进制 |
| **扩展市场** | ✅ 支持 | ✅ 支持 |
| **WebSocket 支持** | ✅ 原生 | ✅ 原生 |
| **配置方式** | CLI flags | CLI flags |

**推荐**：默认使用 **coder/code-server**（社区更大、文档更全）。通过 `server_type` 配置支持切换。

### 7.2 密码与认证策略

| 方式 | 适用场景 | 实现 |
|------|---------|------|
| 自动生成 + 日志输出 | 开发/本机使用 | `generatePassword(16)` → `slog.Info("IDE password", ...)` |
| 自动生成 + 临时 API | WebUI 自动登录 | `GET /api/ide/credentials`（仅 localhost） |
| 用户指定 | 生产/远程使用 | `password = "your-strong-password"` |
| 无密码（仅 localhost） | 最高便利性 | `--auth none` 配置项 |

### 7.3 端口发现机制

```
主进程启动时：
1. 分配 IDE 端口（从 9801 开始查找可用端口）
2. 设置环境变量 CODEACTOR_AGENT_PORT=9800
3. 启动 code-server 进程
4. code-server 内的扩展通过 process.env.CODEACTOR_AGENT_PORT 获知后端地址
```

### 7.4 多客户端支持策略

当前仅支持单用户本地使用，不涉及多客户端冲突解决。
如需支持多用户，可采用：

| 策略 | 说明 |
|------|------|
| 最后写入胜出 (LWW) | 简单但可能丢失修改 |
| 操作转换 (OT) | Google Docs 风格，复杂但精确 |
| CRDT | 去中心化实时协作，适合未来扩展 |

---

## 8. 与现有 VS Code 扩展的兼容性

### 8.1 兼容性矩阵

| 现有功能 | 在 code-server 中的兼容性 | 说明 |
|---------|------------------------|------|
| WebView 嵌入 WebUI | ✅ 完全兼容 | code-server 完整支持 WebView API |
| CodeLens 集成 | ✅ 完全兼容 | 基于 Language Server，不受影响 |
| Tree-sitter 分析 | ✅ 完全兼容 | 纯 WASM 实现，无平台依赖 |
| Agent 事件协议 | ✅ 完全兼容 | JSON Schema 统一，无运行时差异 |
| 启动后端进程 | ⚠️ 逻辑变更 | 在 code-server 中不再需要启动后端，改为连接已有后端 |

### 8.2 扩展行为变更

```
桌面 VS Code:
  扩展激活 → 启动 codeactor 后端进程 → 建立 WebSocket → 嵌入 WebView

code-server:
  扩展激活 → 连接已有后端 (通过 env 发现端口) → 建立 WebSocket → 注册为 editor 客户端
```

### 8.3 扩展打包

在 `build.sh` 中增加：

```bash
# 构建 VS Code 扩展 .vsix 包
if command -v vsce &> /dev/null; then
    cd vscode
    vsce package --out ../dist/codeactor.vsix
    cd ..
fi
```

生成的 `dist/codeactor.vsix` 将由 IDE Manager 自动安装到 code-server 中。

---

## 9. 失败与恢复策略

### 9.1 故障场景与恢复

| 故障场景 | 检测方式 | 恢复策略 |
|---------|---------|---------|
| **code-server 进程崩溃** | `Health()` 检查 + `cmd.Wait()` 通知 | 自动重启（最多 3 次，间隔 5s），失败后标记 IDE 不可用 |
| **WebSocket 断开** | `onclose` 回调 | 指数退避重连（5s → 10s → 20s → 30s max） |
| **HTTP 反向代理超时** | Gin 返回 502 | 前端显示 "IDE 不可用" 提示，用户刷新重试 |
| **文件冲突（Agent vs 用户）** | VS Code 的 dirty 检测 | Agent 写前发送 `close_file` 命令，检测到 dirty 时提示用户 |
| **扩展加载失败** | `onerror` 回调 | 重试加载，日志记录错误 |
| **端口被占用** | `findAvailablePort` 失败 | 尝试下一个端口，若全部失败则禁用 IDE |

### 9.2 回滚/禁用方案

```toml
# 在 config.toml 中设置
[ide]
enabled = false
```

重启服务即可完全移除 IDE 功能，恢复原始聊天模式。所有 IDE 相关代码通过特性开关隔离：

```go
if !config.IDE.Enabled {
    return nil, nil  // 跳过所有 IDE 初始化
}
```

---

## 10. 总结

本方案通过三层架构集成，在 CodeActor Agent 系统中内嵌了完整的 Web IDE：

### 三层集成架构

| 层级 | 组件 | 职责 |
|------|------|------|
| **基础设施层** | code-server sidecar + Gin 反向代理 | 零侵入地复用现有 HTTP 端口，提供完整 IDE 环境 |
| **通信协议层** | WebSocket 客户端注册 + 编辑器命令协议 | 实现 Agent ↔ IDE 双向消息路由 |
| **应用集成层** | VS Code 扩展增强 + Agent IDE 方法 | 将编辑器能力封装为 Agent 可调用的编程接口 |

### 设计原则

1. **最小化代码侵入**：所有新增功能通过配置开关 `ide.enabled` 完全隔离
2. **最大化资产复用**：现有 `vscode/` 扩展直接增强，避免重复建设
3. **渐进式实施**：4 个阶段逐步交付，每个阶段都有明确的验证标准
4. **安全第一**：默认绑定 localhost，路径穿越防护，密码自动管理

### 预期效果

实施完成后，用户将获得以下能力：

- 🖥️ 在浏览器中打开完整的 VS Code IDE
- 📂 浏览、编辑、搜索工作区文件
- 🤖 Agent 在 IDE 中实时展示代码变更
- 🔄 在 IDE 中修改文件，Agent 感知变更并智能响应
- 🔌 使用 VS Code 扩展市场的任意扩展增强开发体验

---

*本文档是 CodeActor 项目的技术设计文档，随着实施进展持续更新。*
