package http

import (
	"encoding/json"
	"log/slog"
	"os"

	"codeactor/internal/assistant"
	"codeactor/internal/util"
	messaging "codeactor/pkg/messaging"
	consumers "codeactor/pkg/messaging/consumers"
)

// ExecuteTask 执行任务的通用函数
func ExecuteTask(taskID, projectDir, taskDesc string, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant) {
	task, ok := taskManager.GetTask(taskID)
	if !ok {
		slog.Error("Task not found", "task_id", taskID)
		return
	}

	// 使用任务的可取消上下文
	ctx := task.Context

	// Initialize message dispatcher
	dispatcher := messaging.NewMessageDispatcher(100)

	// Create TUI consumer for terminal output
	// Wire a real publisher so the TUI can send user responses back into the dispatcher
	uip := messaging.NewMessagePublisher(dispatcher)
	tuiConsumer := consumers.NewTUIConsumer(os.Stdout, uip)
	dispatcher.RegisterConsumer(tuiConsumer)

	// Create TaskManager WebSocket consumer to handle all message types
	taskManagerWSCallback := func(data []byte) error {
		var event messaging.MessageEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		socketMsg := SocketMessage{
			Type:   event.Type,
			Event:  event.Type,
			Data:   event.Content,
			TaskID: taskID,
		}
		taskManager.BroadcastMessage(socketMsg)
		return nil
	}
	taskManagerWSConsumer := consumers.NewWebSocketConsumer(taskManagerWSCallback)
	dispatcher.RegisterConsumer(taskManagerWSConsumer)

	// Integrate messaging with coding assistant
	codingAssistant.IntegrateMessaging(dispatcher)

	var result string
	var err error

	wsCallback := func(messageType string, content string) {
		taskManager.BroadcastMessage(SocketMessage{
			Type:   "agent_msg",
			Event:  messageType,
			Data:   content,
			TaskID: taskID,
		})
	}
	// 使用新的 TaskRequest 结构
	request := assistant.NewTaskRequest(ctx, taskID).
		WithProjectDir(projectDir).
		WithTaskDesc(taskDesc).
		WithMemory(task.Memory).
		WithWSCallback(wsCallback)

	// Add message publisher to request
	request = request.WithMessagePublisher(assistant.NewMessagePublisher(dispatcher))

	result, err = codingAssistant.ProcessCodingTaskWithCallback(request)

	if err != nil {
		slog.Error("Coding task failed", "error", err, "task_id", taskID)
		// 检查是否是因为上下文取消导致的错误
		if ctx.Err() != nil {
			slog.Info("Task was cancelled", "task_id", taskID)
			taskManager.SetTaskError(taskID, "Task was cancelled by user")
		} else {
			taskManager.SetTaskError(taskID, util.WrapError(ctx, err, "coding task failed").Error())
		}
		return
	}
	slog.Info("Coding task finished", "task_id", taskID)
	taskManager.SetTaskResult(taskID, result)

	// Shutdown dispatcher after task completion
	dispatcher.Shutdown()
}
