package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// 全屏 timeline 的尺寸只有一个口径（internal/tui/AGENTS.md 硬约束 3）：
// renderTimelineFullscreenView / initTimelineDetailViewport / calcRightPaneWidth /
// WindowSizeMsg 分支全部从 computeTimelineLayout 取尺寸。
//
// 本文件刻意**不复制公式**：只钉不变量与实际行为。任何一处把手算的 0.35、
// termHeight-4-2 搬回来，下面的守恒等式或 resize 断言就会红。

// TestComputeTimelineLayout_Matrix 宽度 30..240 全量扫描 × 四种常见终端高度。
// 每条断言都是能由 termW/termH 推出的精确整数，没有"大于零"这类恒真条件。
func TestComputeTimelineLayout_Matrix(t *testing.T) {
	heights := []int{20, 24, 40, 60}

	for termW := 30; termW <= 240; termW++ {
		for _, termH := range heights {
			t.Run(fmt.Sprintf("w%d_h%d", termW, termH), func(t *testing.T) {
				layout := computeTimelineLayout(termW, termH)

				// 外壳口径：avail = max(termW-3, 1)；内容高 = max(termH-4, 3)。
				avail := termW - 3
				if avail < 1 {
					avail = 1
				}
				wantContentH := termH - 4
				if wantContentH < 3 {
					wantContentH = 3
				}

				if layout.TotalW != termW {
					t.Fatalf("TotalW = %d, want %d", layout.TotalW, termW)
				}
				if layout.ContentH != wantContentH {
					t.Fatalf("ContentH = %d, want %d（termH=%d 减外壳 4 行，floor 3）",
						layout.ContentH, wantContentH, termH)
				}

				// 不变量 1：左右分栏整数守恒（比例怎么调都不许破）。
				if got := layout.LeftW + layout.RightW; got != avail {
					t.Fatalf("LeftW+RightW = %d, want %d（avail=max(termW-3,1)=%d，LeftW=%d RightW=%d）",
						got, avail, avail, layout.LeftW, layout.RightW)
				}

				// 不变量 2：左列内部守恒 —— 瀑布列让位时整段归树。
				if layout.WaterfallW == 0 {
					if layout.TreeW != layout.LeftW {
						t.Fatalf("WaterfallW==0 时 TreeW = %d, want %d（== LeftW）",
							layout.TreeW, layout.LeftW)
					}
					if layout.Gap != 0 {
						t.Fatalf("WaterfallW==0 时 Gap = %d, want 0（让位就是整段让给树）", layout.Gap)
					}
				} else {
					if got := layout.TreeW + layout.Gap + layout.WaterfallW; got != layout.LeftW {
						t.Fatalf("TreeW+Gap+WaterfallW = %d, want %d（TreeW=%d Gap=%d WaterfallW=%d）",
							got, layout.LeftW, layout.TreeW, layout.Gap, layout.WaterfallW)
					}
					if layout.WaterfallW < timelineWaterfallMin || layout.WaterfallW > timelineWaterfallMax {
						t.Fatalf("WaterfallW = %d, want ∈[%d,%d]",
							layout.WaterfallW, timelineWaterfallMin, timelineWaterfallMax)
					}
				}
				if layout.Gap != 0 && layout.Gap != 1 {
					t.Fatalf("Gap = %d, want 0 或 1（列间只留一列发丝间隔）", layout.Gap)
				}

				// 不变量 3：所有字段非负。
				nonNegative := func(name string, val int) {
					t.Helper()
					if val < 0 {
						t.Fatalf("%s = %d, want >= 0", name, val)
					}
				}
				nonNegative("ContentH", layout.ContentH)
				nonNegative("LeftW", layout.LeftW)
				nonNegative("RightW", layout.RightW)
				nonNegative("TreeW", layout.TreeW)
				nonNegative("WaterfallW", layout.WaterfallW)
				nonNegative("Gap", layout.Gap)

				// 不变量 4：装得下两个 floor（左 24 + 右 30）时详情面板必须拿到 >= 30 列。
				if avail >= 54 && layout.RightW < 30 {
					t.Fatalf("avail = %d 时 RightW = %d, want >= 30（详情面板的 floor）", avail, layout.RightW)
				}
			})
		}
	}
}

// wantLayout 逐字段精确比对（位置参数：LeftW, RightW, TreeW, WaterfallW, Gap, ContentH）。
func wantLayout(t *testing.T, got timelineLayout, leftW, rightW, treeW, wfW, gap, contentH int) {
	t.Helper()
	if got.LeftW != leftW {
		t.Errorf("LeftW = %d, want %d", got.LeftW, leftW)
	}
	if got.RightW != rightW {
		t.Errorf("RightW = %d, want %d", got.RightW, rightW)
	}
	if got.TreeW != treeW {
		t.Errorf("TreeW = %d, want %d", got.TreeW, treeW)
	}
	if got.WaterfallW != wfW {
		t.Errorf("WaterfallW = %d, want %d", got.WaterfallW, wfW)
	}
	if got.Gap != gap {
		t.Errorf("Gap = %d, want %d", got.Gap, gap)
	}
	if got.ContentH != contentH {
		t.Errorf("ContentH = %d, want %d", got.ContentH, contentH)
	}
}

// TestComputeTimelineLayout_PinnedValues 上面那条是不变量（对任何比例都成立），
// 所以公式本身被悄悄改动（56% 改 60%、floor 改 20、瀑布上限改 40）它抓不到 ——
// 这里补三个精确数值，钉住当前策略。
func TestComputeTimelineLayout_PinnedValues(t *testing.T) {
	// 最窄：avail=27 → 左列撞 24 floor、右列只剩 3；瀑布列最小 16 列会把树挤到 7 列
	// （< 22），于是整段让给树（Gap=0、WaterfallW=0、TreeW==LeftW）。
	t.Run("narrow_30x20", func(t *testing.T) {
		wantLayout(t, computeTimelineLayout(30, 20), 24, 3, 24, 0, 0, 16)
	})

	// 常见 100x40：avail=97 → 左 56% = 54，右 43；瀑布列 54/3 = 18，树 54-1-18 = 35。
	t.Run("typical_100x40", func(t *testing.T) {
		wantLayout(t, computeTimelineLayout(100, 40), 54, 43, 35, 18, 1, 36)
	})

	// 宽屏 240x60：avail=237 → 左 132，右 105；瀑布列撞 30 上限，树 132-1-30 = 101。
	t.Run("wide_240x60", func(t *testing.T) {
		wantLayout(t, computeTimelineLayout(240, 60), 132, 105, 101, 30, 1, 56)
	})
}

// TestTimelineResizeTakesSizesFromLayout resize 分支（WindowSizeMsg）必须把
// computeTimelineLayout 的结果写进详情 viewport：宽度与 initTimelineDetailViewport
// 同源（calcRightPaneWidth），高度等于 ContentH。
//
// 改造前这里是 int((termWidth-3)*0.35) + 夹 25、termHeight-4-2 + 夹 3：
// 每次 resize 详情面板都比视图少两行，左右分栏比例还和视图的 56% 对不上 ——
// 这条断言就是那个缺陷的闸门（改回 0.35 时 viewport 宽会与 calcRightPaneWidth 不等）。
func TestTimelineResizeTakesSizesFromLayout(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{40, 20}, {80, 24}, {100, 40}, {120, 40}, {240, 60}} {
		t.Run(fmt.Sprintf("w%d_h%d", sz.w, sz.h), func(t *testing.T) {
			m := newTestModel() // 初始尺寸 100x30
			m.timelineFullscreenMode = true
			m.timelineEntries = []*TimelineEntry{{
				ID: "call_1", Kind: TimelineKindTool, Name: "read_file", Status: ToolStatusSuccess,
			}}
			// 真实路径：进全屏时先建 viewport，之后才可能收到 resize。
			initTimelineDetailViewport(m)
			if m.timelineDetailVP == nil {
				t.Fatal("initTimelineDetailViewport 没有建出详情 viewport")
			}

			updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
			nm, ok := updated.(*model)
			if !ok {
				t.Fatalf("Update 返回的模型类型 = %T, want *model", updated)
			}

			want := computeTimelineLayout(sz.w, sz.h)
			if got, wantW := nm.timelineDetailVP.Width(), calcRightPaneWidth(nm); got != wantW {
				t.Errorf("resize 后 viewport 宽 = %d, want %d（calcRightPaneWidth，与 init 同源）", got, wantW)
			}
			if got := nm.timelineDetailVP.Height(); got != want.ContentH {
				t.Errorf("resize 后 viewport 高 = %d, want %d（computeTimelineLayout.ContentH）",
					got, want.ContentH)
			}
		})
	}
}

// TestTimelineFullscreenAlwaysAltScreen 全屏 timeline 是覆盖层：只要它接管屏幕，
// 返回的 View 就必须是 AltScreen（AltScreen 由 true 翻转成 false 会让渲染器退出备用屏，
// 见 tui_resume_list.go:210 的说明）。零尺寸那一帧（等第一条 WindowSizeMsg）同样不许退化。
func TestTimelineFullscreenAlwaysAltScreen(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{0, 0}, {30, 20}, {80, 24}, {120, 40}, {240, 60}} {
		t.Run(fmt.Sprintf("w%d_h%d", sz.w, sz.h), func(t *testing.T) {
			m := newTestModel()
			m.termWidth, m.termHeight = sz.w, sz.h
			m.timelineFullscreenMode = true
			m.timelineEntries = []*TimelineEntry{{
				ID: "call_1", Kind: TimelineKindTool, Name: "read_file", Status: ToolStatusSuccess,
			}}

			v := renderTimelineFullscreenView(m)
			if !v.AltScreen {
				t.Fatalf("renderTimelineFullscreenView 返回 AltScreen=false（termW=%d termH=%d）",
					sz.w, sz.h)
			}
			// 有尺寸时必须真的画出内容；零尺寸那帧允许空内容。
			if sz.w > 0 && sz.h > 0 && v.Content == "" {
				t.Fatalf("AltScreen 视图内容为空（termW=%d termH=%d）", sz.w, sz.h)
			}
		})
	}
}
