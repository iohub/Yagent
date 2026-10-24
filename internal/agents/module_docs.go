package agents

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 局部文档注入（docs/agents-md-slimming-design.md §5）。
//
// 包内"这么做会静默坏在哪里"住在 internal/**/AGENTS.md，而运行时注入的只有工作区根的
// AGENTS.md（`project_context.go` 不递归）——所以下沉出去的判据对子 Agent 曾是**不可达**的。
// 这个通道把它接回来：委派前缀带上任务点到的目录的局部文档。
const (
	// 预算刻意不做成配置键：这个旋钮没有人调，写成配置就是第二个"存在但不生效"的假开关
	// （§11 坑 25）。超预算必须留下看得见的省略行，而不是静默少一份文档。
	moduleDocsMaxFiles = 3
	moduleDocsMaxBytes = 12 * 1024

	moduleDocsTitle = "# Module Constraints (verbatim from the nearest AGENTS.md files)"
	moduleDocsHint  = "Package-local hard constraints and the tests that pin them. " +
		"Already in your context: the repository-root AGENTS.md (cross-module rules only)."
)

// moduleDocsTokenRE 抓"至少带一层目录"的路径形态；绝对路径也收（用户在任务里会粘全路径）。
var moduleDocsTokenRE = regexp.MustCompile(`/?[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+`)

type moduleDoc struct {
	rel       string
	data      []byte
	size      int64
	truncated bool
}

// LocalDocsForTask 返回任务点到的目录（及其祖先）的 AGENTS.md，最深优先，永不包含工作区根那一份。
//
// 判据与 ProseForTask 同源：**分母是磁盘**——只有"这个目录真的有一份 AGENTS.md"才注入，
// 匹配不上就是没有，不存在"按形状猜路径然后退回一条危险路径"的分支（§11 坑 42 的反面）。
// 正文每次现读、不缓存：缓存它就得再发明一套失效判据（同 blueprint_view.go:123-125 的理由）。
func LocalDocsForTask(root, task string) string {
	if root == "" || task == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	docs := readModuleDocs(abs, moduleDocsDirs(task, abs))
	if len(docs) == 0 {
		return ""
	}

	// 预算按整份文件收；装不下的最后一份截断，剩下的整份计入省略账。
	cut := len(docs)
	var used int64
	omittedBytes := int64(0)
	droppedFiles := 0
	budget := int64(moduleDocsMaxBytes)
	for i := range docs {
		remaining := budget - used
		if remaining <= 0 {
			cut = i
			break
		}
		if int64(len(docs[i].data)) <= remaining {
			used += int64(len(docs[i].data))
			continue
		}
		omittedBytes += docs[i].size - remaining
		droppedFiles++
		docs[i].data = docs[i].data[:int(remaining)]
		docs[i].truncated = true
		used = budget
		cut = i + 1
		break
	}
	for _, d := range docs[cut:] {
		omittedBytes += d.size
		droppedFiles++
	}

	var b strings.Builder
	b.WriteString("\n\n" + moduleDocsTitle + "\n" + moduleDocsHint + "\n")
	for _, d := range docs[:cut] {
		b.WriteString("\n### ")
		b.WriteString(d.rel)
		b.WriteString("/AGENTS.md")
		if d.truncated {
			b.WriteString(" (truncated)")
		}
		b.WriteString("\n")
		b.Write(d.data)
		b.WriteString("\n")
	}
	if omittedBytes > 0 {
		b.WriteString(fmt.Sprintf("\n> [module docs] %d file(s) fully or partially left out, %s omitted "+
			"(budget %s per delegation)\n", droppedFiles, kib(omittedBytes), kib(moduleDocsMaxBytes)))
	}
	return b.String()
}

// readModuleDocs 按给定顺序读候选目录的 AGENTS.md，最多 moduleDocsMaxFiles 份。
func readModuleDocs(abs string, dirs []string) []moduleDoc {
	var out []moduleDoc
	seen := map[string]bool{}
	for _, dir := range dirs {
		if len(out) >= moduleDocsMaxFiles {
			break
		}
		if dir == "" || dir == "." || strings.HasPrefix(dir, "..") || seen[dir] {
			continue
		}
		seen[dir] = true
		fp := filepath.Join(abs, filepath.FromSlash(dir), "AGENTS.md")
		real, err := filepath.EvalSymlinks(fp)
		if err != nil {
			continue // 这个目录没有局部文档（或读不到）：不是错误
		}
		// 软链可以指向仓库外；只认仍落在工作区里的那一份。
		if !strings.HasPrefix(real, abs+string(filepath.Separator)) {
			continue
		}
		st, err := os.Stat(real)
		if err != nil || st.IsDir() || st.Size() == 0 {
			continue
		}
		data, err := os.ReadFile(real)
		if err != nil {
			continue
		}
		out = append(out, moduleDoc{rel: dir, data: data, size: st.Size()})
	}
	return out
}

// moduleDocsDirs 从任务文本抽出候选目录，返回"最深优先、同深按字典序"的去重列表。
// 顺序必须确定：这段前缀每轮进同一个会话，抖一下就是 provider 前缀缓存整段失效。
func moduleDocsDirs(task, abs string) []string {
	depth := map[string]int{}
	for _, tok := range moduleDocsTokenRE.FindAllString(task, -1) {
		rel := moduleDocsRel(tok, abs)
		if rel == "" {
			continue
		}
		for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if _, ok := depth[dir]; !ok {
				depth[dir] = strings.Count(dir, "/")
			}
		}
	}
	dirs := make([]string, 0, len(depth))
	for d := range depth {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if depth[dirs[i]] != depth[dirs[j]] {
			return depth[dirs[i]] > depth[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})
	return dirs
}

// moduleDocsRel 把一个 token 变成"相对工作区根"的路径；判不了就返回空（宁可少注入，不要猜）。
func moduleDocsRel(tok, abs string) string {
	if !strings.ContainsAny(tok, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") {
		return "" // 纯数字/符号（"3/4"）不是路径
	}
	if strings.HasPrefix(tok, "/") {
		rel, err := filepath.Rel(abs, filepath.Clean(tok))
		if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			return "" // 工作区外的绝对路径：一个字都不读
		}
		tok = filepath.ToSlash(rel)
	}
	tok = path.Clean(strings.TrimPrefix(tok, "./"))
	if strings.HasPrefix(tok, "..") || !strings.Contains(tok, "/") {
		return ""
	}
	return tok
}

func kib(n int64) string {
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}
