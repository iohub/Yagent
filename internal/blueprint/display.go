package blueprint

import (
	"strings"
)

// 本文件是渲染层的共用显示原语（形状②的索引行与按需视图都要用）。
//
// 拆出来不是为了"抽象"，是因为 §15 的棘轮把新文件钉在 300 行：
// 索引渲染器自己就该只讲"怎么排一段索引"这一件事。
// 同形显示逻辑在本仓有过教训（坑 18 / 33：两份同形实现只有一边会漂走），
// 所以这里只留一份，index.go 与 prose.go 都调它。

// fallbackCount 是 token 计数的降级口径，与 tokenutil 内部那条一致（§6.1 注明它是**下限**）。
// 调用方没注入计数器时用它，为的是本包保持 leaf（不 import tokenutil）。
func fallbackCount(s string) int { return len(s) / 4 }

// edgeNames 把边列表渲染成相对名（§4.3：边必须用相对路径 —— `->agents` 里的
// agents 可以是任何一层，绝对片路径会把每行撑出预算）。
//
// outbound 决定"对端"是哪一头：出边的对端是 To，入边的对端是 From。
// 把两头搞混的代价是入边渲染成 `<-self` —— 一个没有任何信息量的词。
func edgeNames(edges []Edge, show int, outbound bool) string {
	if len(edges) == 0 {
		return ""
	}
	if len(edges) > show {
		edges = edges[:show]
	}
	names := make([]string, 0, len(edges))
	for _, e := range edges {
		self, other := e.From, e.To
		if !outbound {
			self, other = e.To, e.From
		}
		names = append(names, relEdgeName(self, other))
	}
	return strings.Join(names, ",")
}

// shownCount 是"这一批里真正渲染了几条"，用于把没渲染的那部分算进 `+N`（I3/I4）。
func shownCount(n, show int) int {
	if n > show {
		return show
	}
	return n
}

// relEdgeName 剥掉 from/to 的最长公共目录前缀。
// internal/blueprint -> internal/agents 渲染成 `agents`；跨层时保留剩余路径
// （internal/tools -> internal/agents/director 渲染成 `agents/director`，
// 因为只取末段会让两个不同的 director 撞名）。
func relEdgeName(from, to string) string {
	if to == "." {
		return "."
	}
	if from == to {
		return "self"
	}
	fs, ts := strings.Split(from, "/"), strings.Split(to, "/")
	i := 0
	for i < len(fs) && i < len(ts) && fs[i] == ts[i] {
		i++
	}
	if rest := strings.Join(ts[i:], "/"); rest != "" {
		return rest
	}
	return to
}
