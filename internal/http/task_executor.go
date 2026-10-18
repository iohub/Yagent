package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"yagent/internal/app"
	"yagent/internal/datamanager"
	"yagent/internal/logging"
	messaging "yagent/internal/messaging"
	consumers "yagent/internal/messaging/consumers"
	"yagent/internal/protocol"
	"yagent/internal/supervise"
	"yagent/internal/util"

	"github.com/gin-gonic/gin"
)

// ExecuteTask 执行任务的通用函数
func ExecuteTask(taskID, projectDir, taskDesc string, taskManager *TaskManager, yagent *app.Yagent, dataManager *datamanager.DataManager) {
	task, ok := taskManager.GetTask(taskID)
	if !ok {
		slog.Error("Task not found", "task_id", taskID)
		return
	}

	// 使用任务的可取消上下文
	ctx := task.Context

	// Initialize message dispatcher
	dispatcher := messaging.NewDefaultMessageDispatcher()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in ExecuteTask", "error", r, "task_id", taskID)
			taskManager.SetTaskError(taskID, fmt.Sprintf("Internal error: %v", r))
		}
		// Shutdown dispatcher after task completion
		dispatcher.Shutdown()
		dispatcher.WarnDropped("http/task")
		// 任务收尾：等这个任务 ctx 上那个域里的后台工作退干净（周期保存那条）。
		// 不等的话它会带着已经取消的 taskID 继续往 SaveTaskMemory 里写，
		// 而表现是"任务结束了，文件还在被动"。
		if leaked := supervise.From(ctx).Drain(5 * time.Second); len(leaked) > 0 {
			slog.Warn("ExecuteTask 收尾仍有任务级 goroutine 在跑", "task_id", taskID, "leases", supervise.LeaseNames(leaked))
		}
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
				// 并行委派下前端只能靠这两个坐标把两股流分开摆：
				// From（Agent 名）不再唯一，Seq 让它知道少了哪一条。
				// 刻意放进 Data 而不是改 AgentEventEnvelope —— 那是生成物，
				// 重跑 protoc-gen-yagent 会把仓库里的手工修复冲掉（AGENTS.md §6.6）。
				"seq":           event.Seq,
				"delegation_id": event.DelegationID,
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
	yagent.IntegrateMessaging(dispatcher)

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
		supervise.From(ctx).Go("http/periodic-save", func(context.Context) {
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
		})
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

	// 设置当前 taskID，使日志写入对应 task 目录
	logging.SetCurrentTaskID(taskID)
	defer logging.SetCurrentTaskID("")

	result, err = yagent.ProcessCodingTaskWithCallback(request)

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
