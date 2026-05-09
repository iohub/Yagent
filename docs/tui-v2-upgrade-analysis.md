# TUI 库 v1 → v2 升级分析方案

## 1. 概述

本项目 TUI 基于 **Charmbracelet 全家桶** 构建，包含以下四个核心库：

| 库 | 当前版本 | 最新 v2 版本 | v2 发布时间 | 导入路径变更 |
|---|---|---|---|---|
| **bubbletea**（框架） | v1.3.4 | v2.0.6 | 2026-02-24 | `github.com/charmbracelet/bubbletea` → `charm.land/bubbletea/v2` |
| **bubbles**（组件） | v0.21.0 | v2.1.0 | 2026-02-24 | `github.com/charmbracelet/bubbles` → `charm.land/bubbles/v2` |
| **lipgloss**（样式） | v1.1.1 | v2.0.3 | 2026-02-24 | `github.com/charmbracelet/lipgloss` → `charm.land/lipgloss/v2` |
| **glamour**（Markdown） | v1.0.0 | v2.0.0 | 2026-03-09 | `github.com/charmbracelet/glamour` → `charm.land/glamour/v2` |

> ⚠️ **关键约束**：四个库构成紧密生态，必须**同步升级**。bubbletea v2 依赖 lipgloss v2，bubbles v2 依赖 bubbletea v2，glamour v2 依赖 lipgloss v2。

---

## 2. v2 核心变更

### 2.1 bubbletea v2.0.0

| 变更类别 | 说明 |
|---|---|
| 🚀 **Cursed Renderer** | 基于 ncurses 渲染算法，性能大幅提升，Wish 用户获得数量级性能/带宽优化 |
| ⌨️ **键盘消息重构** | `tea.KeyPressMsg` / `tea.KeyReleaseMsg` 拆分；`key.Type`/`key.Runes` → `key.Code`/`key.Text`；支持 Kitty 键盘协议（`shift+enter`、`super+space` 等组合键） |
| 🖱️ **鼠标消息拆分** | `MouseClickMsg` / `MouseReleaseMsg` / `MouseWheelMsg` / `MouseMotionMsg` 独立类型 |
| 📋 **粘贴消息独立** | `tea.PasteMsg` / `PasteStartMsg` / `PasteEndMsg` |
| 🎨 **声明式 View** | 替代 v1 命令式 API（`tea.EnterAltScreen`、`tea.EnableMouseCellMotion` 等改为 `View` struct 字段声明） |
| 🖊️ **原生光标控制** | 可控制光标位置、颜色、形状 |
| 📊 **内置进度条** | `tea.ProgressBar` |
| 🖥️ **同步更新 (Mode 2026)** | 原子性更新终端窗口 |
| 🌐 **Unicode (Mode 2027)** | 自动启用 |
| 📎 **原生剪贴板** | OSC52 协议，支持 SSH 场景复制粘贴 |
| 🎨 **内置颜色下采样** | 自动检测终端颜色配置并降级 ANSI 样式 |
| 🔤 **环境变量** | `tea.EnvMsg` 提供客户端环境变量 |
| 🔌 **原始转义序列** | `tea.Raw()` 可发送原始转义序列 |
| 🔍 **终端查询** | `tea.RequestCursorPosition`、`tea.RequestTerminalVersion` |

### 2.2 bubbles v2.0.0

| 变更类别 | 说明 |
|---|---|
| 🔄 **Getter/Setter** | 所有组件用 getter/setter 替代导出字段（`.Width` → `.Width()` / `.SetWidth()`） |
| 🏗️ **Functional Options** | 构造函数改用 functional options 模式（如 `viewport.New(viewport.WithWidth(80))`） |
| 💡 **亮色/暗色样式** | `help` 等组件支持手动选择样式（因 Lip Gloss v2 移除 `AdaptiveColor`） |
| 🔙 **Init 签名回归** | 恢复为 `func (m Model) Init() tea.Cmd` |
| 🔑 **DefaultKeyMap 变函数** | 每次调用返回新实例，避免共享状态 |
| ✏️ **真实光标** | `textarea` 和 `textinput` 支持真实终端光标（需显式启用） |
| 🗑️ **废弃符号移除** | `NewModel`、`spinner.Tick()`、`viewport.HighPerformanceRendering` 等已移除 |

**各组件详细变更**：

| 组件 | 主要变更 |
|---|---|
| **Cursor** | `Blink` → `IsBlinked`，`BlinkCmd()` → `Blink()` |
| **Progress** | 颜色 API 全面重构，支持多色渐变，`WithColors()` 替代 `WithGradient` |
| **Viewport** | 支持水平滚动、自定义 gutter、正则高亮、软换行 |
| **Textarea** | 真实光标、分页键绑定、样式重构 |
| **Textinput** | 与 textarea 对齐、真实光标、样式重构 |
| **Table** | getter/setter、使用 `ansi.Truncate` |

### 2.3 lipgloss v2.0.0

| 变更类别 | 说明 |
|---|---|
| 🎯 **确定性样式** | Styles 不再隐式依赖全局 I/O，更精确可控 |
| 🤝 **I/O 协作改进** | 与 Bubble Tea v2 同步工作，不再争夺 I/O |
| 🔌 **可控 I/O 源** | 不再默认使用 `stdin`/`stdout`，可指定任意输入输出 |
| 🔧 **compat 兼容包** | 提供 `AdaptiveColor`、`HasDarkBackground` 等快速迁移工具 |
| 🖨️ **Writer 降级颜色** | `lipgloss.Println`/`Sprint`/`Fprint` 替代 `fmt` 函数，自动颜色降级 |
| 🎨 **颜色类型变更** | `lipgloss.Color()` 返回 `color.Color` 而非 `TerminalColor` 类型 |
| 🌈 **边框渐变** | 新增边框渐变支持 |
| 🔗 **超链接** | 支持终端超链接 |
| ✍️ **下划线样式** | 增强下划线样式选择 |
| 📐 **保留样式换行** | `lipgloss.Wrap` 保留 ANSI 样式 |

### 2.4 glamour v2.0.0

| 变更类别 | 说明 |
|---|---|
| 🔗 **集成 Lip Gloss v2** | 底层使用 Lip Gloss v2，Glamour 专注渲染 |
| 📏 **更好的文本换行** | 使用 `lipgloss.Wrap`，改进 CJK、emoji 等多字节字符处理 |
| 🔗 **超链接支持** | 自动支持 ANSI OSC 8 可点击链接 |
| 📧 **邮件链接优化** | `mailto:` 前缀在渲染时自动隐藏 |
| 🌙 **Dark 成为默认** | 移除 `WithAutoStyle()` 和 `AutoStyle`，默认为 `dark` 样式 |
| 🎨 **颜色配置移除** | `WithColorProfile()` 已移除，颜色降级由 Lip Gloss 处理 |
| 🗑️ **移除 Overlined** | 极少使用且终端支持不佳的字段已删除 |

---

## 3. 项目影响范围分析

### 3.1 影响文件总览

```
internal/tui/
├── tui_model.go          ← 核心 model 定义，lipgloss 样式链，textarea/viewport 初始化
├── tui_update.go         ← Update() 主循环，tea.Msg 类型处理
├── tui_helpers.go        ← tea.NewProgram，lipgloss.HasDarkBackground
├── tui_tasks.go          ← tea.Cmd 任务执行
├── tui_dialogs.go        ← lipgloss.Place ×5，JoinHorizontal/Vertical
├── tui_fzf.go            ← tea.Exec，自定义 execCommand 接口
├── tui_view.go           ← lipgloss.JoinHorizontal/Vertical，textarea 尺寸更新
├── tui_render.go         ← viewport.YOffset/TotalLineCount/Width，lipgloss.Width()
├── tui_history.go        ← lipgloss 样式链
├── styles.go             ← 全局 lipgloss 样式常量 (~120 行)
├── render.go             ← lipgloss 工具渲染
└── anim.go               ← lipgloss 动画渐变

pkg/messaging/consumers/
└── tui.go                ← lipgloss 工具徽章 + diff 渲染
```

### 3.2 工作量估算

| 库 | 涉及文件 | API 调用次数 | 预估修改行数 | 风险等级 |
|---|---|---|---|---|
| **bubbletea** | 6 | ~80 处 | ~200 行 | ⚠️ 中高 |
| **bubbles** | 2 | ~25 处 | ~60 行 | ⚠️ 中 |
| **lipgloss** | 9 | ~200 处 | ~450 行 | 🔴 高 |
| **glamour** | 2 | ~4 处 | ~20 行 | 🟢 低 |
| **合计** | **9 文件** | **~310 处** | **~830 行** | **中等偏高** |

### 3.3 关键风险点

#### 🔴 最高风险 — lipgloss 样式系统

- **170+ 处** `lipgloss.NewStyle()` 调用，涉及链式方法签名变化
- **100+ 处** `lipgloss.Color()` 调用，返回类型从 `TerminalColor` → `color.Color`
- `lipgloss.Place()` 参数模式可能从 positional → options
- `lipgloss.JoinHorizontal`/`JoinVertical` 对齐方式参数变化
- `lipgloss.Width()` 工具函数可能移除

#### 🟡 中等风险

- **bubbletea `tea.Exec`/`tea.ExecCommand`** — `tui_fzf.go` 中自定义了 `execCommand` 和 `pipeCommand` 接口适配，v2 接口可能完全改变
- **bubbles viewport** — 直接字段赋值（`Width`/`Height`/`YOffset`）需改为 getter/setter 方法
- **bubbles textarea** — `FocusedStyle`/`BlurredStyle` 链及嵌套 `Cursor.Style` 可能重构

#### 🟢 低风险

- **glamour** — 仅 2 处调用，标准选项
- **bubbletea 核心 API** — `tea.Batch`/`tea.Quit`/`tea.Cmd` 通常稳定

---

## 4. 升级方案

### 4.1 升级策略

采用**渐进式迁移**策略，从附属到核心逐步推进：

```
glamour（附属） → lipgloss（样式） → bubbles（组件） → bubbletea（框架）
```

**为什么这样排序？**

1. glamour 影响最小（2 文件、4 处调用），作为热身
2. lipgloss 是样式基础，必须先稳定
3. bubbles 依赖 bubbletea，但其组件 API 变更独立于框架
4. bubbletea 是核心框架，最后升级以降低风险

### 4.2 阶段一：准备工作

- [ ] 创建 `feature/tui-v2-upgrade` 分支
- [ ] 确保当前所有测试通过，建立基线
- [ ] 阅读各库 v2.0.0 完整 CHANGELOG 和 Migration Guide
- [ ] 在 `go.mod` 中确认 Go 版本要求（v2 可能要求 Go 1.23+）

### 4.3 阶段二：glamour 升级（预估 30 分钟）

**修改文件**：`internal/tui/tui_model.go`、`internal/tui/tui_render.go`

**操作步骤**：
1. 修改导入路径：`github.com/charmbracelet/glamour` → `charm.land/glamour/v2`
2. 移除 `WithAutoStyle()` 选项，改为明确指定 `dark`/`light`
3. 移除 `WithColorProfile()` 选项
4. 运行 `go mod tidy`
5. 编译验证：`go build ./...`

### 4.4 阶段三：lipgloss 升级（预估 3-4 小时）

**修改文件**：`styles.go`、`tui_model.go`、`tui_view.go`、`tui_dialogs.go`、`tui_render.go`、`tui_history.go`、`anim.go`、`render.go`、`tui_helpers.go`、`pkg/messaging/consumers/tui.go`

**操作步骤**：
1. 全局替换导入路径：`github.com/charmbracelet/lipgloss` → `charm.land/lipgloss/v2`
2. 逐文件处理 `lipgloss.Color()` 返回值类型变化
3. 检查 `lipgloss.Place()` 参数签名，适配新 API
4. 检查 `lipgloss.JoinHorizontal`/`JoinVertical` 对齐方式参数
5. 替换 `lipgloss.Width()` → 使用 `Style.Width()` 或 `ansi.PrintableRuneWidth`
6. 如有 `AdaptiveColor` 使用，改用 `compat` 包或手动判断
7. `lipgloss.NewStyle().GetForeground()` 等 getter 方法适配
8. 编译验证

### 4.5 阶段四：bubbles 升级（预估 1-2 小时）

**修改文件**：`tui_model.go`、`tui_view.go`

**操作步骤**：
1. 修改导入路径：`github.com/charmbracelet/bubbles` → `charm.land/bubbles/v2`
2. `viewport.Width = m.termWidth` → `viewport.SetWidth(m.termWidth)`
3. `viewport.Height = ...` → `viewport.SetHeight(...)`
4. `viewport.YOffset` 直接读写 → `viewport.YOffset()` / `viewport.SetYOffset()`
5. `textarea.New()` 构造函数改为 functional options 模式
6. `textarea.FocusedStyle`/`BlurredStyle` 检查是否改为方法
7. `textarea.Cursor.Style` 检查嵌套访问是否改为方法
8. 编译验证

### 4.6 阶段五：bubbletea 升级（预估 2-3 小时）

**修改文件**：`tui_model.go`、`tui_update.go`、`tui_helpers.go`、`tui_tasks.go`、`tui_dialogs.go`、`tui_fzf.go`

**操作步骤**：
1. 修改导入路径：`github.com/charmbracelet/bubbletea` → `charm.land/bubbletea/v2`
2. `tea.Exec`/`tea.ExecCommand` — 适配新接口，重点修改 `tui_fzf.go` 中的自定义实现
3. `tea.KeyMsg` — 适配 `key.Code`/`key.Text` 替代 `key.Type`/`key.Runes`
4. `tea.MouseMsg` — 适配拆分后的鼠标消息类型
5. 命令式 API → 声明式 View struct 字段（如有使用）
6. `tea.NewProgram()` 选项签名检查
7. 编译验证

### 4.7 阶段六：集成测试（预估 2-4 小时）

- [ ] 全量编译：`go build ./...`
- [ ] 运行现有单元测试
- [ ] 手动 TUI 交互测试：
  - 键盘输入响应（普通字符、组合键、特殊键）
  - 鼠标交互（点击、滚轮）
  - 窗口缩放响应
  - 对话框弹出/关闭
  - 历史面板滚动
  - Markdown 渲染效果
  - 颜色/样式显示正确性
- [ ] 边缘场景测试：SSH 终端、不同终端模拟器

---

## 5. 风险评估与应对

| 风险 | 概率 | 影响 | 应对措施 |
|---|---|---|---|
| lipgloss 样式渲染不一致 | 中 | 高 | 逐文件对比 v1/v2 渲染效果，准备回滚方案 |
| `tea.Exec` 接口不兼容 | 高 | 中 | 提前阅读 v2 源码，必要时重写 fzf 集成 |
| v2 存在未修复 bug | 中 | 中 | 关注 GitHub Issues，延迟到 v2.0.x 稳定后再升级 |
| 第三方依赖冲突 | 低 | 中 | `go mod tidy` 后检查依赖树 |

### 回滚方案

如果升级过程中遇到无法解决的问题：

```bash
git checkout main
git branch -D feature/tui-v2-upgrade
```

所有修改限制在 feature 分支内，不影响主干。

---

## 6. 时间规划

| 阶段 | 内容 | 预估时间 |
|---|---|---|
| 准备工作 | 创建分支、阅读文档 | 0.5h |
| glamour 升级 | 导入路径 + 选项调整 | 0.5h |
| lipgloss 升级 | 170+ 处样式调用迁移 | 3-4h |
| bubbles 升级 | viewport/textarea API 迁移 | 1-2h |
| bubbletea 升级 | 框架核心 API 迁移 | 2-3h |
| 集成测试 | 编译、测试、手动验证 | 2-4h |
| **总计** | | **1.5-2 人天** |

---

## 7. 决策建议

### ✅ 升级的理由

1. **性能飞跃** — Cursed Renderer 带来数量级性能提升，大文本渲染更流畅
2. **更好的输入支持** — Kitty 键盘协议支持现代终端组合键
3. **SSH 剪贴板** — OSC52 原生支持，远程开发场景可直接复制粘贴
4. **颜色下采样** — 自动适配不同终端颜色能力，减少手动适配
5. **生态趋同** — Charmbracelet 全系已 v2，v1 将逐渐停止维护，未来新特性仅在 v2 开发

### ❌ 暂不升级的理由

1. 当前 v1 版本稳定运行，无功能缺陷
2. lipgloss 迁移量大（450 行），是最高风险点
3. bubbletea v2.0.0 发布时间较近（2026-02-24），可能还有未发现的 bug
4. 需要 1.5-2 人天的开发测试投入

### 🏁 最终建议

> **建议在下一个迭代周期执行升级。** v2 带来的收益（性能、键盘协议、SSH 剪贴板）对终端应用有实质价值。虽然迁移工作量中等（~830 行），但风险可控——所有修改集中在 `internal/tui/` 目录内，且可以逐步验证。建议在 v2.0.x 发布 2-3 个补丁版本后启动，以确保稳定性。

---

## 附录：参考链接

- [bubbletea v2.0.0 Release Notes](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.0)
- [bubbles v2.0.0 Release Notes](https://github.com/charmbracelet/bubbles/releases/tag/v2.0.0)
- [lipgloss v2.0.0 Release Notes](https://github.com/charmbracelet/lipgloss/releases/tag/v2.0.0)
- [glamour v2.0.0 Release Notes](https://github.com/charmbracelet/glamour/releases/tag/v2.0.0)
- [Charmbracelet 官方博客](https://charm.sh/blog/)
