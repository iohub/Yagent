package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"codeactor/internal/datamanager"
)

// ──────────────────── 消息类型 ────────────────────

// historyLoadedMsg 历史数据加载完成
type historyLoadedMsg struct {
	items  []datamanager.TaskHistoryItem
	total  int
	offset int
	err    error
}

// historyDeletedMsg 删除完成
type historyDeletedMsg struct {
	taskID string
	err    error
}

// continueWithTaskMsg 继续对话
type continueWithTaskMsg struct {
	taskID string
}

// ──────────────────── HistoryPanel ────────────────────

// HistoryPanel 封装所有历史面板状态
type HistoryPanel struct {
	list     list.Model
	loading  bool
	hasMore  bool
	offset   int
	pageSize int
	ctx      context.Context
	cancel   context.CancelFunc
	active   bool
}

// NewHistoryPanel 创建新的历史面板
func NewHistoryPanel(width, height int) *HistoryPanel {
	ctx, cancel := context.WithCancel(context.Background())

	delegate := newHistoryDelegate()
	l := list.New([]list.Item{}, delegate, width, height-2)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)          // 关键：禁用过滤
	l.SetFilteringEnabled(false)     // 关键：禁用过滤
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()

	// 自定义按键：只保留上下移动，移除默认的 enter/filter
	l.KeyMap.CursorUp = list.DefaultKeyMap().CursorUp
	l.KeyMap.CursorDown = list.DefaultKeyMap().CursorDown
	// 清空其他按键绑定，由我们在 Update 中统一处理
	l.KeyMap.NextPage = list.DefaultKeyMap().NextPage
	l.KeyMap.PrevPage = list.DefaultKeyMap().PrevPage
	l.KeyMap.GoToStart = list.DefaultKeyMap().GoToStart
	l.KeyMap.GoToEnd = list.DefaultKeyMap().GoToEnd

	return &HistoryPanel{
		list:     l,
		pageSize: 20,
		loading:  true,
		hasMore:  true,
		offset:   0,
		ctx:      ctx,
		cancel:   cancel,
		active:   true,
	}
}

// Close 释放资源
func (hp *HistoryPanel) Close() {
	if hp.cancel != nil {
		hp.cancel()
		hp.cancel = nil
	}
	hp.active = false
	hp.list = list.Model{}
	hp.ctx = nil
}

// SetSize 更新面板尺寸
func (hp *HistoryPanel) SetSize(width, height int) {
	hp.list.SetSize(width, height-2)
}

// SelectedItem 返回当前选中项
func (hp *HistoryPanel) SelectedItem() (datamanager.TaskHistoryItem, bool) {
	if hp.list.SelectedItem() == nil {
		return datamanager.TaskHistoryItem{}, false
	}
	item, ok := hp.list.SelectedItem().(historyItem)
	if !ok {
		return datamanager.TaskHistoryItem{}, false
	}
	return datamanager.TaskHistoryItem(item), true
}

// ──────────────────── list.Item 适配 ────────────────────

// historyItem 包装 TaskHistoryItem 以实现 list.Item
type historyItem datamanager.TaskHistoryItem

func (i historyItem) FilterValue() string {
	return i.Title
}

// ──────────────────── Delegate ────────────────────

type historyDelegate struct {
	styles historyItemStyles
}

type historyItemStyles struct {
	normal   lipgloss.Style
	selected lipgloss.Style
	date     lipgloss.Style
	title    lipgloss.Style
	count    lipgloss.Style
	dot      string
	dotSel   string
}

func newHistoryDelegate() historyDelegate {
	selBg := lipgloss.Color("39") // 蓝色
	d := historyDelegate{
		styles: historyItemStyles{
			normal:   lipgloss.NewStyle().Padding(0, 1).Width(0),
			selected: lipgloss.NewStyle().Padding(0, 1).Background(selBg).Width(0),
			date:     lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Width(11),
			title:    lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Padding(0, 1),
			count:    lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Width(5),
			dot:      lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(" "),
			dotSel:   lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(selBg).Render("▸"),
		},
	}
	return d
}

func (d historyDelegate) Height() int                     { return 1 }
func (d historyDelegate) Spacing() int                    { return 0 }
func (d historyDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }

func (d historyDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	h, ok := item.(historyItem)
	if !ok {
		return
	}

	dateStr := h.CreatedAt.Format("01-02 15:04")
	title := truncateTitle(h.Title, 40)
	countStr := ""
	if h.MessageCount > 0 {
		countStr = fmt.Sprintf("%d💬", h.MessageCount)
	}

	s := d.styles
	line := lipgloss.JoinHorizontal(lipgloss.Left,
		s.date.Render(dateStr),
		s.title.Render(title),
		s.count.Render(countStr),
	)

	if index == m.Index() {
		fmt.Fprint(w, s.selected.Width(m.Width()).Render(s.dotSel + line))
	} else {
		fmt.Fprint(w, s.normal.Width(m.Width()).Render(s.dot + line))
	}
}

// ──────────────────── 渲染 ────────────────────

// 面板样式（在 View 中使用）
var (
	hpTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Padding(0, 1)
	hpFooterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 1)
	hpEmptyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Padding(1, 1).Italic(true)
)

// Render 渲染完整历史面板
func (hp *HistoryPanel) Render() string {
	if hp.list.Items() == nil || len(hp.list.Items()) == 0 {
		if hp.loading {
			return lipgloss.JoinVertical(lipgloss.Left,
				hpTitleStyle.Render("📜 历史会话"),
				lipgloss.NewStyle().Padding(1, 1).Foreground(lipgloss.Color("243")).Render("⏳ 加载中..."),
				hpFooterStyle.Render("ESC 关闭"),
			)
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			hpTitleStyle.Render("📜 历史会话"),
			hpEmptyStyle.Render("暂无历史会话"),
			hpFooterStyle.Render("ESC 关闭"),
		)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		hpTitleStyle.Render("📜 历史会话"),
		hp.list.View(),
		hpFooterStyle.Render("↑↓ 选择  Enter 继续  D 删除  ESC 关闭"),
	)
}

// ──────────────────── 命令 ────────────────────

// LoadHistoryCmd 加载历史数据
func LoadHistoryCmd(dm *datamanager.DataManager, ctx context.Context, offset, limit int) tea.Cmd {
	return func() tea.Msg {
		items, err := dm.ListTaskHistoryFast(200)
		if err != nil {
			return historyLoadedMsg{err: err}
		}

		total := len(items)
		// 手动分页
		start := offset
		if start > total {
			start = total
		}
		end := start + limit
		if end > total {
			end = total
		}
		page := items[start:end]

		return historyLoadedMsg{
			items:  page,
			total:  total,
			offset: offset,
			err:    nil,
		}
	}
}

// DeleteHistoryCmd 删除历史项
func DeleteHistoryCmd(dm *datamanager.DataManager, ctx context.Context, taskID string) tea.Cmd {
	return func() tea.Msg {
		err := dm.DeleteTaskMemory(taskID)
		return historyDeletedMsg{taskID: taskID, err: err}
	}
}

// ContinueConversationCmd 继续对话
func ContinueConversationCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		return continueWithTaskMsg{taskID: taskID}
	}
}

// ──────────────────── 辅助函数 ────────────────────

func truncateTitle(s string, max int) string {
	s = strings.TrimSpace(s)
	// 移除换行符
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
