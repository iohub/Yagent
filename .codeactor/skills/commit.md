# Git Commit

帮我完成 git commit，流程如下：

## 步骤 1：检查状态
并行运行 `git status`、`git diff --cached --name-only`、`git log --oneline -3`。若 `--cached` 有文件则只提交已暂存文件；若无变更直接结束。

## 步骤 2：提交
- 根据 diff 内容编写 Conventional Commits 格式的 message：`<type>(<scope>): <subject>`（type: feat/fix/docs/style/refactor/perf/test/chore/ci/build，subject 英文小写 ≤72字符）
- **禁止**任何 AI 署名（Co-authored-by/Signed-off-by/AI-generated 等）
- 若用户未手动暂存，先 `git add` 代码文件再 `git commit -m`；若已暂存则直接 `git commit -m`

## 步骤 3：收尾
运行 `git log --oneline -3` 展示结果，然后询问是否推送。
