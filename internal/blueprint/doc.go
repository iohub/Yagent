package blueprint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"yagent/internal/util"
)

// docSchemaVersion 是本文档格式的版本号。
// 认不出的版本**不猜**：Parse 返回 ErrUnknownVersion，由调用方决定"整份语义丢弃、
// 结构反正会重派生"（设计稿 §3.1）。做读时兼容就等于让每种历史格式都活在一个地方。
const docSchemaVersion = 1

// ErrUnknownVersion 表示 front-matter 的版本号不是这份代码认识的。
var ErrUnknownVersion = errors.New("blueprint: 未知的文档版本")

const (
	frontMatterDelim = "---"
	docTitle         = "# Repository Blueprint"
)

// Front 是 `---` 包裹的 JSON 头（与 blackboard 的便签同形：不引 YAML、解析只有 `encoding/json`）。
//
// Atoms/Stale/Unanchored 三个计数是**正文的派生态**：Render 每次都用正文重算再写，
// 所以手改或旧版本留下的错数不会漂着活下去（同一条纪律见 §6.4 的便签索引"索引必须是派生态"）。
type Front struct {
	Version     int            `json:"version"`
	ProjectID   string         `json:"project_id"`
	ProjectPath string         `json:"project_path"`
	UpdatedAt   string         `json:"updated_at"`
	GitSHA      string         `json:"git_sha,omitempty"`
	GitBranch   string         `json:"git_branch,omitempty"`
	GitOrigin   string         `json:"git_origin,omitempty"`
	Folds       int            `json:"folds"`
	Atoms       int            `json:"atoms"`
	Stale       int            `json:"stale"`
	Unanchored  int            `json:"unanchored"`
	Sources     map[string]int `json:"sources,omitempty"`      // {agent 名: 该 agent 提交的原子数}
	DocSections int            `json:"doc_sections,omitempty"` // 有内容的段落数（含 Modules 下的片）
	// Evicted 是历史上因单段配额被挤出去的原子累计数（§7.4 第 6 条）。
	// 正文里没有被淘汰者的身影，所以这笔账只能留在 front-matter。
	Evicted int `json:"evicted,omitempty"`

	// ModuleFingerprints 是每片模块注解对应的**结构指纹**（R3 写入、渲染时比较）。
	// 它记的是"这段散文当时是关于哪个形状的"，不是"这个形状是权威"。
	ModuleFingerprints map[string]string `json:"module_fingerprints,omitempty"`
}

// Doc 是一份蓝图文档：front-matter + 正文（正文只含 asserted 段落）。
type Doc struct {
	Front Front
	Body  string
}

// EmptyDoc 是给"这个项目还没有蓝图"的初始形态。正文段落骨架由 RenderBody 生成，
// 空文档落盘后一眼就能看出"只有结构、还没有人读过"。
func EmptyDoc(projectID, projectPath string) *Doc {
	return &Doc{
		Front: Front{
			Version:     docSchemaVersion,
			ProjectID:   projectID,
			ProjectPath: projectPath,
		},
	}
}

// Parse 读回一份文档。
// 坏了的 front-matter（非法 JSON / 非对象）返回 error；正文里坏掉的原子行不在这里报，
// 由 Sections 逐段回报 —— 解析一份文档不应该因为一行坏了就整份读不了。
func Parse(raw []byte) (*Doc, error) {
	text := string(raw)
	if !strings.HasPrefix(text, frontMatterDelim) {
		return nil, fmt.Errorf("blueprint: 文档没有 front-matter（应以 %q 开头）", frontMatterDelim)
	}
	rest := text[len(frontMatterDelim):]
	end := strings.Index(rest, "\n"+frontMatterDelim)
	if end < 0 {
		return nil, errors.New("blueprint: front-matter 没有闭合")
	}
	header := strings.TrimSpace(rest[:end])
	body := strings.TrimSpace(rest[end+len("\n"+frontMatterDelim):])
	// 一级标题由 Render 恒定写出，所以它不属于 Body —— 否则"读回来再渲染一次"会多出一行，
	// 幂等断言当场就红了（TestDocRenderParseRoundTrip 钉的就是这条）。
	body = strings.TrimSpace(strings.TrimPrefix(body, docTitle))

	var f Front
	if err := json.Unmarshal([]byte(header), &f); err != nil {
		return nil, fmt.Errorf("blueprint: front-matter 不是合法 JSON: %w", err)
	}
	if f.Version != docSchemaVersion {
		return nil, fmt.Errorf("%w: %d（本实现认 %d）", ErrUnknownVersion, f.Version, docSchemaVersion)
	}
	return &Doc{Front: f, Body: body}, nil
}

// Render 输出落盘字节。计数与段落数在这里重算（见 Front 的注释）。
func (d *Doc) Render() ([]byte, error) {
	f := d.Front
	f.Version = docSchemaVersion

	counts, sections := scanCounts(d.Body)
	f.Atoms = counts.atoms
	f.Stale = counts.stale
	f.Unanchored = counts.unanchored
	f.DocSections = sections

	payload, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("blueprint: marshal front-matter: %w", err)
	}

	var b strings.Builder
	b.WriteString(frontMatterDelim)
	b.WriteByte('\n')
	b.Write(payload)
	b.WriteString("\n" + frontMatterDelim + "\n")
	b.WriteString(docTitle)
	b.WriteByte('\n')
	body := strings.TrimSpace(d.Body)
	if body != "" {
		b.WriteByte('\n')
		b.WriteString(body)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// Load 从 root 读文档；文件不存在返回 (nil, nil) —— "还没有蓝图"不是错误。
func Load(root string) (*Doc, error) {
	path := DocPath(root)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("blueprint: read %s: %w", path, err)
	}
	return Parse(raw)
}

// Save 原子写落盘（0700 目录 / 0600 文件：里面会有路径与结论，不该给同机其他用户读）。
func Save(root string, d *Doc) error {
	data, err := d.Render()
	if err != nil {
		return err
	}
	return writeDoc(root, data)
}

// writeDoc 是文档字节的唯一落盘口子（Save 与 fold 的 CAS 写共用它）。
//
// 分成两个函数的理由只有一个：CAS 必须在写之前把**已经渲染好的字节**再比一次，
// 如果它调的是 Save，就会被重新渲染一遍 —— 两次 Render 之间时钟或计数一动，
// 比较的对象和写的对象就不是同一份字节了。
func writeDoc(root string, data []byte) error {
	return util.AtomicWrite(DocPath(root), data, util.AtomicWriteOptions{
		DirPerm:   0o700,
		FilePerm:  0o600,
		TmpSuffix: ".md.tmp",
		Label:     "blueprint",
	})
}

// SectionBlock 是一个段落及其原子。
type SectionBlock struct {
	Heading string // "Architecture" 或 "Modules/internal/tui"
	Module  string // 前缀段的片路径，其它段为空
	Atoms   []Atom
}

// Sections 把正文切成段落。第二返回值是坏掉的原子行（带行号）：
// 调用方必须把它们记进账目而不是静默跳过 —— "这一轮为什么没更新蓝图"要能被回答。
func (d *Doc) Sections() ([]SectionBlock, []error) {
	var (
		blocks []SectionBlock
		errs   []error
		cur    = -1
	)
	for i, line := range strings.Split(d.Body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## "):
			heading := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			block := SectionBlock{Heading: heading}
			if path, ok := ModulePath(heading); ok {
				block.Module = path
			}
			blocks = append(blocks, block)
			cur = len(blocks) - 1
		case trimmed == "" || strings.HasPrefix(trimmed, "# "):
			// 标题行与空行不进段落（"document title" 与段落之间的间隔）。
		case cur < 0:
			if trimmed != "" {
				errs = append(errs, fmt.Errorf("blueprint: 第 %d 行在任何段落之外: %q", i+1, trimmed))
			}
		default:
			a, ok, err := ParseAtomLine(line)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("第 %d 行: %w", i+1, err))
			case ok:
				blocks[cur].Atoms = append(blocks[cur].Atoms, a)
			default:
				// 非原子的一行：段落里只准有原子，否则就是文档被手改过或折叠写坏了。
				errs = append(errs, fmt.Errorf("blueprint: 第 %d 行不是原子却落在段落里: %q", i+1, trimmed))
			}
		}
	}
	return blocks, errs
}

type counts struct{ atoms, stale, unanchored int }

func scanCounts(body string) (counts, int) {
	var c counts
	sections := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			sections++
			continue
		}
		a, ok, err := ParseAtomLine(line)
		if err != nil || !ok {
			continue
		}
		c.atoms++
		switch a.State {
		case StateStale:
			c.stale++
		case StateUnanchored:
			c.unanchored++
		}
	}
	return c, sections
}

// DocPath 是文档在 root 下的位置。
func DocPath(root string) string { return filepath.Join(root, "blueprint.md") }

// RevisionsDir 是 fold 前整档快照的目录（R6 用到；这里先把路径规则定在一处）。
func RevisionsDir(root string) string { return filepath.Join(root, "revisions") }
