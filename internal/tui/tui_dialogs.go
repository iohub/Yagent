package tui

import (
	"yagent/internal/messaging"

	tea "charm.land/bubbletea/v2"
)

// 求助与授权不再是对话框（docs/ask-panel-redesign.md）：它们是 footer 里的面板，
// 入队、渲染与应答分别住在 ask_state.go / ask_panel.go / ask_keys.go。
// 这个文件只剩下"publisher 什么时候就绪"那一件事。

// listenForPublisher waits for the publisher to become available via the channel.
func listenForPublisher(ch chan *messaging.MessagePublisher) tea.Cmd {
	return func() tea.Msg {
		publisher, ok := <-ch
		if !ok {
			return nil
		}
		return publisherReadyMsg{publisher: publisher}
	}
}
