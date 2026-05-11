package layout

import "sync"

// Region 定义布局区域
type Region struct {
	X, Y    int
	Width, Height int
}

// IsEmpty 检查区域是否有效
func (r Region) IsEmpty() bool {
	return r.Width <= 0 || r.Height <= 0
}

// Area 返回区域面积
func (r Region) Area() int {
	return r.Width * r.Height
}

// LayoutEngine 根据终端尺寸动态计算布局区域
type LayoutEngine struct {
	width, height int
	compact       bool
	regions       map[string]Region
	mu            sync.RWMutex
}

// 区域 ID 常量
const (
	RegionHeader   = "header"
	RegionMain     = "main"
	RegionEditor   = "editor"
	RegionStatus   = "status"
	RegionDialog   = "dialog"
	RegionSidebar  = "sidebar"
)

// 紧凑模式阈值
const (
	CompactWidthBreakpoint  = 120
	CompactHeightBreakpoint = 30
)

// NewLayoutEngine 创建新的布局引擎
func NewLayoutEngine() *LayoutEngine {
	return &LayoutEngine{
		regions: make(map[string]Region),
	}
}

// Resize 重新计算布局（在 WindowSizeMsg 时调用）
func (l *LayoutEngine) Resize(width, height int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.width = width
	l.height = height

	// 检测紧凑模式
	l.compact = width < CompactWidthBreakpoint || height < CompactHeightBreakpoint

	// 计算各区域
	l.computeRegions()
}

// computeRegions 计算所有区域尺寸
func (l *LayoutEngine) computeRegions() {
	w, h := l.width, l.height
	if w <= 0 || h <= 0 {
		return
	}

	// header: 顶部标题栏 (2 行)
	l.regions[RegionHeader] = Region{
		X: 0, Y: 0,
		Width: w, Height: 2,
	}

	// status: 底部状态栏 (1 行)
	l.regions[RegionStatus] = Region{
		X: 0, Y: h - 1,
		Width: w, Height: 1,
	}

	// sidebar: 右侧边栏 (紧凑模式下隐藏)
	if !l.compact {
		sidebarWidth := 30
		if w > sidebarWidth+10 {
			sidebarY := 2 // 从 header 下方开始
			sidebarH := h - 3 // 减去 header 和 status
			l.regions[RegionSidebar] = Region{
				X: w - sidebarWidth,
				Y: sidebarY,
				Width: sidebarWidth,
				Height: sidebarH,
			}
			// main 区域排除 sidebar
			mainW := w - sidebarWidth
			l.regions[RegionMain] = Region{
				X: 0, Y: 2,
				Width: mainW,
				Height: sidebarH,
			}
		} else {
			// sidebar 太窄则隐藏
			delete(l.regions, RegionSidebar)
			l.regions[RegionMain] = Region{
				X: 0, Y: 2,
				Width: w,
				Height: h - 3,
			}
		}
	} else {
		// 紧凑模式：无 sidebar，main 占满
		l.regions[RegionMain] = Region{
			X: 0, Y: 2,
			Width: w,
			Height: h - 3,
		}
	}

	// editor: 输入框区域 (在 main 和 status 之间，约 3 行)
	// 实际高度由主 model 根据 textarea 内容调整
	editorY := h - 4
	l.regions[RegionEditor] = Region{
		X: 0, Y: editorY,
		Width: w,
		Height: 2,
	}

	// 重新计算 main 高度（排除 editor）
	main := l.regions[RegionMain]
	main.Height = editorY - 2
	l.regions[RegionMain] = main
}

// Region 获取指定区域
func (l *LayoutEngine) Region(id string) Region {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.regions[id]
}

// AllRegions 获取所有区域
func (l *LayoutEngine) AllRegions() map[string]Region {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// 返回副本
	result := make(map[string]Region, len(l.regions))
	for k, v := range l.regions {
		result[k] = v
	}
	return result
}

// Width 返回终端宽度
func (l *LayoutEngine) Width() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.width
}

// Height 返回终端高度
func (l *LayoutEngine) Height() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.height
}

// IsCompact 返回是否为紧凑模式
func (l *LayoutEngine) IsCompact() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.compact
}

// SetCompact 手动设置紧凑模式（-1 切换，true/false 直接设置）
func (l *LayoutEngine) SetCompact(compact bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.compact = compact
	l.computeRegions()
}

// ToggleCompact 切换紧凑模式
func (l *LayoutEngine) ToggleCompact() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.compact = !l.compact
	l.computeRegions()
}
