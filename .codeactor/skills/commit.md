# Git Commit

请帮我完成一次专业的开源项目 git commit，按以下步骤执行：

## 步骤 1：检查仓库状态
运行 `git status` 和 `git diff --staged` 查看已暂存的变更。如果没有已暂存的变更，运行 `git diff` 查看未暂存的变更。同时运行 `git log --oneline -5` 查看最近的提交风格。

**提示**：如果 `git diff --cached --name-only` 输出非空（即用户已手动暂存了特定文件），则提示「检测到用户已手动暂存 N 个文件，将只提交这些文件」，后续跳过文件过滤步骤。

## 步骤 2：编写 commit message
根据变更内容，编写一条专业的开源项目 commit message，要求：
- 使用 Conventional Commits 格式：`<type>(<scope>): <subject>`
- type 从以下选择：feat, fix, docs, style, refactor, perf, test, chore, ci, build
- subject 使用英文，简洁明了（不超过 72 字符），首字母小写
- 如果需要详细说明，空一行后用英文写 body，每行不超过 72 字符
- **严禁**在 commit message 中出现任何 AI 相关签名，包括但不限于：Co-authored-by: AI, Signed-off-by: AI, AI-generated, ChatGPT, Claude, Copilot 等
- **严禁**在代码或 commit 中添加任何形式的 AI 署名
- 风格参考知名的开源项目（如 Linux kernel, Kubernetes, Rust 等）

## 步骤 3：执行提交

**智能过滤策略**（按优先级判断）：

### 情况 A：用户已手动暂存文件
如果 `git diff --cached --name-only` 输出非空：
- 跳过过滤，直接对这些已暂存文件执行 `git commit -m "<message>"`

### 情况 B：未手动暂存（智能过滤）
1. 运行 `git status --short` 获取所有变更文件列表。
2. **过滤排除以下文件**：

   | 类别 | 排除规则 |
   |------|----------|
   | **数据文件** | 扩展名：`.csv`, `.tsv`, `.xlsx`, `.xls`, `.parquet`, `.arrow`, `.feather`, `.h5`, `.hdf5`, `.npz`, `.npy`, `.pkl`, `.joblib`, `.sqlite`, `.sqlite3`, `.db`, `.dta`, `.sav`, `.rds`, `.rda` |
   | **二进制/编译产物** | 扩展名：`.exe`, `.dll`, `.so`, `.a`, `.o`, `.obj`, `.bin`, `.pt`, `.pth`, `.onnx`, `.safetensors`, `.gguf`, `.wasm`, `.pyc`, `.pyo`, `.class`, `.jar`, `.war`, `.apk`, `.ipa`, `.whl`, `.egg` |
   | **媒体文件** | 扩展名：`.png`, `.jpg`, `.jpeg`, `.gif`, `.bmp`, `.tif`, `.tiff`, `.webp`, `.svg`（非图标/UI资源时排除），`.mp3`, `.wav`, `.flac`, `.ogg`, `.mp4`, `.avi`, `.mov`, `.mkv`, `.webm` |
   | **压缩包** | 扩展名：`.zip`, `.tar`, `.gz`, `.bz2`, `.7z`, `.rar`, `.xz`, `.zst`, `.tgz`, `.tar.gz`, `.tar.bz2` |
   | **测试数据/夹具** | 路径包含：`test/data/`、`tests/data/`、`test/fixtures/`、`tests/fixtures/`、`testdata/`、`__test_data__/`、`sample_data/`、`*.testdata.*` |
   | **大文件提醒** | 单个文件超过 **5MB** 时，跳过并提醒用户手动处理 |

3. 对过滤后的代码文件执行 `git add <file1> <file2> ...`。
4. 执行 `git commit -m "<message>"` 提交。**无需用户确认，直接提交。**
5. 如果过滤后没有任何文件可提交，告知用户「没有需要提交的代码文件（数据文件、二进制文件已自动排除）」，然后结束。

## 步骤 4：展示提交结果
提交完成后，运行 `git log --oneline -3` 展示最近3条提交记录，让用户确认 commit 内容是否正确。

## 步骤 5：询问推送
展示 git log 后，询问用户："提交已完成，以上是最近3条提交记录，是否需要推送到远程仓库？(git push)"。等待用户回复后执行。

---

**注意事项**：
- 如果仓库没有变更，直接告知用户 "没有需要提交的变更" 并结束
- 所有 git 命令使用 `run_bash` 工具执行
- 保持交互自然，不要过度自动化
