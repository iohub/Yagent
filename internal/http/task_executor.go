package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/datamanager"
	messaging "codeactor/internal/messaging"
	consumers "codeactor/internal/messaging/consumers"
	"codeactor/internal/protocol"
	"codeactor/internal/util"

	"github.com/gin-gonic/gin"
)

// ExecuteTask 执行任务的通用函数
func ExecuteTask(taskID, projectDir, taskDesc string, taskManager *TaskManager, codeActor *app.CodeActor, dataManager *datamanager.DataManager) {
	task, ok := taskManager.GetTask(taskID)
	if !ok {
		slog.Error("Task not found", "task_id", taskID)
		return
	}

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

	// Create TaskManager WebSocket consumer to handle all message types
	taskManagerWSCallback := func(data []byte) error {
		var event messaging.MessageEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		socketMsg := NewRealtimeMessage(
			protocol.EventType(event.Type),
			gin.H{
				"task_id":   taskID,
				"content":   event.Content,
				"timestamp": event.Timestamp.Unix(),
				"metadata":  event.Metadata,
			},
			event.From,
			taskID,
		)
		taskManager.BroadcastMessage(socketMsg)
		return nil
	}
	taskManagerWSConsumer := consumers.NewWebSocketConsumer(taskManagerWSCallback)
	dispatcher.RegisterConsumer(taskManagerWSConsumer)

	// Integrate messaging with coding assistant
	codeActor.IntegrateMessaging(dispatcher)

	var result string
	var err error

	// 使用新的 TaskRequest 结构
	request := app.NewTaskRequest(ctx, taskID).
		WithProjectDir(projectDir).
		WithTaskDesc(taskDesc).
		WithMemory(task.Memory)

	// Add message publisher to request
	request = request.WithMessagePublisher(messaging.NewMessagePublisher(dispatcher))

	// 启动定期保存 memory 的 goroutine，确保运行期间的消息也能及时写入文件
	stopPeriodicSave := make(chan struct{})
	if dataManager != nil {
		ticker := time.NewTicker(5 * time.Second)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := dataManager.SaveTaskMemory(taskID, task.Memory); err != nil {
						slog.Warn("Failed to periodic save task memory", "task_id", taskID, "error", err)
					}
				case <-stopPeriodicSave:
					return
				}
			}
		}()
		// 任务结束时停止定时保存并刷新
		defer close(stopPeriodicSave)
		defer func() {
			if dataManager != nil {
				if err := dataManager.FlushTaskMemory(taskID); err != nil {
					slog.Warn("Failed to flush task memory", "task_id", taskID, "error", err)
				}
			}
		}()
	}

	result, err = codeActor.ProcessCodingTaskWithCallback(request)

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
