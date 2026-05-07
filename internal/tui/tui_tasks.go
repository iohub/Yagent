package tui

import (
	"context"
	"strings"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/memory"
	"codeactor/pkg/messaging"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

func (m *model) submitTask() tea.Cmd {
	taskDesc := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	m.taskRunning = true
	m.commandMode = true
	m.errMsg = ""

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
		eventType: "status",
		content:   "Task submitted: " + taskDesc,
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
	m.input.SetValue("")
	m.taskRunning = true
	m.commandMode = true
	m.errMsg = ""

	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   message,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	m.publisherCh = make(chan *messaging.MessagePublisher, 1)
	return tea.Batch(
		executeFollowUpCmd(message, m.currentTask, m.assistant, m.dataManager, m.eventCh, m.publisherCh),
		listenForEvents(m.eventCh),
		listenForPublisher(m.publisherCh),
		tickCmd(),
	)
}

// executeTaskCmd runs a coding task in a background goroutine.
func executeTaskCmd(
	taskDesc string,
	task *http.Task,
	ca *app.CodingAssistant,
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
	ca *app.CodingAssistant,
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
