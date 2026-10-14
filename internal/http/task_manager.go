package http

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"yagent/internal/memory"
	"yagent/internal/protocol"

	"github.com/google/uuid"
	"github.com/olahol/melody"
)

type TaskManager struct {
	tasks       map[string]*Task
	lock        sync.RWMutex
	melody      *melody.Melody
	taskTimeout time.Duration
}

func NewTaskManager(m *melody.Melody, taskTimeout time.Duration) *TaskManager {
	return &TaskManager{
		tasks:       make(map[string]*Task),
		melody:      m,
		taskTimeout: taskTimeout,
	}
}

func (tm *TaskManager) CreateTask(socket *melody.Session, projectDir string) *Task {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	ctx, cancel := NewTaskContext(tm.taskTimeout)

	taskID := uuid.New().String()
	task := &Task{
		ID:         taskID,
		Status:     TaskStatusRunning,
		ProjectDir: projectDir,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Memory:     memory.NewConversationMemory(300),
		Socket:     socket,
		Context:    ctx,
		CancelFunc: cancel,
	}
	tm.tasks[taskID] = task
	return task
}

func (tm *TaskManager) AddTask(task *Task) {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	tm.tasks[task.ID] = task
}

func (tm *TaskManager) SetTaskResult(taskID, result string) {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	if task, ok := tm.tasks[taskID]; ok {
		task.Status = TaskStatusFinished
		task.Result = result
		task.UpdatedAt = time.Now()
		// 通过WebSocket发送更新
		tm.sendTaskUpdate(task)
	} else {
		slog.Warn("SetTaskResult: Task not found", "task_id", taskID)
	}
}

func (tm *TaskManager) SetTaskError(taskID, errMsg string) {
	slog.Info("SetTaskError called", "task_id", taskID, "error", errMsg)
	tm.lock.Lock()
	defer tm.lock.Unlock()
	if task, ok := tm.tasks[taskID]; ok {
		task.Status = TaskStatusFailed
		task.Error = errMsg
		task.UpdatedAt = time.Now()
		// 通过WebSocket发送更新
		tm.sendTaskUpdate(task)
	} else {
		slog.Warn("SetTaskError: Task not found", "task_id", taskID)
	}
}

func (tm *TaskManager) SetTaskProgress(taskID, progress string) {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	if task, ok := tm.tasks[taskID]; ok {
		task.Progress = progress
		task.UpdatedAt = time.Now()
		// 通过WebSocket发送进度更新
		tm.sendTaskUpdate(task)
	}
}

func (tm *TaskManager) GetTask(taskID string) (*Task, bool) {
	tm.lock.RLock()
	defer tm.lock.RUnlock()
	task, ok := tm.tasks[taskID]
	return task, ok
}

func (tm *TaskManager) sendTaskUpdate(task *Task) {
	if task.Socket != nil {
		update := TaskUpdate{
			TaskID:    task.ID,
			Status:    task.Status,
			Result:    task.Result,
			Error:     task.Error,
			Progress:  task.Progress,
			UpdatedAt: task.UpdatedAt.Unix(),
		}

		message := SocketMessage{
			Type:  "task_update",
			Event: "task_update",
			Data:  update,
			From:  "System",
		}

		if data, err := json.Marshal(message); err == nil {
			task.Socket.Write(data)
		}
	}
}

// BroadcastMessage 广播消息到所有连接的session
func (tm *TaskManager) BroadcastMessage(message interface{}) {
	if tm.melody != nil {
		data, err := json.Marshal(message)
		if err == nil {
			tm.melody.Broadcast(data)
		}
	}
}

// PauseTask 按下这个任务的暂停闸门：执行会在下一个 step 边界停住。
//
// 返回 false 有三种可能，都必须让调用方看得见（而不是"接受了但什么也没停"）：
// 任务不存在、任务已结束、这个任务没有闸门（走的是不建闸门的旧路径）。
// 已暂停的任务再按一次是幂等的，返回 true。
func (tm *TaskManager) PauseTask(taskID string) bool {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	task, ok := tm.tasks[taskID]
	if !ok {
		slog.Warn("Pause task: not found", "task_id", taskID)
		return false
	}
	gate := task.Gate()
	if gate == nil {
		slog.Warn("Pause task: this task's ctx carries no pause gate", "task_id", taskID)
		return false
	}
	if task.Status != TaskStatusRunning && task.Status != TaskStatusPaused {
		slog.Warn("Pause task: task is not running", "task_id", taskID, "status", task.Status)
		return false
	}
	gate.Pause("http /api/pause_task")
	task.Status = TaskStatusPaused
	task.UpdatedAt = time.Now()
	slog.Info("Pause task", "task_id", taskID)
	tm.sendTaskUpdate(task)
	// Blocked 在这一刻必然是 false —— 闸门刚按下，执行还没走到 step 边界。
	// 把这个字段带到协议里就是为了不骗人：客户端可以据此显示"正在停"。
	tm.BroadcastMessage(NewRealtimeMessage(protocol.EventTypeTaskPaused, protocol.TaskPausedData{
		TaskId:  taskID,
		Reason:  "http /api/pause_task",
		Blocked: gate.Blocked(),
	}, "System", taskID))
	return true
}

// ContinueTask 放开闸门，停在 step 边界上的执行继续往下跑。
//
// 判据是"状态仍是 paused"而不只是"闸门按过"：暂停之后又被取消的任务，闸门还停在
// paused 上，但它的 ctx 已经死了 —— 放开它只会让循环往下跑一步然后立刻失败，
// 而接口却回答了"已继续"。A 层没有跨进程恢复（那是 P2 的 B 层），所以这种任务
// 在本进程里就是救不回来了，必须显式拒绝。
func (tm *TaskManager) ContinueTask(taskID string) bool {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	task, ok := tm.tasks[taskID]
	if !ok {
		slog.Warn("Continue task: not found", "task_id", taskID)
		return false
	}
	gate := task.Gate()
	if gate == nil {
		slog.Warn("Continue task: this task's ctx carries no pause gate", "task_id", taskID)
		return false
	}
	if task.Status != TaskStatusPaused || !gate.Paused() {
		slog.Warn("Continue task: task was not paused", "task_id", taskID,
			"status", task.Status, "gate_paused", gate.Paused())
		return false
	}
	gate.Resume()
	task.Status = TaskStatusRunning
	task.UpdatedAt = time.Now()
	slog.Info("Continue task", "task_id", taskID)
	tm.sendTaskUpdate(task)
	tm.BroadcastMessage(NewRealtimeMessage(protocol.EventTypeTaskResumed, protocol.TaskResumedData{
		TaskId: taskID,
		Reason: "http /api/continue_task",
	}, "System", taskID))
	return true
}

// CancelTask 取消指定的任务
func (tm *TaskManager) CancelTask(taskID string) bool {
	slog.Info("Cancel task", "task_id", taskID)
	tm.lock.Lock()
	defer tm.lock.Unlock()
	if task, ok := tm.tasks[taskID]; ok {
		task.Status = TaskStatusCancelled
		task.UpdatedAt = time.Now()
		if task.CancelFunc != nil {
			task.CancelFunc()
		}
		tm.sendTaskUpdate(task)
		return true
	} else {
		slog.Warn("Task not found or not running", "task_id", taskID)
	}
	return false
}
