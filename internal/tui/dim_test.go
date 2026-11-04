package tui

import (
	"strings"
	"testing"
)

// 压暗内核的闸门。每条都对应 dim.go 头注里的一条规矩，删掉那条规矩就红。

const esc = "\x1b["

// TestDim_SGRTable 钉住"只动前景 + 保持原编码族 + 认不出来就原样"。
// 期望值全是按 dimValue(v)=v*55/100+8 与两套 256 调色板手算的，
// 不是跑出来抄的 —— 抄实现结果就等于把 bug 写成规格。
func TestDim_SGRTable(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"灰阶 250 退到同一档里更暗的一格", esc + "38;5;250m", esc + "38;5;242m"},
		{"灰阶 245 仍留在灰阶区", esc + "38;5;245m", esc + "38;5;240m"},
		{"灰阶顶格 255 不落进立方区", esc + "38;5;255m", esc + "38;5;245m"},
		{"立方区白 231 仍输出 38;5", esc + "38;5;231m", esc + "38;5;102m"},
		{"立方区橙 214 退成同色相暗橙", esc + "38;5;214m", esc + "38;5;94m"},
		{"truecolor 保持 truecolor", esc + "38;2;255;255;255m", esc + "38;2;148;148;148m"},
		{"亮 16 退到同色相的暗半区", esc + "94m", esc + "34m"},
		{"亮白退到暗白", esc + "97m", esc + "37m"},
		{"暗半区没有更暗的一格，不动", esc + "31m", esc + "31m"},
		{"默认字色换成压暗后的中性字色", esc + "39m", esc + "38;5;243m"},
		{"整段 reset 之后必须补回默认字色", esc + "0m", esc + "0m" + esc + "38;5;243m"},
		{"空参数的 reset 同样补", esc + "m", esc + "m" + esc + "38;5;243m"},
		{"粗体保留、前景照压", esc + "1;38;5;250m", esc + "1;38;5;242m"},
		{"背景色不碰", esc + "48;5;220m", esc + "48;5;220m"},
		{"背景与前景同一条：只换前景", esc + "48;5;220;38;5;250m", esc + "48;5;220;38;5;242m"},
		{"下划线色不碰", esc + "58;5;250m", esc + "58;5;250m"},
		{"反显不碰", esc + "7m", esc + "7m"},
		{"带 : 子参数的整条原样", esc + "38;2;255:255:255m", esc + "38;2;255:255:255m"},
		{"认不出的 38 形态整条原样", esc + "38;7m", esc + "38;7m"},
		{"38 被截断（参数不够）整条原样", esc + "38;5m", esc + "38;5m"},
		{"非数字参数整条原样", esc + "38;5;abcm", esc + "38;5;abcm"},
	}
	for _, c := range cases {
		if got := dimSGR(c.in, true); got != c.want {
			t.Errorf("%s\ndimSGR(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestDim_LightAnchor 钉住浅色终端往**亮**的方向压：混向 #e8e8e8，而不是把深色底的那套
// 锚点硬套上来（那会让浅色终端的字反而更黑、更抢）。
func TestDim_LightAnchor(t *testing.T) {
	if got, want := dimSGR(esc+"38;2;0;0;0m", false), esc+"38;2;104;104;104m"; got != want {
		t.Errorf("浅色锚点 dimSGR = %q, want %q", got, want)
	}
	if got, want := dimSGR(esc+"38;2;0;0;0m", true), esc+"38;2;8;8;8m"; got != want {
		t.Errorf("深色锚点 dimSGR = %q, want %q", got, want)
	}
}

// TestDim_KeepsForeignBytes 钉住规矩 ③：不是 SGR 的控制序列一律逐字节原样。
// 这些序列写坏一条的后果不是"不暗"，是终端状态错乱（超链接把后面的字吞掉、光标序列被当颜色）。
func TestDim_KeepsForeignBytes(t *testing.T) {
	for _, s := range []string{
		"\x1b[?25l",                     // 私有 CSI：隐藏光标
		"\x1b[1000D",                     // 退 1000 列
		"\x1b]8;;https://example.com\x07", // OSC 8 超链接
		"\x1bM",                          // 单字符 ESC 序列（反向换行）
		"\x1b[38;5",                      // 未闭合：剩下的必须原样落地
	} {
		if got := dimSGRKeepAll(s); got != s {
			t.Errorf("非 SGR 序列被改写: %q -> %q", s, got)
		}
	}
}

// dimSGRKeepAll 走完整的行扫描（不是直接调 dimSGR），因为要测的是"扫描器会不会动它"。
// 减掉 dimAnsi 在行首/行尾加的那两条固定序列。
func dimSGRKeepAll(s string) string {
	out := dimAnsi(s, true)
	out = strings.TrimPrefix(out, dimNeutralSeq)
	if len(s) > 0 {
		out = strings.TrimSuffix(out, "\x1b[0m")
	}
	return out
}

// TestDim_LineContract 钉住行级形态：每行前置压暗后的默认字色、后置 reset，
// 空行一个字节都不写（那里没字可压，多一条序列就多一次漏进下一块面板的机会）。
func TestDim_LineContract(t *testing.T) {
	got := dimAnsi("a\n\nb", true)
	want := dimNeutralSeq + "a\x1b[0m\n\n" + dimNeutralSeq + "b\x1b[0m"
	if got != want {
		t.Errorf("行级形态\ngot  = %q\nwant = %q", got, want)
	}
	if dimAnsi("", true) != "" {
		t.Error("空串必须原样返回，不然面板高度会多出一块")
	}
}

// TestDim_NeverBrightensAndKeepsFamily 对 256 个索引逐条扫：输出必须留在同一段里
// （16 色的亮半区退到暗半区是唯一的跨段，那是主题自己的暗一档），
// 压暗后不得比输入更接近白色，而且**必须真的压暗了一部分** ——
// 一条都不变的表就是恒真的装饰。
func TestDim_NeverBrightensAndKeepsFamily(t *testing.T) {
	changed := 0
	for n := 0; n < 256; n++ {
		got := dimIndex256(n, true)
		if got < 0 || got > 255 {
			t.Fatalf("n=%d 压出调色板: %d", n, got)
		}
		switch band(n) {
		case bandBright16:
			if got != n-8 {
				t.Errorf("n=%d 亮半区应退到同色相暗半区 %d，实得 %d", n, n-8, got)
			}
		case bandDark16:
			if band(got) != bandDark16 {
				t.Errorf("n=%d 暗半区不该跳段，实得 %d", n, got)
			}
		default:
			if band(got) != band(n) {
				t.Errorf("n=%d 应从 %d 段跳到同一段，实得 %d", n, band(n), got)
			}
			if out, in := channelOf(got), channelOf(n); out > in {
				t.Errorf("n=%d -> %d 压暗后反而更亮（%d > %d）", n, got, out, in)
			}
		}
		if got != n {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("256 个索引一个都没变，这条断言恒真")
	}
	if changed < 100 {
		t.Errorf("只压暗了 %d 个索引，多半是权重或调色板接错了", changed)
	}
}

const (
	bandDark16 = iota
	bandBright16
	bandCube
	bandGray
)

func band(n int) int {
	switch {
	case n < 8:
		return bandDark16
	case n < 16:
		return bandBright16
	case n < 232:
		return bandCube
	default:
		return bandGray
	}
}

// channelOf 返回索引的代表亮度：灰阶取通道值，立方取三档平均（只用于比较亮度方向）。
func channelOf(n int) int {
	switch band(n) {
	case bandCube:
		v := n - 16
		r, g, b := cubeLevels[v/36], cubeLevels[(v/6)%6], cubeLevels[v%6]
		return (r + g + b) / 3
	case bandGray:
		return 8 + 10*(n-232)
	default:
		return 0
	}
}
