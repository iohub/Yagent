# ask_user_for_help 工具重构 — TUI 交互设计方案

> 日期: 2025-07-17
> 状态: 草案
> 负责人: Director

---

## 一、现状分析

### 现有实现的问题

经过对项目代码的深入分析，现有 `ask_user_for_help` 工具的实现存在 5 个核心问题：

#### 问题 1：两条分离的交互路径

| 路径 | 实现位置 | 问题 |
|------|----------|------|
| **CLI 模式** | `internal/messaging/consumers/tui.go` → `showUserInputDialog()` | 纯 `bufio.Reader` 文本输入，无选项支持，体验粗糙 |
| **TUI 模式** | `internal/tui/tui_dialogs.go` → `openConfirmDialog()` | **语义错配**，见问题 2 |

#### 问题 2：ConfirmDialog 语义错配

`ask_user_for_help` 被路由到了为 **WorkspaceGuard 授权** 设计的 ConfirmDialog，但两者的语义完全不同：

- **ConfirmDialog 选项**: `a(允许本次)` `t(允许本工具)` `s(会话全部)` `p(项目全部)` `d(拒绝)` — 授权决策
- **ask_user_for_help 需要**: `是/否` 或 `选项A/B/C` 或 `自由文本输入` — 用户协作

**ConfirmDialog 的选项完全不适用于回答问题的场景。**

#### 问题 3：suggested_options 未被利用

工具定义中已有 `suggested_options` 参数（字符串格式），但在 TUI 侧被完全丢弃，用户看不到任何预设选项。

#### 问题 4：缺乏交互类型区分

所有的 `ask_user_for_help` 请求都走同一种交互模式，但实际有 3 种不同的需求场景：

| 需求场景 | 当前行为 | 期望行为 |
|----------|----------|----------|
| "请确认是否删除？" | 显示授权选项 | 显示 `[是] [否]` 按钮（Confirm 模式） |
| "请选择测试框架？" | 显示授权选项 | 显示选项列表（Select 模式） |
| "请描述问题现象？" | 显示授权选项 | 显示文本输入框（Input 模式） |

#### 问题 5：事件协议字段不足

```go
// 现有 UserHelpNeededData — 字段太少
type UserHelpNeededData struct {
    Question string  // 问题文本
    Context  string  // 上下文（可选）
}
// 缺少: interaction_type, options, default_value, placeholder, allow_custom, request_id
```

---

## 二、核心设计思想

### 架构原则

1. **领域分离** — 工具授权确认与用户帮助请求是两个完全不同的领域概念，使用不同的 Dialog 组件
2. **增量改动** — 不修改 ConfirmDialog 任何代码，降低回归风险
3. **向后兼容** — 旧 Agent 发送的请求也能被正确处理（自动推断交互类型）
4. **三态统一** — 三种交互模式共用同一个 `UserHelpDialog` 组件，内部状态机切换

### 核心架构图

```
┌──────────────────────────────────────────────────────────────────┐
│                        Event Bus                                 │
│  ┌─────────────────────┐  ┌──────────────────────────────────┐  │
│  │ EventToolAuthNeeded │  │ EventUserHelpNeeded              │  │
│  │         ↓           │  │         ↓                        │  │
│  │  ConfirmDialog      │  │  UserHelpDialog                  │  │
│  │  (授权语义, 零改动)  │  │  ├─ Confirm 模式 (是/否)         │  │
│  │                     │  │  ├─ Select 模式 (多选一)          │  │
│  │                     │  │  └─ Input 模式 (自由文本)         │  │
│  └─────────────────────┘  └──────────────────────────────────┘  │
│                           │                                     │
│                      DialogStack (统一管理焦点/层级)             │
└──────────────────────────────────────────────────────────────────┘
```

---

## 三、三种交互模式设计

### 模式 1：Confirm（确认模式）

**适用场景**：二元决策 — "是否继续？""是否删除文件？""是否确认操作？"

**交互原型**：
```
╭──────────────────────────────────────────────╮
│  ❓ Ask for Help                             │
│                                              │
│  Do you want to proceed with the deployment? │
│                                              │
│  ℹ Production environment will be affected.  │
│                                              │
│       ┌──────┐       ┌──────┐                │
│       │ Yes ▸│       │  No  │                │
│       └──────┘       └──────┘                │
│                                              │
│  ←→ Navigate  Enter Confirm  y/n Quick       │
╰──────────────────────────────────────────────╯
```

**键盘绑定**：

| 按键 | 功能 |
|------|------|
| `←/→` 或 `Tab` | 在 Yes/No 按钮间切换焦点 |
| `Enter` | 确认当前选中的选项 |
| `y` / `n` | 快速选择 Yes / No 并立即提交 |
| `Esc` | 取消交互（Cancelled=true） |

**自动触发条件**：`suggested_options` 为布尔语义对，如：
- `["yes", "no"]` / `["y", "n"]`
- `["true", "false"]`
- `["allow", "deny"]` / `["approve", "reject"]`
- `["ok", "cancel"]` / `["confirm", "cancel"]`

---

### 模式 2：Select（选择模式）

**适用场景**：从多个选项中选择一个 — "选择测试框架""选择部署环境""选择数据库"

**交互原型**：
```
╭──────────────────────────────────────────────╮
│  ❓ Ask for Help                             │
│                                              │
│  Which testing framework should I use?       │
│                                              │
│  ℹ This choice affects project structure.    │
│                                              │
│    ▸ pytest                                  │
│      unittest                                │
│      nose2                                   │
│      Custom input...                         │
│                                              │
│  ↑↓ Navigate  Enter Select  1-9 Quick        │
╰──────────────────────────────────────────────╯
```

**键盘绑定**：

| 按键 | 功能 |
|------|------|
| `↑/↓` 或 `j/k` | 在选项列表中上下移动光标 |
| `Enter` | 选中当前高亮项 |
| `1`-`9` | 数字键快速选择对应编号的选项 |
| `Tab` | 进入自定义输入模式（如 AllowCustom=true） |
| `Esc` | 取消交互 |

**自定义输入激活后**：
```
    ▸ Custom input...
  ┌──────────────────────────────────────┐
  │ Type your answer here...            │
  └──────────────────────────────────────┘
  Enter Submit  Esc Back to list
```

**自动触发条件**：`suggested_options` ≥ 2 项，且非布尔语义对

---

### 模式 3：Input（输入模式）

**适用场景**：自由文本输入 — "请描述问题""请输入分支名""请提供更多信息"

**交互原型**：
```
╭──────────────────────────────────────────────╮
│  ❓ Ask for Help                             │
│                                              │
│  Please describe the issue you're seeing:    │
│                                              │
│  ℹ Include error messages if possible.       │
│                                              │
│  ┌──────────────────────────────────────┐    │
│  │ The tests fail with timeout...       │    │
│  │                                      │    │
│  └──────────────────────────────────────┘    │
│                                              │
│       ┌──────────┐    ┌────────┐             │
│       │ Submit ▸│    │ Cancel │             │
│       └──────────┘    └────────┘             │
│                                              │
│  Enter Submit  Tab Buttons  Ctrl+U Clear     │
╰──────────────────────────────────────────────╯
```

**键盘绑定**：

| 按键 | 功能 |
|------|------|
| 普通字符输入 | 文本编辑（使用 bubbletea textarea 组件） |
| `Enter` | 提交（单行模式）/ 换行（多行模式） |
| `Tab` | 从输入框切换到 Submit/Cancel 按钮 |
| `Ctrl+U` | 清空输入框 |
| `Esc` | 取消交互 |
| `↑` | 从按钮区域回到文本输入框 |

**自动触发条件**：`suggested_options` 为空或仅有 1 项

---

## 四、交互类型自动推断逻辑

```
                    suggested_options?
                          │
            ┌─────────────┴─────────────┐
            │                           │
          [] 或 [1项]               ≥ 2 项
            │                           │
            ▼                           ▼
       ┌─────────┐          ┌──────────────────────┐
       │  Input  │          │   布尔语义对检测      │
       │  模式   │          │  (yes/no, y/n,        │
       └─────────┘          │   true/false,         │
                            │   allow/deny,         │
                            │   approve/reject...)  │
                            └──────────┬───────────┘
                                       │
                          ┌────────────┴────────────┐
                          │                         │
                          ▼                         ▼
                     ┌─────────┐           ┌──────────────┐
                     │ Confirm │           │   Select     │
                     │  模式   │           │    模式      │
                     └─────────┘           └──────────────┘
```

**Agent 也可通过 `interaction_type` 参数显式指定模式**，覆盖自动推断逻辑。

### 推断实现

```go
func InferInteractionType(options []string) InteractionType {
    switch len(options) {
    case 0, 1:
        return InteractionInput
    case 2:
        if isBooleanPair(options) {
            return InteractionConfirm
        }
        return InteractionSelect
    default:
        return InteractionSelect
    }
}

func isBooleanPair(options []string) bool {
    normalized := make(map[string]bool)
    booleanValues := []string{"yes", "no", "y", "n", "true", "false",
        "allow", "deny", "approve", "reject", "ok", "cancel",
        "confirm", "cancel", "1", "0"}
    for _, v := range booleanValues {
        normalized[v] = true
    }
    
    if len(options) != 2 {
        return false
    }
    return normalized[strings.ToLower(strings.TrimSpace(options[0]))] &&
           normalized[strings.ToLower(strings.TrimSpace(options[1]))]
}
```

---

## 五、事件协议扩展

### 新增类型常量

```go
// InteractionType 定义用户帮助的交互模式
type InteractionType string

const (
    InteractionConfirm InteractionType = "confirm"  // 二元确认
    InteractionSelect  InteractionType = "select"   // 多选一
    InteractionInput   InteractionType = "input"    // 自由输入
)
```

### 扩展 UserHelpNeededData

```go
// UserHelpNeededData 工具请求用户帮助时发送的事件数据
type UserHelpNeededData struct {
    Question        string           `json:"question"`                    // 问题文本
    Context         string           `json:"context,omitempty"`           // 上下文/说明
    InteractionType InteractionType  `json:"interaction_type"`            // ★ 交互模式
    Options         []string         `json:"options,omitempty"`           // ★ 选项列表
    DefaultValue    string           `json:"default_value,omitempty"`     // ★ 默认值
    Placeholder     string           `json:"placeholder,omitempty"`       // ★ 输入占位
    AllowCustom     bool             `json:"allow_custom,omitempty"`      // ★ 允许自定义输入
    RequestID       string           `json:"request_id"`                  // ★ 请求唯一ID
}
```

### 扩展 UserHelpResponseData

```go
// UserHelpResponseData 用户响应帮助请求时返回的数据
type UserHelpResponseData struct {
    Value           string           `json:"value"`                       // 用户回答
    InteractionType InteractionType  `json:"interaction_type"`            // 回传交互类型
    IsCustom        bool             `json:"is_custom,omitempty"`         // 是否自定义输入
    Cancelled       bool             `json:"cancelled,omitempty"`         // 是否取消
    RequestID       string           `json:"request_id"`                  // 匹配请求ID
}
```

### 向后兼容策略

```go
// UnmarshalJSON — 旧 Agent 发来的数据缺少 interaction_type 自动推断
func (d *UserHelpNeededData) UnmarshalJSON(data []byte) error {
    type Alias UserHelpNeededData
    aux := &struct { *Alias }{ Alias: (*Alias)(d) }
    if err := json.Unmarshal(data, aux); err != nil {
        return err
    }
    if d.InteractionType == "" {
        d.InteractionType = InferInteractionType(d.Options)
    }
    return nil
}
```

---

## 六、工具参数增强

### ask_user_for_help 工具参数定义

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `reason` | string | ✅ | 为什么需要用户帮助的说明 |
| `specific_question` | string | ✅ | 具体要问用户的问题 |
| `suggested_options` | []string | ❌ | 预设选项列表 |
| `interaction_type` | string | ❌ | 显式指定模式: confirm/select/input |
| `default_value` | string | ❌ | 默认选中项或预填文本 |
| `placeholder` | string | ❌ | 输入框占位提示文本 |
| `allow_custom` | bool | ❌ | Select 模式是否允许自定义输入（默认 true） |

### Agent 调用示例

```
# 场景 1：确认模式
ask_user_for_help(
    reason="部署到生产环境需要确认",
    specific_question="是否继续部署到生产环境？",
    suggested_options=["yes", "no"]
)

# 场景 2：选择模式
ask_user_for_help(
    reason="不确定使用哪个测试框架",
    specific_question="请选择测试框架：",
    suggested_options=["pytest", "unittest", "nose2"]
)

# 场景 3：输入模式
ask_user_for_help(
    reason="需要用户描述遇到的问题",
    specific_question="请描述你遇到的问题现象：",
    placeholder="包括错误信息、复现步骤..."
)

# 场景 4：选择模式 + 自定义输入
ask_user_for_help(
    reason="需要选择或指定分支名",
    specific_question="请选择目标分支：",
    suggested_options=["main", "develop", "release"],
    allow_custom=true
)
```

---

## 七、UserHelpDialog 组件设计

### 组件结构

```go
// internal/tui/components/user_help_dialog.go

// dialogState 内部状态机
type dialogState int

const (
    stateConfirmSelect dialogState = iota  // Confirm：在 Yes/No 按钮间切换
    stateSelectList                         // Select：在选项列表中导航
    stateSelectCustom                       // Select：自定义输入框激活
    stateInputText                          // Input：文本输入框聚焦
    stateInputButtons                       // Input：在 Submit/Cancel 按钮间切换
)

// UserHelpDialog 实现 Dialog 接口
type UserHelpDialog struct {
    id           string
    data         protocol.UserHelpNeededData
    interaction  protocol.InteractionType
    state        dialogState

    // Confirm 模式状态
    confirmIndex int           // 0=Yes, 1=No

    // Select 模式状态
    selectIndex  int           // 当前光标位置
    options      []optionItem  // 选项列表（含可能的 Custom 项）

    // Input 模式状态
    textInput    textarea.Model
    buttonIndex  int           // 0=Submit, 1=Cancel

    // 样式
    styles       userHelpStyles

    // 结果
    result       *protocol.UserHelpResponseData
    closed       bool
}

type optionItem struct {
    Label    string
    IsCustom bool
}
```

### 状态机转换图

```
                          ┌─────────────┐
                          │  Init()     │
                          └──────┬──────┘
                                 │
                    ┌────────────┼────────────┐
                    │            │            │
                    ▼            ▼            ▼
             ┌──────────┐ ┌──────────┐ ┌──────────┐
             │ Confirm  │ │ Select   │ │  Input   │
             │ 模式     │ │ 模式     │ │  模式    │
             └────┬─────┘ └────┬─────┘ └────┬─────┘
                  │            │            │
            ┌─────┴────┐ ┌────┴────┐  ┌────┴────┐
            │ ← → 切换 │ │ ↑ ↓ 导航│  │ 文本输入 │
            │ y/n 提交 │ │ 1-9选择 │  │ Tab→按钮 │
            │ Esc 取消 │ │ Tab→输入│  │ Enter提交│
            └──────────┘ │ Esc取消 │  │ Esc 取消 │
                         └─────────┘  └─────────┘
                              │
                         ┌────┴────┐
                         │ Custom  │
                         │ 输入    │
                         │ Enter提交│
                         │ Esc返回 │
                         └─────────┘
```

### 键盘事件处理（核心）

| 状态 | 按键 | 行为 |
|------|------|------|
| **stateConfirmSelect** | ← → / Tab | 切换 confirmIndex (0↔1) |
| | y / n | 快速选择并提交 |
| | Enter | 提交当前选中 |
| | Esc | 取消 (Cancelled=true) |
| **stateSelectList** | ↑ ↓ / j k | 移动 selectIndex |
| | 1-9 | 数字快速选择 |
| | Enter | 选中当前项（Custom 项则进入输入） |
| | Tab | 进入 Custom 输入 |
| | Esc | 取消 |
| **stateSelectCustom** | 普通输入 | textarea 编辑 |
| | Enter | 提交自定义文本 |
| | Esc | 返回选项列表 |
| **stateInputText** | 普通输入 | textarea 编辑 |
| | Enter | 提交（单行）/ 换行（多行） |
| | Tab | 切换到按钮区 |
| | Ctrl+U | 清空 |
| | Esc | 取消 |
| **stateInputButtons** | ← → / Tab | 切换 buttonIndex |
| | Enter | 提交或取消 |
| | ↑ | 回到文本输入 |
| | Esc | 取消 |

---

## 八、消息路由改造

### 完整数据流

```
Agent 调用 ask_user_for_help
    │
    ▼
FlowControlTool 构造 UserHelpNeededData
（含 interaction_type、options、request_id 等）
    │
    ▼
UserConfirmManager.RequestUserHelp()
    ├── 生成 RequestID，创建 channel
    ├── 发布 EventUserHelpNeeded 到 EventBus
    └── 阻塞等待 channel（带超时）
    │
    ▼
EventBus 分发到所有 Consumer
    │
    ▼
TUIConsumer 收到事件
    ├── 判断事件类型:
    │   ├── EventToolAuthNeeded → openConfirmDialog()     // 不变
    │   └── EventUserHelpNeeded → openUserHelpDialog()    // ★ 新增
    │
    ▼
DialogStack.Push(UserHelpDialog)
    │
    ▼
用户交互 → Dialog.Update() 处理按键
    │
    ▼
Dialog.IsClosed() == true
    │
    ▼
TUI Update 检测到关闭
    ├── dialogStack.Pop()
    └── respondToUserHelp(result)
        └── 发布 EventUserHelpRespond 到 EventBus
    │
    ▼
UserConfirmManager 收到响应
    ├── 根据 RequestID 路由到对应 channel
    └── channel 写入 → 阻塞解除
    │
    ▼
FlowControlTool 获得用户回答
```

### 路由代码修改点

**`internal/messaging/consumers/tui.go`** — 新增事件路由分支：
```go
func (c *TUIConsumer) handleEvent(event Event) {
    switch event.Type {
    case EventToolAuthNeeded:
        c.openConfirmDialog(event)       // 授权确认 → ConfirmDialog
    case EventUserHelpNeeded:
        c.openUserHelpDialog(event)      // ★ 用户帮助 → UserHelpDialog
    }
}
```

**`internal/tui/tui_update.go`** — 新增响应分发：
```go
func (m *Model) handleDialogClose(closable ClosableDialog) {
    m.dialogStack.Pop()
    switch d := closable.(type) {
    case *ConfirmDialog:
        m.respondToAuth(d.Result())        // 授权响应（不变）
    case *UserHelpDialog:
        m.respondToUserHelp(d.Result())    // ★ 帮助响应（新增）
    }
}
```

---

## 九、CLI 降级方案

当 TUI 不可用时（如非交互式终端、管道模式），提供纯文本降级：

```go
func (c *CLIConsumer) cliConfirm(data) {
    // 显示: "❓ 是否继续? [yes/no]: "
    // 读取用户输入，匹配 yes/no
}

func (c *CLIConsumer) cliSelect(data) {
    // 显示编号列表:
    //   1. pytest
    //   2. unittest
    //   3. nose2
    //   0. Custom input
    // 输入: 2
    // 返回: "unittest"
}

func (c *CLIConsumer) cliInput(data) {
    // 显示: "❓ 请描述: > "
    // 读取用户输入文本
}
```

---

## 十、与 ConfirmDialog 的共存策略

| 维度 | ConfirmDialog | UserHelpDialog |
|------|---------------|----------------|
| **触发事件** | `EventToolAuthNeeded` | `EventUserHelpNeeded` |
| **响应事件** | `EventToolAuthRespond` | `EventUserHelpRespond` |
| **领域语义** | 安全授权（允许/拒绝工具执行） | 用户协作（回答问题/选择/输入） |
| **选项类型** | 固定：a/t/s/p/d | 动态：由 Agent 根据场景提供 |
| **代码改动** | **零改动** | 全新组件 |
| **Dialog 接口实现** | 已有 | 新增实现 |

**关键原则**：ConfirmDialog 代码完全不动，只有消息路由层新增一个分支。

---

## 十一、实现步骤清单

| # | 文件 | 操作 | 工作量 |
|---|------|------|--------|
| 1 | `internal/protocol/agent_events.go` | 新增 InteractionType 类型、扩展 UserHelpNeededData/UserHelpResponseData、添加向后兼容 UnmarshalJSON | ~85 行 |
| 2 | `internal/tui/components/user_help_dialog.go` | **新建**：UserHelpDialog 组件（状态机 + 三种模式渲染 + 键盘事件处理 + 提交/取消逻辑） | ~450 行 |
| 3 | `internal/tui/components/dialog.go` | 可选：新增 ClosableDialog 接口（含 IsClosed()/Result()） | ~8 行 |
| 4 | `internal/tools/flow_control.go` | 扩展 AskUserForHelpParams 参数、新增 resolveInteractionType 推断逻辑 | ~40 行 |
| 5 | `internal/tools/user_confirm.go` | 新增 pendingHelpRequests map、RequestUserHelp/HandleUserHelpResponse 方法、按模式设置超时 | ~55 行 |
| 6 | `internal/messaging/consumers/tui.go` | 新增 openUserHelpDialog 路由分支 | ~15 行 |
| 7 | `internal/tui/tui_update.go` | 新增 respondToUserHelp、扩展 handleDialogUpdate | ~20 行 |
| 8 | `internal/messaging/consumers/cli.go` | 新增三种模式的 CLI 降级交互 | ~80 行 |
| **合计** | | | **~750 行** |

---

## 十二、测试策略

### 单元测试

| 测试项 | 覆盖内容 |
|--------|----------|
| TestInferInteractionType | 所有选项组合的推断结果 |
| TestIsBooleanPair | 各种布尔语义对的识别 |
| TestConfirmModeKeys | ←→切换、y/n提交、Esc取消 |
| TestSelectModeNavigation | ↑↓导航、1-9快选、Enter确认 |
| TestSelectCustomInput | Tab进入输入→Enter提交→返回 |
| TestInputMode | 文本输入→Enter/Tab→Submit |
| TestCancelAllModes | 三种模式 Esc 取消 |
| TestBackwardCompatibility | 旧格式 JSON 反序列化兼容 |

### 集成测试场景

| 场景 | 步骤 | 预期 |
|------|------|------|
| Confirm 端到端 | Agent 调用 `ask_user_for_help(options=["yes","no"])` → TUI 弹窗 → 用户按 `y` | 工具收到 `"yes"` |
| Select 端到端 | Agent 调用 `ask_user_for_help(options=["a","b","c"])` → 用户选 b | 工具收到 `"b"` |
| Input 端到端 | Agent 调用 `ask_user_for_help(question="描述问题")` → 用户输入 | 工具收到文本 |
| Select+Custom | Agent 调用 `ask_user_for_help(allow_custom=true)` → 用户选 Custom → 输入 | `IsCustom=true` |
| 取消 | 任意模式下按 Esc | `Cancelled=true` |
| CLI 降级 | 非 TUI 环境触发 | 纯文本交互正常 |
| 旧 Agent 兼容 | 缺少 interaction_type 的请求 | 自动推断正确模式 |

---

## 十三、风险与回滚

### 风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| UserHelpDialog 有严重 bug | 低 | 中 | 路由层可切回 ConfirmDialog 降级 |
| 协议不兼容 | 低 | 高 | UnmarshalJSON 兜底 + omitempty |
| 性能问题 | 极低 | 低 | 轻量组件，无持续计算 |
| 嵌套弹窗死锁 | 低 | 中 | DialogStack 先入先出，超时兜底 |

### 回滚方案

1. **最小回滚**：TUIConsumer 路由切回 `openConfirmDialog`（1 行改动）
2. **协议回滚**：新字段均有 `omitempty`，旧代码无需修改即可兼容
3. **完全回滚**：移除 UserHelpDialog 注册，恢复原有路由路径
