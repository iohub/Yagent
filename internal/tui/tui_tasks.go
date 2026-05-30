package tui

import (
	"context"
	"strings"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/compact"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/memory"
	"codeactor/internal/messaging"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
)

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

	// Count input tokens
	if taskDesc != "" {
		if tok := compact.GetGlobalTokenizer(); tok != nil {
			if count, err := tok.CountTokens(taskDesc); err == nil && count > 0 {
				m.inputTokens += int64(count)
			}
		}
	}

	m.taskRunning = true
	m.commandMode = true
	m.errMsg = ""
	// Task started — update status bar cache
	m.cachedStatusBar = m.renderAirlineStatusBar()
	m.statusBarValid = true
	// Also update token dashboard since we counted input tokens
	m.cachedTokenDashboard = m.renderTokenDashboard()
	m.tokenDashboardValid = true

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

	// Add submission entry
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "user_message",
		content:   taskDesc,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	m.publisherCh = make(chan *messaging.MessagePublisher, 1)
	return tea.Batch(
		executeTaskCmd(taskDesc, task, m.assistant, m.taskManager, m.dataManager, m.eventCh, m.publisherCh),
		listenForEvents(m.eventCh),
		listenForPublisher(m.publisherCh),
		tickCmd(),
	)
}

// submitFollowUp sends a follow-up message to an existing task.
func (m *model) submitFollowUp(message string) tea.Cmd {
	// Count input tokens
	if message != "" {
		if tok := compact.GetGlobalTokenizer(); tok != nil {
			if count, err := tok.CountTokens(message); err == nil && count > 0 {
				m.inputTokens += int64(count)
			}
		}
	}

	m.input.SetValue("")
	m.taskRunning = true
	m.commandMode = true
	m.errMsg = ""
	// Task started — update status bar cache
	m.cachedStatusBar = m.renderAirlineStatusBar()
	m.statusBarValid = true
	// Also update token dashboard since we counted input tokens
	m.cachedTokenDashboard = m.renderTokenDashboard()
	m.tokenDashboardValid = true

	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "user_message",
		content:   message,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	m.publisherCh = make(chan *messaging.MessagePublisher, 1)
	return tea.Batch(
		executeFollowUpCmd(message, m.currentTask, m.assistant, m.dataManager, m.eventCh, m.publisherCh),
		listenForEvents(m.eventCh),
		listenForPublisher(m.publisherCh),
	)
}

// executeTaskCmd runs a coding task in a background goroutine.
func executeTaskCmd(
	taskDesc string,
	task *http.Task,
	ca *app.CodeActor,
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
			WithProjectDir(task.ProjectDir).
			WithTaskDesc(taskDesc).
			WithMemory(task.Memory).
			WithMessagePublisher(messaging.NewMessagePublisher(dispatcher))

		result, err := ca.ProcessCodingTaskWithCallback(request)

		if dm != nil {
			if saveErr := dm.SaveTaskMemory(task.ID, task.Memory); saveErr != nil {
				// non-fatal
			}
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
func executeFollowUpCmd(
	message string,
	task *http.Task,
	ca *app.CodeActor,
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
			WithProjectDir(task.ProjectDir).
			WithUserMessage(message).
			WithMemory(task.Memory).
			WithMessagePublisher(messaging.NewMessagePublisher(dispatcher))

		result, err := ca.ProcessConversation(request)

		if dm != nil {
			if saveErr := dm.SaveTaskMemory(task.ID, task.Memory); saveErr != nil {
				// non-fatal
			}
		}

		return taskCompleteMsg{taskID: task.ID, result: result, err: err}
	}
}
