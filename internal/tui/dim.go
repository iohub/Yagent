package tui

import (
	"strconv"
	"strings"
)

// 压暗背景的唯一内核：有待答求助/授权时把整屏其余部分推远一档，让面板浮上来。
//
// 为什么重写颜色而不是加 SGR 2（faint）：faint 由终端自行解释，Apple Terminal 根本不实现，
// 另有几款把"暗"做得轻到看不出来；而这里要的是可保证的"背景退下去"。代价是这一段必须懂
// ANSI，于是立下三条规矩：
// ① **只动前景** —— 背景色（48）与下划线色（58）是选中行/超链接的形状，压它就是把 UI 糊掉；
// ② **保持原编码族** —— 256 进 256 出、truecolor 进 truecolor 出、16 色进 16 色出，
//    这样 lipgloss 与 glamour 已经按终端能力选好的表示形式，不会被换成它不支持的那种；
// ③ **认不出来就原样返回** —— 任何解析不了的参数形态（含 ':' 子参数、未闭合序列）都整条抄走，
//    压暗失效远好过把控制序列写坏。
//
// 还有两个必须同时成立的前提：
// ④ 没上色的正文（默认字色）也得退下去。主题的真实默认前景拿不到，所以在每行行首、以及每一次
//    整段 reset 之后补一个压暗后的中性字色：深色主题按 #d0d0d0、浅色主题按 #303030 各拉
//    dimWeightPercent 都落在同一个灰阶附近，所以两个方向共用 dimNeutralGray 一个索引。
// ⑤ 压暗不改行数也不改列宽 —— 只替换序列、不增删可见字符（ANSI 不占格）。布局高度契约
//    （computeFooterHeight 与 View 逐行对应）因此不受影响，由 TestDim_NeverChangesCells 钉住。

const (
	// dimWeightPercent 是每个前景通道向终端底色拉的权重。
	dimWeightPercent = 45
	// dimNeutralGray 是压暗后的默认字色，见前提 ④。
	dimNeutralGray = 243

	// 两个方向的底色锚点：深色终端取 #121212（与对话框遮罩同一个值），浅色终端取 #e8e8e8。
	dimAnchorOnDarkBG  = 18
	dimAnchorOnLightBG = 232
)

// dimNeutralSeq 是"压暗后的默认字色"这条序列本身，行首与整段 reset 之后各补一次。
var dimNeutralSeq = "\x1b[38;5;" + strconv.Itoa(dimNeutralGray) + "m"

// xterm 256 立方区每档的通道值。
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

func (m *model) askDimActive() bool {
	return m.askPanelBlock() != ""
}

// dimBehindAsk 在判据为假时**逐字节原样返回**：没有待答求助的那一帧不该为这个特性多走一步。
func (m *model) dimBehindAsk(s string) string {
	if s == "" || !m.askDimActive() {
		return s
	}
	return dimAnsi(s, m.useDarkStyle)
}

// dimAnsi 压暗一整块已渲染文本。空行不写任何序列：那里没有字可压，
// 而多写一条序列就多一次"漏进下一块面板"的机会。
func dimAnsi(s string, dark bool) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s) + 16*strings.Count(s, "\n"))
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if line == "" {
			continue
		}
		b.WriteString(dimNeutralSeq)
		dimLine(&b, line, dark)
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// dimLine 抄写一行，途中把每一条 SGR 序列交给 dimSGR，其余字节原样落地。
func dimLine(b *strings.Builder, line string, dark bool) {
	for i := 0; i < len(line); {
		if line[i] != 0x1b {
			j := strings.IndexByte(line[i:], 0x1b)
			if j < 0 {
				b.WriteString(line[i:])
				return
			}
			b.WriteString(line[i : i+j])
			i += j
			continue
		}
		// OSC（超链接）、单字符 ESC 序列等等：逐字节原样走，绝不解析。
		if i+1 >= len(line) || line[i+1] != '[' {
			b.WriteByte(line[i])
			i++
			continue
		}
		j := i + 2
		for j < len(line) && line[j] >= 0x30 && line[j] <= 0x3f {
			j++
		}
		k := j
		for k < len(line) && line[k] >= 0x20 && line[k] <= 0x2f {
			k++
		}
		if k >= len(line) {
			b.WriteString(line[i:])
			return
		}
		if line[k] == 'm' {
			b.WriteString(dimSGR(line[i:k+1], dark))
		} else {
			b.WriteString(line[i : k+1])
		}
		i = k + 1
	}
}

// dimSGR 重写一条 SGR 序列（整条，含 "\x1b[" 与 'm'）。
func dimSGR(seq string, dark bool) string {
	params := seq[2 : len(seq)-1]
	if params == "" {
		return seq + dimNeutralSeq
	}
	if strings.ContainsRune(params, ':') {
		return seq
	}
	parts := strings.Split(params, ";")
	if dimIsPlainReset(parts) {
		return seq + dimNeutralSeq
	}

	out := make([]string, 0, len(parts)+4)
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return seq
		}
		switch {
		case n == 39:
			// "回到默认字色"就是前提 ④ 里那批没有上色的字。
			out = append(out, "38", "5", strconv.Itoa(dimNeutralGray))
		case n >= 90 && n <= 97:
			// 亮半区换成同色相的暗半区：这是主题自己的暗一档，不需要猜它的 RGB。
			out = append(out, strconv.Itoa(n-60))
		case n == 38:
			repl, ok := dimExtended(parts[i+1:], dark)
			if !ok {
				return seq
			}
			out = append(out, "38")
			out = append(out, repl...)
			i += len(repl)
		case n == 48, n == 58:
			skipped, ok := extendedArgCount(parts[i+1:])
			if !ok {
				return seq
			}
			out = append(out, parts[i:i+1+skipped]...)
			i += skipped
		default:
			out = append(out, parts[i])
		}
	}
	return "\x1b[" + strings.Join(out, ";") + "m"
}

// dimExtended 处理 "38;" 后面的真色形式，返回替换后的参数与被吃掉的段数。
func dimExtended(args []string, dark bool) ([]string, bool) {
	if _, ok := extendedArgCount(args); !ok {
		return nil, false
	}
	if args[0] == "5" {
		idx, err := strconv.Atoi(args[1])
		if err != nil || idx < 0 || idx > 255 {
			return nil, false
		}
		return []string{"5", strconv.Itoa(dimIndex256(idx, dark))}, true
	}
	r, err1 := strconv.Atoi(args[1])
	g, err2 := strconv.Atoi(args[2])
	b, err3 := strconv.Atoi(args[3])
	if err1 != nil || err2 != nil || err3 != nil || r > 255 || g > 255 || b > 255 {
		return nil, false
	}
	dr, dg, db := dimRGB(r, g, b, dark)
	return []string{"2", strconv.Itoa(dr), strconv.Itoa(dg), strconv.Itoa(db)}, true
}

// extendedArgCount 是 "5;N" 与 "2;r;g;b" 这两种扩展色各自的参数段数。
func extendedArgCount(args []string) (int, bool) {
	if len(args) >= 2 && args[0] == "5" {
		return 2, true
	}
	if len(args) >= 4 && args[0] == "2" {
		return 4, true
	}
	return 0, false
}

func dimIsPlainReset(parts []string) bool {
	for _, p := range parts {
		if p != "0" {
			return false
		}
	}
	return true
}

// dimIndex256 压暗一个 256 色索引，输入与输出留在同一段里：
// 0-7 是主题的暗半区（再往下就没有了）不动，8-15 退到 0-7，立方区与灰阶区各自回编码到自己那一档。
func dimIndex256(n int, dark bool) int {
	switch {
	case n < 8:
		return n
	case n < 16:
		return n - 8
	case n < 232:
		v := n - 16
		r, g, b := cubeLevels[v/36], cubeLevels[(v/6)%6], cubeLevels[v%6]
		dr, dg, db := dimRGB(r, g, b, dark)
		return 16 + nearestLevel(dr)*36 + nearestLevel(dg)*6 + nearestLevel(db)
	default:
		dg := dimValue(8+10*(n-232), dark)
		level := (dg - 8 + 5) / 10
		if level < 0 {
			level = 0
		}
		if level > 23 {
			level = 23
		}
		return 232 + level
	}
}

func nearestLevel(v int) int {
	best, bestErr := 0, abs(v-cubeLevels[0])
	for i, l := range cubeLevels {
		if e := abs(v - l); e < bestErr {
			bestErr, best = e, i
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func dimRGB(r, g, b int, dark bool) (int, int, int) {
	return dimValue(r, dark), dimValue(g, dark), dimValue(b, dark)
}

func dimValue(v int, dark bool) int {
	anchor := dimAnchorOnLightBG
	if dark {
		anchor = dimAnchorOnDarkBG
	}
	return v*(100-dimWeightPercent)/100 + anchor*dimWeightPercent/100
}
