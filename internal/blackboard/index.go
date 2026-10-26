package blackboard

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 便签索引：从磁盘派生的一页目录，让每个 Agent 一落地就知道"已经有哪些结论可用"。
//
// 它是**派生态**：每次调用都重扫磁盘，不进 ConversationMemory 也不进 rollout。
// 这是索引能扛住上下文压缩的唯一原因——压缩动的是会话历史，而动不了文件系统。

// IndexHeaderPrefix 是索引块的固定开头。注入方据此识别并剔除自己上一轮插过的索引，
// 从而保证同一步内重复注入是幂等的。
const IndexHeaderPrefix = "## Board Index ("

const (
	DefaultIndexMaxNotes  = 12
	DefaultIndexLineChars = 160

	indexToolRunes = 30 // 索引行里 tool 名的上限
	indexTaskRunes = 40 // 索引行里 task 段的上限：认出"属于哪个话题"就够
	indexMinRemain = 24 // task + conclusion 至少留出的预算，否则整行不可读
	indexSep       = " | "
)

// IndexOptions 是索引渲染参数。
type IndexOptions struct {
	// MaxNotes 最多列几条。<0 取 DefaultIndexMaxNotes；**==0 表示不生成索引**。
	// 后者是 [blackboard] index_max_notes 的应急阀门语义，不要"顺手"改成 <=0 走默认。
	MaxNotes int
	// LineChars 单行 rune 预算。<=0 取 DefaultIndexLineChars；
	// 想要"不裁剪整行"请经 IndexOptionsFromConfig 换算成 MaxRenderRunes，别直接传 0。
	LineChars int
}

// IndexOptionsFromConfig 把 [blackboard] 配置的两个整数换算成渲染参数。
//
// 换算必须在这里发生：配置层的 index_line_chars = 0 表示"不裁剪行"，
// 而本包把 <=0 读成"取默认"。直接把 0 传进来会让"不裁剪"变成"裁到 160"。
// MaxNotes 的 0 两边语义一致（关掉索引），原样透传。
func IndexOptionsFromConfig(maxNotes, lineChars int) IndexOptions {
	if lineChars == 0 {
		lineChars = MaxRenderRunes
	}
	return IndexOptions{MaxNotes: maxNotes, LineChars: lineChars}
}

// BuildIndex 扫描 board 并渲染 newest-first 的便签索引。
// 返回 (索引文本, board 内便签总数, error)。board 不存在 → ("", 0, nil)。
// 索引文本为空（无便签，或阀门关掉）时调用方不得注入空块。
func BuildIndex(boardID string, opts IndexOptions) (string, int, error) {
	if opts.MaxNotes == 0 {
		return "", 0, nil
	}
	maxNotes := opts.MaxNotes
	if maxNotes < 0 {
		maxNotes = DefaultIndexMaxNotes
	}
	lineChars := opts.LineChars
	if lineChars <= 0 {
		lineChars = DefaultIndexLineChars
	}

	candidates, err := listNoteFiles(BoardDir(boardID))
	if err != nil {
		return "", 0, err
	}
	total := len(candidates)
	if total == 0 {
		return "", 0, nil
	}

	// 文件名形如 20060102T150405Z_<rand4>-<pid>-<seq6>.md ⇒ 字典序即时间序
	// （时间戳在最前，序号定宽零填充，同进程内还兼作同秒内的写入顺序）。
	// 所以先只按文件名排序，最后仅 decode 最新的 maxNotes 个（不像 ListNotes 那样全读进内存）。
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].name > candidates[j].name
	})

	shown := make([]string, 0, maxNotes)
	skipped := 0
	for _, c := range candidates {
		if len(shown) >= maxNotes {
			break
		}
		data, err := os.ReadFile(filepath.Join(c.dir, c.name))
		if err != nil {
			skipped++
			continue
		}
		note, err := decodeNote(data)
		if err != nil {
			skipped++
			continue
		}
		shown = append(shown, formatIndexLine(note.toMeta(), lineChars))
	}
	if len(shown) == 0 {
		return "", total, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s%s, %d notes, newest first — call read_note(ref) for full text, list_notes(agent) to page)\n",
		IndexHeaderPrefix, SanitizeBoardID(boardID), total)
	for _, line := range shown {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if hidden := total - len(shown); hidden > 0 || skipped > 0 {
		fmt.Fprintf(&b, "[%d older notes not listed — call list_notes() to page", hidden)
		if skipped > 0 {
			fmt.Fprintf(&b, "; %d unreadable notes skipped", skipped)
		}
		b.WriteString("]\n")
	}
	return strings.TrimRight(b.String(), "\n"), total, nil
}

type noteFile struct {
	dir  string
	name string
}

// listNoteFiles 列出 board 下各 Agent 子目录里的便签文件。
// board 目录不存在返回 (nil, nil)。只认 *.md——*.md.tmp 是原子写的中间态。
func listNoteFiles(boardDir string) ([]noteFile, error) {
	agents, err := os.ReadDir(boardDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("blackboard: list board dir: %w", err)
	}
	var out []noteFile
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		dir := filepath.Join(boardDir, a.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), noteFileExt) {
				continue
			}
			out = append(out, noteFile{dir: dir, name: e.Name()})
		}
	}
	return out, nil
}

// formatIndexLine 渲染一行：note_ref | tool | task | conclusion | <size>c | artifact:<id>
//
// 裁剪顺序是刻意的：note_ref 保完整（它是取回句柄），tool 给小上限，
// 剩下的预算按 3:7 分给 task 与 conclusion——识别"这条属于哪个话题"够用即可，结论更值钱。
func formatIndexLine(m NoteMeta, lineChars int) string {
	conclusion := m.Conclusion
	if conclusion == "" {
		conclusion = m.Summary
	}

	head := []string{m.NoteRef, truncateRunes(flatLine(m.Tool), indexToolRunes)}
	tail := []string{strconv.Itoa(m.BodySize) + "c"}
	if len(m.ArtifactIDs) > 0 {
		tail = append(tail, "artifact:"+strings.Join(m.ArtifactIDs, ","))
	}

	// 固定段开销 + 可变段引入的 3 个分隔符。
	fixed := runeLen(strings.Join(head, indexSep)) + runeLen(strings.Join(tail, indexSep)) + 3*runeLen(indexSep)
	remain := lineChars - fixed
	if remain < indexMinRemain {
		remain = indexMinRemain
	}
	// 可变段预算：比例分配保证短行不浪费，硬上限保证长行（尤其是"不裁剪"时）不失控——
	// 索引是目录，不是正文；task 与 conclusion 各自的上限与行总预算无关。
	taskBudget := min(remain*3/10, indexTaskRunes)
	concBudget := min(remain-taskBudget, IndexConclusionRunes)

	line := make([]string, 0, len(head)+len(tail)+2)
	line = append(line, head...)
	line = append(line, truncateRunes(flatLine(m.Task), taskBudget))
	line = append(line, truncateRunes(flatLine(conclusion), concBudget))
	line = append(line, tail...)
	return strings.Join(line, indexSep)
}

// flatLine 把空白（含换行）压成单个空格：索引必须一条便签一行，
// 否则注入块没法按行判断预算，也会把多行正文混进目录里。
func flatLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
