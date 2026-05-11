package anim

import (
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Animation 表示一个动画实例
type Animation struct {
	ID       string    // 唯一标识符
	FPS      int       // 帧率
	Frame    int       // 当前帧（从 0 开始）
	Visible  bool      // 是否可见（在视口中）
	lastTick time.Time // 上次 tick 时间（运行时设置）
}

// Manager 管理所有动画实例，支持可见性感知（不可见时暂停）
type Manager struct {
	animations map[string]*Animation
	mu         sync.RWMutex
}

// NewManager 创建新的动画管理器
func NewManager() *Manager {
	return &Manager{
		animations: make(map[string]*Animation),
	}
}

// Register 注册一个新动画
func (m *Manager) Register(id string, fps int) *Animation {
	m.mu.Lock()
	defer m.mu.Unlock()

	anim := &Animation{
		ID:      id,
		FPS:     fps,
		Frame:   0,
		Visible: true,
	}
	m.animations[id] = anim
	return anim
}

// Unregister 移除一个动画
func (m *Manager) Unregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.animations, id)
}

// GetAnimation 获取指定动画
func (m *Manager) GetAnimation(id string) *Animation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.animations[id]
}

// GetFrame 获取指定动画的当前帧
func (m *Manager) GetFrame(id string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if anim, ok := m.animations[id]; ok {
		return anim.Frame
	}
	return 0
}

// SetVisible 设置动画的可见性
func (m *Manager) SetVisible(id string, visible bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if anim, ok := m.animations[id]; ok {
		anim.Visible = visible
		if visible {
			// 可见时重置 lastTick，避免跳跃
			anim.lastTick = time.Time{}
		}
	}
}

// Tick 推进所有可见动画的帧
// delta 是上次 Tick 以来的毫秒数
func (m *Manager) Tick(delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, anim := range m.animations {
		if !anim.Visible {
			continue // 不可见的动画不推进
		}

		interval := int64(1000 / anim.FPS)
		if delta >= interval {
			anim.Frame++
			anim.lastTick = time.Now()
		}
	}
}

// TickWithCmd 返回一个 tea.Cmd，定期发送 tickMsg 触发帧推进
// 调用方需要在 Update 中处理 tickMsg 并调用 Tick()
func (m *Manager) TickWithCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
		m.mu.Lock()
		visibleCount := 0
		for _, anim := range m.animations {
			if anim.Visible {
				visibleCount++
				break // 只要有可见动画就足够了
			}
		}
		m.mu.Unlock()

		if visibleCount > 0 {
			return tickMsg{Time: t}
		}
		return nil
	})
}

// tickMsg 内部消息类型，由 TickWithCmd 触发
type tickMsg struct {
	Time time.Time
}

// ProcessTickMsg 处理 tickMsg，计算 delta 并推进动画帧
// 调用方在 Update 中：
//
//	case anim.tickMsg:
//	    m.animManager.ProcessTickMsg(msg)
func (m *Manager) ProcessTickMsg(msg tickMsg) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, anim := range m.animations {
		if !anim.Visible {
			continue
		}

		var delta int64
		if anim.lastTick.IsZero() {
			// 首次 tick，不推进帧，仅记录时间
			anim.lastTick = msg.Time
			continue
		}

		delta = msg.Time.Sub(anim.lastTick).Milliseconds()
		interval := int64(1000 / anim.FPS)

		if delta >= interval {
			// 累 excess 帧数，避免追赶过多
			framesToAdvance := delta / interval
			anim.Frame += int(framesToAdvance)
			excess := delta % interval
			anim.lastTick = anim.lastTick.Add(time.Duration(excess) * time.Millisecond)
		}
	}
}

// ResetFrame 重置指定动画的帧
func (m *Manager) ResetFrame(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if anim, ok := m.animations[id]; ok {
		anim.Frame = 0
	}
}

// ResetAllFrames 重置所有动画的帧
func (m *Manager) ResetAllFrames() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, anim := range m.animations {
		anim.Frame = 0
	}
}
