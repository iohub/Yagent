package tui

import (
	"context"
	"strings"
	"time"

	"yagent/internal/app"
	"yagent/internal/datamanager"
	"yagent/internal/http"
	"yagent/internal/logging"
	"yagent/internal/memory"
	"yagent/internal/messaging"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
)

// submitFromEditor 提交输入框内容（alt+s / ctrl+s 共用）。
// ensureSessionID 保证本次提交有一块确定的便签板。
//
// 需要它是因为 tab 的 sessionID 只写在 sessionTab 上，而首个 tab 是在初始化里造的、
// 不经过 restoreSessionTab —— 于是 m.sessionID 会是空串，Board 只能退回 taskID，
// 同一会话内"开第二个任务"就看不见第一个任务的结论。空串在这里补一个 id，
// 之后由 tab 的 save/restore 接管。
func (m *model) ensureSessionID() {
	if m.sessionID == "" {
		m.sessionID = uuid.New().String()
	}
}

// 运行中静默忽略：此时输入框仍然可打字，用于排队写下一条指令。
func (m *model) submitFromEditor() tea.Cmd {
	if m.taskRunning {
		return nil
	}
	taskDesc := strings.TrimSpace(m.input.Value())
	if taskDesc == "" {
		return nil
	}
	if ok, errStr := validateInputs(m.projectDir, taskDesc); !ok {
		m.errMsg = errStr
		return nil
	}
	m.ensureSessionID()
	if m.currentTask != nil {
		return m.submitFollowUp(taskDesc)
	}
	return m.submitTask()
}

func (m *model) submitTask() tea.Cmd {
	return m.submitTaskWithContent(m.input.Value())
}

// submitTaskWithContent 使用指定的任务描述提交任务
func (m *model) submitTaskWithContent(taskDesc string) tea.Cmd {
	taskDesc = strings.TrimSpace(taskDesc)

	// 验证输入
	if valid, errMsg := validateInputs(m.projectDir, taskDesc); !valid {
		m.errMsg = errMsg
		return nil
	}

	m.taskRunning = true
	m.taskStarted = true
	m.commandMode = true
	m.errMsg = ""
	// 新一轮开始：RUN 列归零，Σ 与每 agent 的 TOTAL 保留
	m.resetRunTokens()
	// Task started — update status bar cache
	m.cachedStatusBar = m.renderAirlineStatusBar()
	m.statusBarValid = true

	ctx, cancel := context.WithCancel(context.Background())
	task := &http.Task{
		ID:         uuid.New().String(),
		Status:     http.TaskStatusRunning,
		ProjectDir: m.projectDir,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Memory:     memory.NewConversationMemory(300),
		Context:    ctx,
		CancelFunc: cancel,
	}
	m.taskManager.AddTask(task)
	m.currentTask = task
	m.resumedFromRollout = false // 全新任务，清除恢复会话遗留的续跑标记

	// 更新 tab 标题（如果仍是默认标题）
	m.updateActiveTabTitle(taskDesc)

	// Add submission entry
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "user_message",
		content:   taskDesc,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	m.publisherCh = make(chan *messaging.MessagePublisher, 1)
	return tea.Batch(
		executeTaskCmd(taskDesc, task, m.sessionID, m.assistant, m.taskManager, m.dataManager, m.eventCh, m.publisherCh),
		listenForEvents(m.eventCh),
		listenForPublisher(m.publisherCh),
		m.ensureTick(),
	)
}

// submitFollowUp sends a follow-up message to an existing task.
func (m *model) submitFollowUp(message string) tea.Cmd {
	// 除 submitFromEditor 之外还有直接调用本函数的入口（恢复会话后的续跑），
	// 所以兜底也要在这里过一次。
	m.ensureSessionID()

	m.input.SetValue("")
	m.taskRunning = true
	m.taskStarted = true
	m.commandMode = true
	m.errMsg = ""
	// 追问同样是"新一轮"：RUN 列归零，Σ 与每 agent 的 TOTAL 保留
	m.resetRunTokens()
	// Task started — update status bar cache
	m.cachedStatusBar = m.renderAirlineStatusBar()
	m.statusBarValid = true

	// 消费一次性续跑标记：恢复会话（/resume、history 加载）后的首次追问按 Resumed
	// 提交，Director 续跑首轮强制注入项目上下文；只消费一次，与 FirstConversation
	// "仅首次注入"的语义对齐。
	resumed := m.resumedFromRollout
	m.resumedFromRollout = false

	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "user_message",
		content:   message,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	// 更新 tab 标题（如果仍是默认标题）
	m.updateActiveTabTitle(message)

	m.publisherCh = make(chan *messaging.MessagePublisher, 1)
	return tea.Batch(
		executeFollowUpCmd(message, m.currentTask, m.sessionID, m.assistant, m.dataManager, m.eventCh, m.publisherCh, resumed),
		listenForEvents(m.eventCh),
		listenForPublisher(m.publisherCh),
	)
}

// executeTaskCmd runs a coding task in a background goroutine.
func executeTaskCmd(
	taskDesc string,
	task *http.Task,
	sessionID string,
	ca *app.Yagent,
	tm *http.TaskManager,
	dm *datamanager.DataManager,
	eventCh chan *messaging.MessageEvent,
	publisherCh chan *messaging.MessagePublisher,
) tea.Cmd {
	return func() tea.Msg {
		dispatcher := messaging.NewMessageDispatcher(100)
		defer dispatcher.Shutdown()

		consumer := &tuiEventConsumer{ch: eventCh}
		dispatcher.RegisterConsumer(consumer)

		ca.IntegrateMessaging(dispatcher)

		// Send publisher to TUI so it can respond to authorization dialogs
		publisher := messaging.NewMessagePublisher(dispatcher)
		select {
		case publisherCh <- publisher:
		default:
		}

		request := app.NewTaskRequest(task.Context, task.ID).
			WithSessionID(sessionID).
			WithProjectDir(task.ProjectDir).
			WithTaskDesc(taskDesc).
			WithMemory(task.Memory).
			WithMessagePublisher(messaging.NewMessagePublisher(dispatcher))

		// Start periodic save (every 5 seconds)
		stopPeriodicSave := make(chan struct{})
		ticker := time.NewTicker(5 * time.Second)
		go func() {
			for {
				select {
				case <-ticker.C:
					if dm != nil {
						dm.SaveTaskMemory(task.ID, task.Memory)
					}
				case <-stopPeriodicSave:
					return
				}
			}
		}()
		defer close(stopPeriodicSave)
		defer ticker.Stop()

		// 设置当前 task ID，使日志路由到 task 专属目录
		logging.SetCurrentTaskID(task.ID)
		defer logging.SetCurrentTaskID("")

		result, err := ca.ProcessCodingTaskWithCallback(request)

		if dm != nil {
			dm.SaveTaskMemory(task.ID, task.Memory)
			dm.FlushTaskMemory(task.ID)
		}

		if err != nil {
			tm.SetTaskError(task.ID, err.Error())
		} else {
			tm.SetTaskResult(task.ID, result)
		}

		return taskCompleteMsg{taskID: task.ID, result: result, err: err}
	}
}

// executeFollowUpCmd runs a follow-up message on an existing task.
// resumed 表示该 task 由会话恢复而来（仅首次追问为 true），Director 续跑首轮
// 强制注入项目上下文。
func executeFollowUpCmd(
	message string,
	task *http.Task,
	sessionID string,
	ca *app.Yagent,
	dm *datamanager.DataManager,
	eventCh chan *messaging.MessageEvent,
	publisherCh chan *messaging.MessagePublisher,
	resumed bool,
) tea.Cmd {
	return func() tea.Msg {
		dispatcher := messaging.NewMessageDispatcher(100)
		defer dispatcher.Shutdown()

		consumer := &tuiEventConsumer{ch: eventCh}
		dispatcher.RegisterConsumer(consumer)

		ca.IntegrateMessaging(dispatcher)

		// Send publisher to TUI so it can respond to authorization dialogs
		publisher := messaging.NewMessagePublisher(dispatcher)
		select {
		case publisherCh <- publisher:
		default:
		}

		request := app.NewTaskRequest(task.Context, task.ID).
			WithSessionID(sessionID).
			WithProjectDir(task.ProjectDir).
			WithUserMessage(message).
			WithMemory(task.Memory).
			WithResumed(resumed).
			WithMessagePublisher(messaging.NewMessagePublisher(dispatcher))

		// Start periodic save (every 5 seconds)
		stopPeriodicSave := make(chan struct{})
		ticker := time.NewTicker(5 * time.Second)
		go func() {
			for {
				select {
				case <-ticker.C:
					if dm != nil {
						dm.SaveTaskMemory(task.ID, task.Memory)
					}
				case <-stopPeriodicSave:
					return
				}
			}
		}()
		defer close(stopPeriodicSave)
		defer ticker.Stop()

		// 设置当前 task ID，使日志路由到 task 专属目录
		logging.SetCurrentTaskID(task.ID)
		defer logging.SetCurrentTaskID("")

		result, err := ca.ProcessConversation(request)

		if dm != nil {
			dm.SaveTaskMemory(task.ID, task.Memory)
			dm.FlushTaskMemory(task.ID)
		}

		return taskCompleteMsg{taskID: task.ID, result: result, err: err}
	}
}
