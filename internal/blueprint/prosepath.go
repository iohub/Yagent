package blueprint

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// 本文件是 I14（"散文里的路径必须属于这片"）的判据来源。
//
// 它挂在 AnchorChecker 上而不是 Snapshot 上，理由有两条：
//   - 存在性本来就归它管（§5.2(b) 的锚点复核是唯一 os.Lstat 的地方），
//     再加一份"另一个地方也 stat 一遍"就是会漂走的第二份事实（§11 坑 18）；
//   - Snapshot 是 atomic.Pointer 发布的共享只读副本，往里塞懒建缓存是数据竞态；
//     AnchorChecker 的生命周期是**一次 fold**，天生就是这份缓存的作用域。

// normalizeProsePath 把散文里的 token 归一成仓库相对 POSIX 路径。
// 不处理绝对路径与 `../`：pathTokens 已把首尾的 `/` 和 `.` 剪掉，
// 越出根与凭空路径到了这里都是同一种形态（存在性由 `where` 锚点管，见 foreignPath）。
func normalizeProsePath(tok string) string {
	p := filepath.ToSlash(strings.TrimSpace(tok))
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// ownersOf 回答"这个 token 在仓库里到底指哪几片"。返回去重、排序的片路径；
// 空切片 = 派生集合里找不到同名东西（既可能是散文，也可能是凭空写的路径）。
//
// 两条形态都要认，否则惩罚的是准确而不是幻觉：
//  1. 从根写全的 "internal/blueprint/fold.go" —— 走 ModuleOf（目录前缀逐级上溯）；
//  2. 少写前缀的 "blueprint/fold.go" 与裸文件名 "fold.go" —— 模型写的是人话，
//     按文件名在派生模块目录里找回真身。只认第一种的话，一句正确的论断会因为
//     省略了 "internal/" 就被整条丢弃（2026-10-19 生产里三条提交全这么死的）。
func (c *AnchorChecker) ownersOf(snap *Snapshot, tok string) []string {
	if c == nil || snap == nil {
		return nil
	}
	rel := normalizeProsePath(tok)
	if rel == "" || rel == "." {
		return nil
	}
	if dir := path.Dir(rel); dir != "." {
		if c.fileExists(dir) {
			// 父目录真实存在 → 这是"从根写全"的形态，上溯结果就是答案，不再猜后缀。
			if owner, ok := snap.ModuleOf(rel); ok {
				return []string{owner}
			}
			return nil
		}
		return c.filesByBase(snap)[path.Base(rel)]
	}
	return c.filesByBase(snap)[rel]
}

// existsUnderRoot 报告 token 是否指向仓库里的真东西（文件或目录）。
func (c *AnchorChecker) existsUnderRoot(tok string) bool {
	if c == nil {
		return false
	}
	rel := normalizeProsePath(tok)
	if rel == "" || rel == "." {
		return false
	}
	return c.fileExists(rel)
}

// filesByBase 建（并缓存）"文件名 → 覆盖它的片"索引。
// 只收派生模块目录**直属**的代码与文档文件名：递归子目录交给 ModuleOf 的上溯去管，
// 这里只管"模型把路径写短了"那一种形态。排序去重，保证 fold 是确定性纯函数（I20）。
func (c *AnchorChecker) filesByBase(snap *Snapshot) map[string][]string {
	if c.byBase != nil || snap == nil {
		return c.byBase
	}
	out := map[string][]string{}
	if c.byBaseSeen == nil {
		c.byBaseSeen = map[string]bool{}
	}
	for _, leaf := range snap.Leaves {
		if leaf.Submodule || c.byBaseSeen[leaf.Dir] {
			continue
		}
		c.byBaseSeen[leaf.Dir] = true
		entries, err := os.ReadDir(filepath.Join(snap.Root, filepath.FromSlash(leaf.Dir)))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			out[e.Name()] = append(out[e.Name()], leaf.Dir)
		}
	}
	for name, dirs := range out {
		sort.Strings(dirs)
		out[name] = dedupeSorted(dirs)
	}
	c.byBase = out
	return c.byBase
}

func dedupeSorted(in []string) []string {
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}
