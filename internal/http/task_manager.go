package http

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"codeactor/internal/assistant"

	"github.com/google/uuid"
	"github.com/olahol/melody"
	"github.com/rs/zerolog/log"
)

type TaskManager struct {
	tasks  map[string]*Task
	lock   sync.RWMutex
	melody *melody.Melody
}

func NewTaskManager(m *melody.Melody) *TaskManager {
	return &TaskManager{
		tasks:  make(map[string]*Task),
		melody: m,
	}
}

func (tm *TaskManager) CreateTask(socket *melody.Session, projectDir string) *Task {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())

	taskID := uuid.New().String()
	task := &Task{
		ID:         taskID,
		Status:     TaskStatusRunning,
		ProjectDir: projectDir,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Memory:     assistant.NewConversationMemory(300),
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
	}
}

func (tm *TaskManager) SetTaskError(taskID, errMsg string) {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	if task, ok := tm.tasks[taskID]; ok {
		task.Status = TaskStatusFailed
		task.Error = errMsg
		task.UpdatedAt = time.Now()
		// 通过WebSocket发送更新
		tm.sendTaskUpdate(task)
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

// CancelTask 取消指定的任务
func (tm *TaskManager) CancelTask(taskID string) bool {
	log.Info().Str("task_id", taskID).Msg("Cancel task")
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
		log.Warn().Str("task_id", taskID).Msg("Task not found or not running")
	}
	return false
}