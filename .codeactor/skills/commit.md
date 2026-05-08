# Git Commit

请帮我完成一次专业的开源项目 git commit，按以下步骤执行：

## 步骤 1：检查仓库状态
运行 `git status` 和 `git diff --staged` 查看已暂存的变更。如果没有已暂存的变更，运行 `git diff` 查看未暂存的变更。同时运行 `git log --oneline -5` 查看最近的提交风格。

## 步骤 2：编写 commit message
根据变更内容，编写一条专业的开源项目 commit message，要求：
- 使用 Conventional Commits 格式：`<type>(<scope>): <subject>`
- type 从以下选择：feat, fix, docs, style, refactor, perf, test, chore, ci, build
- subject 使用英文，简洁明了（不超过 72 字符），首字母小写
- 如果需要详细说明，空一行后用英文写 body，每行不超过 72 字符
- **严禁**在 commit message 中出现任何 AI 相关签名，包括但不限于：Co-authored-by: AI, Signed-off-by: AI, AI-generated, ChatGPT, Claude, Copilot 等
- **严禁**在代码或 commit 中添加任何形式的 AI 署名
- 风格参考知名的开源项目（如 Linux kernel, Kubernetes, Rust 等）

## 步骤 3：展示并确认
将编写好的 commit message 展示给用户，请求确认。等待用户回复确认后再继续。

## 步骤 4：执行提交
用户确认后，执行 `git add -A` 暂存所有变更，然后执行 `git commit -m "<message>"` 提交。如果 commit message 包含 body，使用多个 `-m` 参数或临时文件方式。

## 步骤 5：询问推送
提交成功后，询问用户："Commit 已完成，是否需要推送到远程仓库？(git push)"。等待用户回复，如果用户同意则执行 `git push`，否则结束。

---

**注意事项**：
- 如果仓库没有变更，直接告知用户 "没有需要提交的变更" 并结束
- 所有 git 命令使用 `run_bash` 工具执行
- 保持交互自然，不要过度自动化
