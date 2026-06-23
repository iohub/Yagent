package compact

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────
// PromptTemplateConfig 可序列化的提示词模板配置
// ─────────────────────────────────────────────────────────

// PromptTemplateConfig 包含所有摘要场景的提示词
type PromptTemplateConfig struct {
	// SegmentPrompt 分段摘要提示词
	SegmentPrompt string `yaml:"segment_prompt" json:"segment_prompt"`

	// MergePrompt 摘要合并提示词
	MergePrompt string `yaml:"merge_prompt" json:"merge_prompt"`

	// FullCompressPrompt 全量压缩提示词
	FullCompressPrompt string `yaml:"full_compress_prompt" json:"full_compress_prompt"`

	// ConstraintPrompt 约束提取提示词
	ConstraintPrompt string `yaml:"constraint_prompt" json:"constraint_prompt"`
}

// ─────────────────────────────────────────────────────────
// 默认提示词模板
// ─────────────────────────────────────────────────────────

// DefaultSegmentPrompt 默认分段摘要提示词
const DefaultSegmentPrompt = `# Role
You are a **Conversation Summarizer** for an AI-powered coding assistant system. Your task is to compress conversation history without losing any critical context needed for ongoing development work.

# Task
Extract the following from the provided conversation fragment:

1. **Task Progress**: What tasks have been completed? What is currently in progress?
2. **Key Decisions**: What important architectural or design decisions were made? Why?
3. **Code Changes**: Which files were modified? What are the key code patterns introduced?
4. **Errors & Fixes**: What problems were encountered? How were they resolved?
5. **Critical Discoveries**: Important facts about the codebase — file structure, dependencies, tech stack, conventions, etc.

# Rules
- **Preserve Identifiers**: Retain ALL specific identifiers — file names, function names, class names, variable names, paths.
- **Preserve Error Details**: Keep concrete error messages and their corresponding fix strategies verbatim.
- **Ignore Redundancy**: Skip duplicated tool output content; keep only the meaningful results.
- **Be Complete**: Do NOT omit any context that could be useful for continuing the work.
- **Be Concise**: Summarize efficiently; prefer bullet points over verbose prose.

# Output Format
- Use clear, structured Markdown.
- Output in **English**.
- Organize extracted information under the 5 categories listed above.`

// DefaultMergePrompt 默认摘要合并提示词
const DefaultMergePrompt = `Merge the following two conversation summaries into a single consolidated summary.
Preserve all important technical details, decisions, and constraints.
Output in the same structured format as the originals.

Summary A:
{summary_a}

Summary B:
{summary_b}

Consolidated Summary:`

// DefaultFullCompressPrompt 默认全量压缩提示词
const DefaultFullCompressPrompt = `# Role
You are a **Conversation Compressor** for an AI-powered coding assistant. Compress the following conversation to fit within the token budget while preserving all critical context.

# Task
Extract and condense:

1. **Task Progress**: What has been done? What is in progress?
2. **Key Decisions**: Architecture, design, and implementation decisions.
3. **Code Changes**: Modified files, key patterns introduced.
4. **Errors & Fixes**: Problems encountered and solutions applied.
5. **Critical Context**: Codebase facts, dependencies, conventions.

# Rules
- Preserve ALL specific identifiers (file names, function names, paths).
- Preserve concrete error messages verbatim.
- Be complete but concise.`

// DefaultConstraintPrompt 默认约束提取提示词
const DefaultConstraintPrompt = `Extract specific constraints, requirements, and rules from the following user messages.
Focus on:

1. Technical constraints (languages, frameworks, versions)
2. Business rules and requirements
3. User preferences (coding style, naming conventions)
4. Format/output requirements
5. Prohibitions (things to avoid)

Output a concise list of constraints found. If no clear constraints exist, output "No specific constraints found."`

// DefaultPromptConfig 默认提示词配置
var DefaultPromptConfig = PromptTemplateConfig{
	SegmentPrompt:      DefaultSegmentPrompt,
	MergePrompt:        DefaultMergePrompt,
	FullCompressPrompt: DefaultFullCompressPrompt,
	ConstraintPrompt:   DefaultConstraintPrompt,
}

// ─────────────────────────────────────────────────────────
// FileBasedPromptTemplate 从文件加载的提示词模板
// ─────────────────────────────────────────────────────────

// FileBasedPromptTemplate 支持从文件加载提示词，可选热更新
type FileBasedPromptTemplate struct {
	mu       sync.RWMutex
	config   PromptTemplateConfig
	filePath string        // 配置文件路径（空表示只使用默认值）
	modTime  time.Time     // 文件最后修改时间（用于热更新检测）
	watch    bool          // 是否启用热更新
}

// NewDefaultPromptTemplate 创建使用默认提示词的模板
func NewDefaultPromptTemplate() *FileBasedPromptTemplate {
	return &FileBasedPromptTemplate{
		config: DefaultPromptConfig,
		watch:  false,
	}
}

// NewFilePromptTemplate 从文件加载提示词模板
// 如果文件不存在或加载失败，使用默认值并记录警告
func NewFilePromptTemplate(filePath string, watch bool) *FileBasedPromptTemplate {
	t := &FileBasedPromptTemplate{
		config:   DefaultPromptConfig,
		filePath: filePath,
		watch:    watch,
	}

	if filePath != "" {
		if err := t.loadFromFile(); err != nil {
			slog.Warn("Failed to load prompt template from file, using defaults",
				"path", filePath, "error", err)
		}
	}

	return t
}

// loadFromFile 从配置文件加载提示词
func (t *FileBasedPromptTemplate) loadFromFile() error {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return fmt.Errorf("read prompt file: %w", err)
	}

	var config PromptTemplateConfig
	// 尝试 JSON 解析
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse prompt file: %w", err)
	}

	// 只替换非空的字段
	t.mu.Lock()
	if config.SegmentPrompt != "" {
		t.config.SegmentPrompt = config.SegmentPrompt
	}
	if config.MergePrompt != "" {
		t.config.MergePrompt = config.MergePrompt
	}
	if config.FullCompressPrompt != "" {
		t.config.FullCompressPrompt = config.FullCompressPrompt
	}
	if config.ConstraintPrompt != "" {
		t.config.ConstraintPrompt = config.ConstraintPrompt
	}
	t.mu.Unlock()

	// 检查是否需要使用默认值（用户显式设置的标志）

	// 记录文件修改时间
	info, err := os.Stat(t.filePath)
	if err == nil {
		t.modTime = info.ModTime()
	}

	slog.Info("Prompt template loaded from file", "path", t.filePath)
	return nil
}

// checkReload 检查文件是否已更新并重新加载（热更新）
func (t *FileBasedPromptTemplate) checkReload() {
	if !t.watch || t.filePath == "" {
		return
	}

	info, err := os.Stat(t.filePath)
	if err != nil {
		return
	}

	if info.ModTime().After(t.modTime) {
		slog.Info("Prompt template file changed, reloading", "path", t.filePath)
		if err := t.loadFromFile(); err != nil {
			slog.Warn("Failed to reload prompt template", "error", err)
		}
	}
}

// SegmentPrompt 返回分段摘要的提示词
func (t *FileBasedPromptTemplate) SegmentPrompt() string {
	t.checkReload()
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.config.SegmentPrompt
}

// MergePrompt 返回摘要合并的提示词
func (t *FileBasedPromptTemplate) MergePrompt() string {
	t.checkReload()
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.config.MergePrompt
}

// FullCompressPrompt 返回全量压缩的提示词
func (t *FileBasedPromptTemplate) FullCompressPrompt() string {
	t.checkReload()
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.config.FullCompressPrompt
}

// ConstraintPrompt 返回约束提取的提示词
func (t *FileBasedPromptTemplate) ConstraintPrompt() string {
	t.checkReload()
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.config.ConstraintPrompt
}

// Config 返回当前配置（用于调试和导出）
func (t *FileBasedPromptTemplate) Config() PromptTemplateConfig {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.config
}

// Reload 手动重新加载配置文件
func (t *FileBasedPromptTemplate) Reload() error {
	if t.filePath == "" {
		return fmt.Errorf("no file path configured")
	}
	return t.loadFromFile()
}

// ─────────────────────────────────────────────────────────
// PromptTemplate 与 LLMSummarizer 的集成适配
// ─────────────────────────────────────────────────────────

// FormatMergePrompt 格式化合并提示词，替换占位符
func FormatMergePrompt(template PromptTemplate, summaryA, summaryB string) string {
	prompt := template.MergePrompt()
	// 替换占位符
	prompt = strings.ReplaceAll(prompt, "{summary_a}", summaryA)
	prompt = strings.ReplaceAll(prompt, "{summary_b}", summaryB)
	return prompt
}

// Ensure FileBasedPromptTemplate implements PromptTemplate
var _ PromptTemplate = (*FileBasedPromptTemplate)(nil)

// ResolvePromptTemplate 解析提示词模板：如果指定了文件路径则从文件加载，否则使用默认模板
// 辅助函数，用于在外部配置中便捷创建模板
func ResolvePromptTemplate(filePath string, watch bool) PromptTemplate {
	if filePath != "" {
		return NewFilePromptTemplate(filePath, watch)
	}
	return NewDefaultPromptTemplate()
}

// MustAbsPath 将文件路径转换为绝对路径（用于配置解析）
func MustAbsPath(filePath string) string {
	if filePath == "" {
		return ""
	}
	if filepath.IsAbs(filePath) {
		return filePath
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		slog.Warn("Failed to resolve absolute path, using original", "path", filePath, "error", err)
		return filePath
	}
	return abs
}
