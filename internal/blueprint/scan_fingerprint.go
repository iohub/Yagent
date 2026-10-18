package blueprint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// fingerprint 计算 §5.2(a) 的结构指纹：sha256(排序后的文件列表 + 语言直方图 + 出边 + 入边)。
//
// edgesOut/edgesIn 用全量（未截断）数据 —— 显示截断只影响 Leaf 的字段，不影响哈希：
// 否则第 25 条边加入不会改变指纹，指纹就失效了。
// 字节布局是本实现的规范性选择（§5.2(a) 只规定了成分）：每行一个事实，
// f/l/o/i 四段各自排序后拼接 —— 任何成分变化都会改变哈希，两次扫描逐位相同（确定性）。
func fingerprint(files []string, langs map[string]int, edgesOut, edgesIn map[string]int) string {
	var b strings.Builder
	for _, f := range sortedCopy(files) {
		fmt.Fprintf(&b, "f\t%s\n", f)
	}
	for _, kv := range sortedCounts(langs) {
		fmt.Fprintf(&b, "l\t%s\t%d\n", kv.k, kv.v)
	}
	for _, kv := range sortedEdges(edgesOut) {
		fmt.Fprintf(&b, "o\t%s\t%d\n", kv.k, kv.v)
	}
	for _, kv := range sortedEdges(edgesIn) {
		fmt.Fprintf(&b, "i\t%s\t%d\n", kv.k, kv.v)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

type kv struct {
	k string
	v int
}

func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

func sortedCounts(m map[string]int) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].k < out[j].k })
	return out
}

func sortedEdges(m map[string]int) []kv {
	out := make([]kv, 0, len(m))
	for to, n := range m {
		out = append(out, kv{to, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].k < out[j].k })
	return out
}
