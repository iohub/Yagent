package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"codeactor/internal/assistant"
	"codeactor/internal/util"
	messaging "codeactor/pkg/messaging"
	consumers "codeactor/pkg/messaging/consumers"
)

// ExecuteTask 执行任务的通用函数
func ExecuteTask(taskID, projectDir, taskDesc string, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant, dataManager *assistant.DataManager) {
	task, ok := taskManager.GetTask(taskID)
	if !ok {
		slog.Error("Task not found", "task_id", taskID)
		return
	}

	// Initialize codebase in background
	go func() {
		payload := map[string]string{"project_dir": projectDir}
		jsonData, err := json.Marshal(payload)
		if err != nil {
			slog.Error("Failed to marshal codebase_init payload", "error", err)
			return
		}

		req, err := http.NewRequest("POST", "http://127.0.0.1:12800/codebase_init", bytes.NewBuffer(jsonData))
		if err != nil {
			slog.Error("Failed to create codebase_init request", "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			slog.Error("Failed to send codebase_init request", "error", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			slog.Error("codebase_init failed", "status_code", resp.StatusCode)
		} else {
			slog.Info("codebase_init request sent successfully", "project_dir", projectDir)
		}
	}()

	// 使用任务的可取消上下文
	ctx := task.Context

	// Initialize message dispatcher
	dispatcher := messaging.NewMessageDispatcher(100)

	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in ExecuteTask", "error", r, "task_id", taskID)
			taskManager.SetTaskError(taskID, fmt.Sprintf("Internal error: %v", r))
		}
		// Shutdown dispatcher after task completion
		dispatcher.Shutdown()
	}()

	// Create TUI consumer for terminal output
	// Wire a real publisher so the TUI can send user responses back into the dispatcher
	uip := messaging.NewMessagePublisher(dispatcher)
	tuiConsumer := consumers.NewTUIConsumer(os.Stdout, uip)
	dispatcher.RegisterConsumer(tuiConsumer)

	// Create Persistence consumer if dataManager is provided
	if dataManager != nil {
		persistenceCallback := func(data []byte) error {
			var event messaging.MessageEvent
			if err := json.Unmarshal(data, &event); err != nil {
				return err
			}
			if event.Type == "memory_change" {
				if err := dataManager.SaveTaskMemory(taskID, task.Memory); err != nil {
					slog.Error("Failed to save task memory", "error", err, "task_id", taskID)
				}
			}
			return nil
		}
		persistenceConsumer := consumers.NewWebSocketConsumer(persistenceCallback)
		dispatcher.RegisterConsumer(persistenceConsumer)
	}

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
			From:   event.From,
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
		slog.Error("Task failed", "error", err, "task_id", taskID)
		// 检查是否是因为上下文取消导致的错误
		if ctx.Err() != nil {
			slog.Info("Task was cancelled", "task_id", taskID)
			taskManager.SetTaskError(taskID, "Task was cancelled by user")
		} else {
			taskManager.SetTaskError(taskID, util.WrapError(ctx, err, "coding task failed").Error())
		}
		return
	}
	slog.Info("Task completed successfully", "task_id", taskID)
	taskManager.SetTaskResult(taskID, result)

	// Save memory one last time
	if dataManager != nil {
		if err := dataManager.SaveTaskMemory(taskID, task.Memory); err != nil {
			slog.Error("Failed to save task memory at completion", "error", err, "task_id", taskID)
		}
	}
}
