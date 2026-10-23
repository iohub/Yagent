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

// taskRetention 是终态任务在表里保留多久。它是测试用的 seam（同 datamanager 的
// taskFlushDebounce）：不改它的话，"过期清扫"这条用例要么等一小时，要么只能断言常量。
var taskRetention = time.Hour

func (tm *TaskManager) CreateTask(socket *melody.Session, projectDir string) *Task {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	tm.evictLocked(time.Now())

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

// evictLocked 清掉"停在终态、且超过 taskRetention 没再动过"的任务。
//
// 这张表原来只进不出：每个历史 Task 连着整份 ConversationMemory 与结果正文会一路堆到
// 进程重启。挑在 CreateTask 时清扫而不是起一条定时 goroutine —— 没人来建新任务的
// 时候没人需要这份内存，也就不必再多一条要等人等的后台。
// paused 与 running 一律不动：前者还要靠 /api/continue_task 救回来。
func (tm *TaskManager) evictLocked(now time.Time) {
	for id, task := range tm.tasks {
		if task.Status == TaskStatusRunning || task.Status == TaskStatusPaused {
			continue
		}
		if now.Sub(task.UpdatedAt) < taskRetention {
			continue
		}
		delete(tm.tasks, id)
	}
}

func (tm *TaskManager) SetTaskResult(taskID, result string) {
	tm.lock.Lock()
	task, ok := tm.tasks[taskID]
	if !ok {
		tm.lock.Unlock()
		slog.Warn("SetTaskResult: Task not found", "task_id", taskID)
		return
	}
	task.Status = TaskStatusFinished
	task.Result = result
	task.UpdatedAt = time.Now()
	act := tm.prepareTaskUpdate(task)
	tm.lock.Unlock()
	act.deliver(tm.melody)
}

func (tm *TaskManager) SetTaskError(taskID, errMsg string) {
	slog.Info("SetTaskError called", "task_id", taskID, "error", errMsg)
	tm.lock.Lock()
	task, ok := tm.tasks[taskID]
	if !ok {
		tm.lock.Unlock()
		slog.Warn("SetTaskError: Task not found", "task_id", taskID)
		return
	}
	task.Status = TaskStatusFailed
	task.Error = errMsg
	task.UpdatedAt = time.Now()
	act := tm.prepareTaskUpdate(task)
	tm.lock.Unlock()
	act.deliver(tm.melody)
}

func (tm *TaskManager) SetTaskProgress(taskID, progress string) {
	tm.lock.Lock()
	task, ok := tm.tasks[taskID]
	if !ok {
		tm.lock.Unlock()
		return
	}
	task.Progress = progress
	task.UpdatedAt = time.Now()
	act := tm.prepareTaskUpdate(task)
	tm.lock.Unlock()
	act.deliver(tm.melody)
}

func (tm *TaskManager) GetTask(taskID string) (*Task, bool) {
	tm.lock.RLock()
	defer tm.lock.RUnlock()
	task, ok := tm.tasks[taskID]
	return task, ok
}

// writeSession 是投递 WS 单播的唯一出口。它是测试用的 seam（同 defaultToolTimeout、
// taskFlushDebounce 一个道理）：这一层要钉的恰恰是"慢客户端不许占着 tm.lock"，
// 而 melody.Session 的写要一条真实连接才测得出来。
var writeSession = func(s *melody.Session, data []byte) error {
	if s == nil {
		return nil
	}
	return s.Write(data)
}

// melodyBroadcast 是广播的唯一出口，与 writeSession 同一个理由：这一层要钉的是
// "投递不许在 tm.lock 里"，而 melody 的广播要真实连接才测得出来。
var melodyBroadcast = func(m *melody.Melody, data []byte) error {
	if m == nil {
		return nil
	}
	return m.Broadcast(data)
}

// taskAction 是"锁内装配、锁外投递"的那对字节。
//
// 把 Socket.Write 与 melody.Broadcast 挪出 tm.lock 的理由：melody 的写会在慢客户端上
// 阻塞，而原来它们发生在**写锁里** —— 于是一个卡住的浏览器标签能把所有任务的状态更新、
// 以及 /api/task_status 的读锁一起顶住。表现不是报错，是"整个服务点不动了"。
type taskAction struct {
	data      []byte
	socket    *melody.Session
	broadcast []byte
}

func (a taskAction) deliver(m *melody.Melody) {
	if len(a.data) > 0 {
		if err := writeSession(a.socket, a.data); err != nil {
			slog.Debug("task update write failed", "error", err)
		}
	}
	if m != nil && len(a.broadcast) > 0 {
		if err := melodyBroadcast(m, a.broadcast); err != nil {
			slog.Debug("task broadcast failed", "error", err)
		}
	}
}

// prepareTaskUpdate 必须在持有 tm.lock 时调用（它读 Task 的字段），
// 只做装配、不做任何 IO。
func (tm *TaskManager) prepareTaskUpdate(task *Task) taskAction {
	if task.Socket == nil {
		return taskAction{}
	}
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
	data, err := json.Marshal(message)
	if err != nil {
		slog.Warn("failed to marshal task update", "task_id", task.ID, "error", err)
		return taskAction{}
	}
	return taskAction{data: data, socket: task.Socket}
}

// prepareBroadcast 把要广播的消息在锁内编成字节，投递留到锁外。
func prepareBroadcast(message interface{}) []byte {
	data, err := json.Marshal(message)
	if err != nil {
		slog.Warn("failed to broadcast message", "error", err)
		return nil
	}
	return data
}

// BroadcastMessage 广播消息到所有连接的session
func (tm *TaskManager) BroadcastMessage(message interface{}) {
	if tm.melody == nil {
		return
	}
	if data := prepareBroadcast(message); len(data) > 0 {
		if err := melodyBroadcast(tm.melody, data); err != nil {
			slog.Debug("broadcast failed", "error", err)
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
	task, ok := tm.tasks[taskID]
	if !ok {
		tm.lock.Unlock()
		slog.Warn("Pause task: not found", "task_id", taskID)
		return false
	}
	gate := task.Gate()
	if gate == nil {
		tm.lock.Unlock()
		slog.Warn("Pause task: this task's ctx carries no pause gate", "task_id", taskID)
		return false
	}
	if task.Status != TaskStatusRunning && task.Status != TaskStatusPaused {
		tm.lock.Unlock()
		slog.Warn("Pause task: task is not running", "task_id", taskID, "status", task.Status)
		return false
	}
	gate.Pause("http /api/pause_task")
	task.Status = TaskStatusPaused
	task.UpdatedAt = time.Now()
	slog.Info("Pause task", "task_id", taskID)
	// Blocked 在这一刻必然是 false —— 闸门刚按下，执行还没走到 step 边界。
	// 把这个字段带到协议里就是为了不骗人：客户端可以据此显示"正在停"。
	// 广播的字节也在锁内编好（gate.Blocked() 读的是闸门自己的原子状态，与这张表无关），
	// 真正的 Socket 写留到 Unlock 之后。
	act := tm.prepareTaskUpdate(task)
	act.broadcast = prepareBroadcast(NewRealtimeMessage(protocol.EventTypeTaskPaused, protocol.TaskPausedData{
		TaskId:  taskID,
		Reason:  "http /api/pause_task",
		Blocked: gate.Blocked(),
	}, "System", taskID))
	tm.lock.Unlock()
	act.deliver(tm.melody)
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
	task, ok := tm.tasks[taskID]
	if !ok {
		tm.lock.Unlock()
		slog.Warn("Continue task: not found", "task_id", taskID)
		return false
	}
	gate := task.Gate()
	if gate == nil {
		tm.lock.Unlock()
		slog.Warn("Continue task: this task's ctx carries no pause gate", "task_id", taskID)
		return false
	}
	if task.Status != TaskStatusPaused || !gate.Paused() {
		tm.lock.Unlock()
		slog.Warn("Continue task: task was not paused", "task_id", taskID,
			"status", task.Status, "gate_paused", gate.Paused())
		return false
	}
	gate.Resume()
	task.Status = TaskStatusRunning
	task.UpdatedAt = time.Now()
	slog.Info("Continue task", "task_id", taskID)
	act := tm.prepareTaskUpdate(task)
	act.broadcast = prepareBroadcast(NewRealtimeMessage(protocol.EventTypeTaskResumed, protocol.TaskResumedData{
		TaskId: taskID,
		Reason: "http /api/continue_task",
	}, "System", taskID))
	tm.lock.Unlock()
	act.deliver(tm.melody)
	return true
}

// CancelTask 取消指定的任务
func (tm *TaskManager) CancelTask(taskID string) bool {
	slog.Info("Cancel task", "task_id", taskID)
	tm.lock.Lock()
	task, ok := tm.tasks[taskID]
	if !ok {
		tm.lock.Unlock()
		slog.Warn("Task not found or not running", "task_id", taskID)
		return false
	}
	task.Status = TaskStatusCancelled
	task.UpdatedAt = time.Now()
	if task.CancelFunc != nil {
		task.CancelFunc()
	}
	act := tm.prepareTaskUpdate(task)
	tm.lock.Unlock()
	act.deliver(tm.melody)
	return true
}
