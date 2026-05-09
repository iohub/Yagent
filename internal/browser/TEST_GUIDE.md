# Browser-Agent 系统功能测试指南

## 1. 概述

本文档提供 Browser-Agent 系统的完整功能测试指南。测试目标是对 Browser-Agent 的 **17 个工具**进行系统级功能验证，确保以下核心功能全部正常工作：

- **导航**：页面跳转、前进/后退、刷新、重定向跟随
- **交互**：点击、输入、滚动、表单操作
- **等待**：元素等待、固定毫秒等待
- **提取**：文本提取、HTML 提取、属性获取
- **输出**：视口截图、全页截图、PDF 生成
- **Cookie 管理**：获取 Cookie、设置 Cookie
- **高级工具**：安全 JavaScript 执行与危险操作拦截
- **错误处理**：路径验证、选择器校验、超时处理

## 2. 测试架构

### 测试基础设施

| 组件 | 说明 |
|------|------|
| 测试文件位置 | `internal/browser/browser_integration_test.go` |
| 构建标签 | `//go:build integration` |
| 测试服务器 | `internal/browser/testhelpers/server.go` |
| 浏览器管理 | 共享浏览器管理器（Headless Chrome + Rod） |
| 测试框架 | Go testing + testify/require |

### 架构特点

- **共享浏览器实例**：所有测试用例复用同一个 Headless Chrome 实例，减少启动开销
- **本地测试服务器**：使用 Go `net/http/httptest` 创建本地 HTTP 服务器，不依赖外部网络
- **隔离测试数据**：每个测试用例使用独立的工作区和临时目录
- **自动化清理**：测试结束后自动关闭浏览器、清理临时文件

## 3. 测试环境要求

| 要求 | 版本/说明 |
|------|-----------|
| Go | 1.21+ |
| 浏览器 | Chrome 或 Chromium（Headless 模式） |
| 操作系统 | Linux / macOS / Windows |
| 内存 | 建议 2GB+ 可用内存 |
| 磁盘 | 建议 500MB+ 可用空间（用于临时文件和 PDF 输出） |

### 安装依赖

```bash
# 安装 Go 依赖
go mod download

# 安装 Chrome/Chromium（Linux 示例）
sudo apt-get install -y chromium-browser

# 或使用官方 Chrome
wget -q -O - https://dl-ssl.google.com/linux/linux_signing_key.pub | sudo apt-key add -
sudo sh -c 'echo "deb [arch=amd64] http://dl.google.com/linux/chrome/deb/ stable main" >> /etc/apt/sources.list.d/google.list'
sudo apt-get update && sudo apt-get install -y google-chrome-stable
```

## 4. 运行测试

### 基本用法

```bash
# 运行全部集成测试
go test -tags integration ./internal/browser/ -v -timeout 180s

# 运行单个测试
go test -tags integration ./internal/browser/ -v -run TestNavigate_Success -timeout 60s

# 运行特定类别
go test -tags integration ./internal/browser/ -v -run "TestNavigate|TestClick" -timeout 120s

# 仅运行截图相关测试
go test -tags integration ./internal/browser/ -v -run Screenshot -timeout 60s

# 输出覆盖率（需要安装 go test 覆盖率工具）
go test -tags integration ./internal/browser/ -coverprofile=coverage.out -timeout 180s
go tool cover -html=coverage.out  # 查看 HTML 覆盖率报告
```

### 高级选项

```bash
# 设置浏览器路径（如果不在默认位置）
BROWSER_PATH=/usr/bin/chromium go test -tags integration ./internal/browser/ -v

# 保留浏览器窗口（调试用，非 Headless 模式）
HEADLESS=false go test -tags integration ./internal/browser/ -v -run TestClick_Element

# 增加测试日志输出
go test -tags integration ./internal/browser/ -v -count=1

# 并行运行测试（注意：部分测试依赖共享状态，可能影响结果）
go test -tags integration ./internal/browser/ -parallel=2 -timeout 180s
```

## 5. 测试用例清单

按功能类别列出全部 **37 个测试用例**。

### 5.1 导航类测试（7 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 1 | `TestNavigate_Success` | 导航到测试主页 | URL 和标题正确 |
| 2 | `TestNavigate_InvalidURL` | 导航到无效 URL | 返回错误 |
| 3 | `TestNavigate_NonHTTPProtocol` | 导航到 `ftp://` | 安全策略拒绝 |
| 4 | `TestGetCurrentURL` | 获取当前 URL | URL 正确 |
| 5 | `TestGoBack` | 后退导航 | 回到上一页 |
| 6 | `TestGoForward` | 前进导航 | 前进到下一页 |
| 7 | `TestReload` | 页面刷新 | 页面仍可用 |

### 5.2 交互类测试（6 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 8 | `TestClick_Element` | 点击按钮 | onclick 触发，文本变为 `"clicked"` |
| 9 | `TestClick_NonExistentSelector` | 点击不存在元素 | 返回错误 |
| 10 | `TestInput_Text` | 输入文本 | 输入框值正确 |
| 11 | `TestInput_Empty` | 清空输入 | 输入框为空 |
| 12 | `TestScroll_ToCoordinates` | 坐标滚动 | `scrollY=1000` |
| 13 | `TestScroll_ToElement` | 元素滚动 | 元素进入视口 |

### 5.3 等待类测试（3 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 14 | `TestWaitElement_Success` | 等待延迟元素 | 元素出现成功 |
| 15 | `TestWaitElement_Timeout` | 等待不存在元素 | 超时错误 |
| 16 | `TestWait_FixedMillis` | 固定毫秒等待 | 等待 ≥ 450ms |

### 5.4 提取类测试（4 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 17 | `TestExtractText_FullPage` | 全页文本提取 | 包含 `"Test Page"` |
| 18 | `TestExtractText_BySelector` | 选择器文本提取 | 文本精确匹配 |
| 19 | `TestExtractHTML_BySelector` | HTML 提取 | 包含元素 HTML |
| 20 | `TestExtractText_Truncation` | 文本截断 | `max_chars` 生效 |

### 5.5 输出类测试（3 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 21 | `TestScreenshot_Viewport` | 视口截图 | 文件非空 |
| 22 | `TestScreenshot_FullPage` | 全页截图 | ≥ 视口截图大小 |
| 23 | `TestPDF_Generation` | PDF 生成 | 有效 PDF 文件 |

### 5.6 Cookie 类测试（2 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 24 | `TestGetCookies` | 获取 Cookie | 找到 `test-cookie` |
| 25 | `TestSetCookies` | 设置 Cookie | 设置的 Cookie 可获取 |

### 5.7 高级工具测试（2 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 26 | `TestEvaluateJS_Safe` | 安全 JS 执行 | `document.title` 正确 |
| 27 | `TestEvaluateJS_Dangerous` | 危险 JS 拒绝 | `eval`/`Function`/`document.write` 被拦截 |

### 5.8 错误处理测试（2 个）

| # | 测试名称 | 测试目标 | 预期结果 |
|---|---------|---------|---------|
| 28 | `TestScreenshot_InvalidPath` | 无效路径验证 | 路径校验拒绝 |
| 29 | `TestScreenshot_InvalidPath_Tool` | 工具级路径验证 | 工具拒绝无效路径 |

### 5.9 辅助测试（8 个）

| # | 测试名称 | 测试目标 |
|---|---------|---------|
| 30 | `TestExtractHTML_FullPage` | 全页 HTML 提取 |
| 31 | `TestExtractText_MultipleElements` | 多元素文本提取 |
| 32 | `TestGetAttribute` | 元素属性获取 |
| 33 | `TestFormInteraction` | 表单提交验证 |
| 34 | `TestRedirect_Follow` | 302 重定向跟随 |
| 35 | `TestMultipleCookies` | 多 Cookie 设置验证 |
| 36 | `TestElementVisibility` | 元素可见性检查 |
| 37 | `TestPageHistory` | 导航历史验证 |

## 6. 覆盖矩阵

下表展示 **17 个浏览器工具** 与测试用例的映射关系，确保每个工具都有对应的测试覆盖。

| # | 工具 | 覆盖测试 | 测试数量 |
|---|------|---------|---------|
| 1 | `navigate` | #1, #2, #3, #34 | 4 |
| 2 | `get_current_url` | #4 | 1 |
| 3 | `go_back` | #5, #37 | 2 |
| 4 | `go_forward` | #6, #37 | 2 |
| 5 | `reload` | #7 | 1 |
| 6 | `click` | #8, #9, #33 | 3 |
| 7 | `input` | #10, #11, #33 | 3 |
| 8 | `scroll` | #12, #13 | 2 |
| 9 | `wait_element` | #14, #15 | 2 |
| 10 | `wait` | #16 | 1 |
| 11 | `extract_text` | #17, #18, #20, #31 | 4 |
| 12 | `extract_html` | #19, #30 | 2 |
| 13 | `get_attribute` | #32 | 1 |
| 14 | `screenshot` | #21, #22, #28, #29 | 4 |
| 15 | `pdf` | #23 | 1 |
| 16 | `get_cookies` | #24, #35 | 2 |
| 17 | `set_cookies` | #25, #35 | 2 |
| 18 | `evaluate_js` | #26, #27 | 2 |
| | **合计** | **37 个测试用例** | |

### 覆盖率统计

```
导航工具:     ████████████████████ 100% (4/4 测试覆盖)
交互工具:     ████████████████████ 100% (6/6 测试覆盖)
等待工具:     ████████████████████ 100% (3/3 测试覆盖)
提取工具:     ████████████████████ 100% (7/7 测试覆盖)
输出工具:     ████████████████████ 100% (5/5 测试覆盖)
Cookie 工具:  ████████████████████ 100% (5/5 测试覆盖)
JS 执行工具:  ████████████████████ 100% (2/2 测试覆盖)
错误处理工具: ████████████████████ 100% (2/2 测试覆盖)
```

## 7. 测试服务器路由说明

测试服务器（`internal/browser/testhelpers/server.go`）提供以下 HTTP 端点用于测试：

| 路由 | 方法 | 用途 | 响应内容 |
|------|------|------|---------|
| `/` | GET | 主测试页面 | 包含按钮、输入框、滚动目标、表单等完整交互元素 |
| `/page2` | GET | 第二页面 | 用于前进/后退测试 |
| `/set-cookie` | GET | 设置 Cookie | 设置 `test-cookie` 响应头 |
| `/delay` | GET | 重定向测试 | 302 重定向到 `/delay-content` |
| `/delay-content` | GET | 延迟内容页面 | JS 2 秒后动态插入 `#delayed` 元素 |
| `/api/data` | GET | JSON API | 返回 `{"status":"ok","message":"Hello from API"}` |
| `/form` | GET | 表单页面 | 包含 name 和 email 输入框及提交按钮 |
| `/submit` | POST | 表单提交 | 处理表单数据并返回确认页面 |
| `/redirect` | GET | 重定向测试 | 302 重定向回 `/` |
| `/set-multiple-cookies` | GET | 多 Cookie | 设置 `cookie-a` 和 `cookie-b` |

### 测试页面元素说明

主页面 `/` 包含以下关键元素用于交互测试：

```html
<!-- 点击测试 -->
<button id="test-button" onclick="this.textContent='clicked'">Click me</button>

<!-- 输入测试 -->
<input type="text" id="test-input" placeholder="Enter text">

<!-- 滚动测试 -->
<div id="scroll-target" style="height: 2000px;">Scroll Target</div>
<div id="tall-content" style="height: 3000px;"></div>

<!-- 延迟元素测试 -->
<div id="delayed" style="display:none;">Delayed Content</div>
<script>
  setTimeout(function() {
    document.getElementById('delayed').style.display = 'block';
  }, 2000);
</script>
```

## 8. 注意事项

### 构建标签

- 测试使用 `//go:build integration` 构建标签，**不会**在普通 `go test` 中运行
- 必须显式使用 `-tags integration` 标志才能运行这些测试
- 这确保了 CI/CD 流水线可以在不需要浏览器环境的情况下快速运行单元测试

### 浏览器要求

- 测试需要 Chrome 或 Chromium 浏览器（Headless 模式，自动启动）
- 浏览器路径可通过 `BROWSER_PATH` 环境变量指定
- 如果未找到浏览器，测试将跳过并显示警告
- 浏览器以无头模式（Headless）运行，不会弹出 GUI 窗口

### 网络与隔离

- 所有 HTTP 请求使用本地 `httptest.Server`，**不依赖外部网络**
- 测试服务器绑定到 `127.0.0.1`，端口随机分配
- 测试工作区使用临时目录（`os.MkdirTemp`），测试结束后自动清理
- 每个测试用例的浏览器状态独立，通过 `SetupBrowser`/`TeardownBrowser` 管理

### 超时与性能

| 类型 | 超时时间 | 说明 |
|------|---------|------|
| 单个测试 | 30 秒 | 默认超时，可通过 `-timeout` 标志调整 |
| 全量测试 | 180 秒 | 建议全量运行时设置的超时时间 |
| 元素等待 | 5 秒 | `wait_element` 默认超时时间 |
| JS 执行 | 10 秒 | JavaScript 执行超时 |

### 安全注意事项

- 工具级路径验证确保文件操作在允许目录内进行
- JavaScript 沙箱拦截危险操作：`eval`、`Function` 构造函数、`document.write`、`document.domain` 修改
- Cookie 值在工具返回时会被脱敏显示为 `[REDACTED]`，测试直接使用 Rod API 验证原始值

### 调试技巧

```bash
# 查看测试输出中的详细日志
go test -tags integration ./internal/browser/ -v -run TestNavigate_Success 2>&1 | grep -i "browser\|navigate"

# 运行单个测试并查看浏览器日志
go test -tags integration ./internal/browser/ -v -run TestClick_Element -count=1

# 如果测试卡在浏览器启动，检查 Chrome 是否可用
chromium-browser --version

# 验证测试服务器是否可以正常响应
curl http://127.0.0.1:8080/  # 需要先启动测试服务器
```

### 常见问题

| 问题 | 可能原因 | 解决方案 |
|------|---------|---------|
| `browser not found` | Chrome/Chromium 未安装 | 安装 Chrome 或设置 `BROWSER_PATH` |
| `test timeout` | 系统资源不足 | 增加 `-timeout` 或释放系统资源 |
| `screenshot failed` | 磁盘空间不足 | 检查临时目录可用空间 |
| `connection refused` | 测试服务器端口冲突 | 测试框架自动选择端口，通常不会冲突 |

## 附录 A：测试执行流程图

```
┌─────────────────────────────────────────────────────────────────┐
│                        测试执行流程                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. go test -tags integration                                   │
│       │                                                         │
│       ▼                                                         │
│  2. TestMain()                                                  │
│       │                                                         │
│       ├── 启动本地测试服务器 (:随机端口)                          │
│       │                                                         │
│       ├── 创建共享浏览器管理器 (Headless Chrome + Rod)           │
│       │                                                         │
│       ▼                                                         │
│  3. 每个测试用例                                                │
│       │                                                         │
│       ├── SetupBrowser() → 创建独立 BrowserInstance              │
│       │                                                         │
│       ├── 执行测试逻辑                                          │
│       │    │                                                     │
│       │    ├── 导航测试 → Navigate()                             │
│       │    ├── 交互测试 → Click() / Input() / Scroll()           │
│       │    ├── 等待测试 → WaitElement() / Wait()                 │
│       │    ├── 提取测试 → ExtractText() / ExtractHTML()          │
│       │    ├── 输出测试 → Screenshot() / PDF()                   │
│       │    └── Cookie/JS 测试 → GetCookies() / EvaluateJS()      │
│       │                                                         │
│       └── TeardownBrowser() → 关闭 BrowserInstance               │
│                                                                 │
│  4. Teardown()                                                  │
│       │                                                         │
│       ├── 关闭所有浏览器实例                                     │
│       │                                                         │
│       ├── 清理测试服务器                                        │
│       │                                                         │
│       └── 清理临时目录                                          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## 附录 B：测试报告格式

测试运行完成后，输出格式如下：

```
=== RUN   TestNavigate_Success
--- PASS: TestNavigate_Success (0.15s)
    browser_integration_test.go:XX: Navigate to http://127.0.0.1:XXXXX/
    browser_integration_test.go:XX: Expected URL http://127.0.0.1:XXXXX/, got http://127.0.0.1:XXXXX/
    browser_integration_test.go:XX: Expected title Test Page, got Test Page

=== RUN   TestClick_Element
--- PASS: TestClick_Element (0.12s)
    browser_integration_test.go:XX: Clicking button#test-button
    browser_integration_test.go:XX: Expected text: clicked, got: clicked
...

PASS
coverage: 85.3% of statements
ok      codeactor-agent/internal/browser    12.345s  37 tests passed
```

---

*本文档最后更新：2025 年*
*适用于 Browser-Agent v1.0+*
