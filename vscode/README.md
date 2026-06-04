
<img width="378" height="116" alt="DLICODER" src="https://gist.github.com/user-attachments/assets/0d239666-4dca-4d6d-8f95-6fafa976f9c7" />

# CodeActor — AI Coding Assistant (Claude-powered)

CodeActor 将 Claude 能力带进 VS Code：在侧边栏里对话、对当前代码进行解释/Review，并通过 CodeLens 在函数/类上方一键触发常用指令。

## 功能

- **侧边栏 Chat**：在 Activity Bar 打开 CodeActor 面板进行对话式协作。
- **CodeLens 一键指令**：在函数/类等代码定义上方显示按钮（例如 Explain / Review），点一下就把选中代码按模板发给 Claude。
- **可配置 CodeLens 指令**：通过设置自定义按钮标题、提示文案与 messageTemplate。

## 快速开始

1. 安装扩展后，点击左侧 Activity Bar 的 **CodeActor** 图标。
2. 打开任意代码文件（TypeScript / JavaScript / Python / Go / Rust / Java / C/C++ / C# / Ruby / PHP / Swift 等），在代码定义上方使用 CodeLens 按钮。
3. 如需调整按钮或关闭 CodeLens：打开 VS Code 设置，搜索 **CodeActor**。

## 配置项

### `codeactor.enableCodeLens`

- 类型：`boolean`
- 默认：`true`
- 说明：是否在代码定义上方显示 CodeLens 按钮。

### `codeactor.codeLensCommands`

- 类型：`array`
- 默认包含两条命令：Explain / Review
- 字段：
  - `title`：按钮文案
  - `tooltip`：悬停提示（可选）
  - `action`：动作标识（如 `explain` / `review`）。使用 `edit` 可抑制某个 lens
  - `messageTemplate`：发送给 Claude 的消息模板，使用 `{code}` 占位符

示例：

```jsonc
{
  "codeactor.enableCodeLens": true,
  "codeactor.codeLensCommands": [
    {
      "title": "⚡ Explain",
      "tooltip": "Explain this code",
      "action": "explain",
      "messageTemplate": "Explain the following code:\n\n{code}"
    },
    {
      "title": "🔍 Review",
      "tooltip": "Review for bugs and improvements",
      "action": "review",
      "messageTemplate": "Review the following code for bugs and improvements:\n\n{code}"
    }
  ]
}
```

## 命令

- `CodeActor: Execute CodeLens Command`
- `CodeActor: Edit CodeLens Commands`

可通过命令面板（`Cmd/Ctrl+Shift+P`）搜索 `CodeActor` 执行。

## 构建/打包（开发者）

此仓库提供了一键脚本将 WebUI 打包进扩展并生成 `.vsix`：

```bash
# 在仓库根目录
node scripts/build-vscode-plugin.js

# 或者在 vscode/ 目录
npm run build:plugin
```

环境变量：

- `SERVER_URL`：WebSocket 服务地址（默认：`ws://localhost:3000`）

生成的 `.vsix` 会出现在 `vscode/` 目录下。

## 兼容性

- VS Code：^1.85.0

## 反馈与贡献

仓库：https://github.com/codeactor/codeactor
