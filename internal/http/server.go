package http

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"codeactor/internal/assistant"
	"codeactor/internal/memory"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/olahol/melody"
)

// Server HTTP服务器结构
type Server struct {
	taskManager     *TaskManager
	codingAssistant *assistant.CodingAssistant
	dataManager     *assistant.DataManager
	melody          *melody.Melody
	router          *gin.Engine
}

// NewServer 创建新的HTTP服务器
func NewServer(codingAssistant *assistant.CodingAssistant) *Server {
	// 创建 WebSocket 管理器
	m := melody.New()
	m.Config.MessageBufferSize = 256

	// 创建全局任务管理器
	taskManager := NewTaskManager(m)

	// 初始化数据管理器
	dataManager, err := assistant.NewDataManager()
	if err != nil {
		slog.Error("Failed to initialize DataManager", "error", err)
	}

	// 设置 WebSocket 处理器
	HandleWebSocket(m, taskManager, codingAssistant, dataManager)

	// 使用 gin 创建路由
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/api/task_status"},
	}))

	server := &Server{
		taskManager:     taskManager,
		codingAssistant: codingAssistant,
		dataManager:     dataManager,
		melody:          m,
		router:          r,
	}

	server.setupRoutes()
	return server
}

// setupRoutes 设置所有路由
func (s *Server) setupRoutes() {
	// 添加 CORS 支持
	s.router.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// WebSocket 路由
	s.router.GET("/ws", func(c *gin.Context) {
		s.melody.HandleRequest(c.Writer, c.Request)
	})

	// 处理编程任务的API（兼容原有HTTP接口）
	s.router.POST("/api/start_task", s.handleStartTask)

	// 查询任务状态的API
	s.router.GET("/api/task_status", s.handleTaskStatus)

	// 获取对话记忆的API
	s.router.GET("/api/memory", s.handleGetMemory)

	// 清空对话记忆的API
	s.router.DELETE("/api/memory", s.handleClearMemory)

	// 取消任务的API
	s.router.POST("/api/cancel_task", s.handleCancelTask)

	// 获取特定类型消息的API
	s.router.GET("/api/memory/:type", s.handleGetMemoryByType)

	// 获取历史任务列表
	s.router.GET("/api/history", s.handleListHistory)

	// 加载历史任务
	s.router.POST("/api/load_task", s.handleLoadTask)
}

func (s *Server) handleListHistory(c *gin.Context) {
	if s.dataManager == nil {
		c.JSON(500, gin.H{"error": "DataManager not initialized"})
		return
	}

	limit := 50 // Default limit
	history, err := s.dataManager.ListTaskHistory(limit)
	if err != nil {
		slog.Error("Failed to list task history", "error", err)
		c.JSON(500, gin.H{"error": "Failed to list task history"})
		return
	}

	c.JSON(200, history)
}

func (s *Server) handleLoadTask(c *gin.Context) {
	var req struct {
		TaskID     string `json:"task_id"`
		ProjectDir string `json:"project_dir"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid request body"})
		return
	}
	if req.TaskID == "" {
		c.JSON(400, gin.H{"error": "task_id is required"})
		return
	}

	if s.dataManager == nil {
		c.JSON(500, gin.H{"error": "DataManager not initialized"})
		return
	}

	// 检查任务是否已经在运行
	if _, ok := s.taskManager.GetTask(req.TaskID); ok {
		// 已经在运行，直接返回成功
		c.JSON(200, gin.H{"task_id": req.TaskID, "message": "Task is already running"})
		return
	}

	// 加载Memory
	mem, err := s.dataManager.LoadTaskMemory(req.TaskID)
	if err != nil {
		slog.Error("Failed to load task memory", "error", err, "task_id", req.TaskID)
		c.JSON(404, gin.H{"error": "Task memory not found"})
		return
	}

	// 创建新任务，使用加载的Memory
	// 使用用户提供的ProjectDir，或者如果为空则尝试从Memory中推断（如果可能），或者设为空。
	// 这里我们假设如果为空，用户可能只是想查看。
	// 但为了支持继续任务，我们应该尽量有ProjectDir。
	// 如果前端在Load时无法提供ProjectDir（因为可能不在历史记录中），我们可能需要让用户选择。
	// 暂时只使用请求中的ProjectDir。

	ctx, cancel := context.WithCancel(context.Background())
	task := &Task{
		ID:         req.TaskID,
		Status:     TaskStatusRunning,
		ProjectDir: req.ProjectDir,
		CreatedAt:  time.Now(), // This is technically "restored at"
		UpdatedAt:  time.Now(),
		Memory:     mem,
		Context:    ctx,
		CancelFunc: cancel,
	}

	s.taskManager.AddTask(task)
	slog.Info("Task loaded/restored", "task_id", req.TaskID)

	c.JSON(200, gin.H{"task_id": req.TaskID, "message": "Task loaded successfully"})
}

func (s *Server) handleStartTask(c *gin.Context) {
	var req CodingTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid request body"})
		return
	}
	if req.ProjectDir == "" {
		c.JSON(400, gin.H{"error": "project_dir is required"})
		return
	}
	if req.TaskDesc == "" {
		c.JSON(400, gin.H{"error": "task_desc is required"})
		return
	}
	slog.Info("HTTP coding task submitted", "project_dir", req.ProjectDir, "task_desc", req.TaskDesc)

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())

	var task *Task

	if req.TaskID != "" {
		// 尝试查找现有任务
		existingTask, ok := s.taskManager.GetTask(req.TaskID)
		if ok {
			// 如果任务正在运行，拒绝请求（或者可以设计为排队）
			if existingTask.Status == TaskStatusRunning {
				cancel()
				c.JSON(409, gin.H{"error": "Task is already running"})
				return
			}
			task = existingTask
			task.Status = TaskStatusRunning
			task.UpdatedAt = time.Now()
			task.CancelFunc = cancel
			task.Context = ctx
			// 保留 Memory，不重新创建
			slog.Info("Continuing existing task", "task_id", task.ID)
		} else {
			// 如果内存中没有，尝试从 DataManager 加载 Memory
			// 这通常发生在服务重启后，但 WebUI 保留了 task_id
			mem, err := s.dataManager.LoadTaskMemory(req.TaskID)
			if err == nil {
				task = &Task{
					ID:         req.TaskID,
					Status:     TaskStatusRunning,
					ProjectDir: req.ProjectDir,
					CreatedAt:  time.Now(), // 实际上是恢复时间
					UpdatedAt:  time.Now(),
					Memory:     mem,
					Context:    ctx,
					CancelFunc: cancel,
				}
				s.taskManager.AddTask(task)
				slog.Info("Restored task from memory", "task_id", req.TaskID)
			} else {
				// 如果无法加载 Memory，回退到创建新任务，但使用请求的 TaskID (或者生成新的，这里选择生成新的以避免混淆，或者报错)
				// 为了稳健性，如果找不到以前的 Memory，我们把它当作新任务，但最好通知用户。
				// 这里我们选择创建一个新任务，但使用请求的 ID，这样前端不用改 ID。
				// 不过要注意，如果前端以为有 Memory 但实际没有，可能会很奇怪。
				// 既然是 start_task，如果找不到之前的，就当新的开始吧。
				task = &Task{
					ID:         req.TaskID,
					Status:     TaskStatusRunning,
					ProjectDir: req.ProjectDir,
					CreatedAt:  time.Now(),
					UpdatedAt:  time.Now(),
					Memory:     memory.NewConversationMemory(300),
					Context:    ctx,
					CancelFunc: cancel,
				}
				s.taskManager.AddTask(task)
				slog.Info("Task memory not found, starting fresh with provided ID", "task_id", req.TaskID)
			}
		}
	} else {
		// 创建新任务
		task = &Task{
			ID:         uuid.New().String(),
			Status:     TaskStatusRunning,
			ProjectDir: req.ProjectDir,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Memory:     memory.NewConversationMemory(300),
			Context:    ctx,
			CancelFunc: cancel,
		}
		s.taskManager.AddTask(task)
		slog.Info("Task created", "task_id", task.ID)
	}

	// 后台执行任务
	go ExecuteTask(task.ID, req.ProjectDir, req.TaskDesc, s.taskManager, s.codingAssistant, s.dataManager)

	c.JSON(200, CodingTaskResponse{TaskID: task.ID})
}

func (s *Server) handleTaskStatus(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		c.JSON(400, gin.H{"error": "task_id is required"})
		return
	}
	task, ok := s.taskManager.GetTask(taskID)
	if !ok {
		c.JSON(404, gin.H{"error": "task not found"})
		return
	}
	c.JSON(200, gin.H{
		"task_id":    task.ID,
		"status":     task.Status,
		"result":     task.Result,
		"error":      task.Error,
		"progress":   task.Progress,
		"created_at": task.CreatedAt,
		"updated_at": task.UpdatedAt,
		"memory": gin.H{
			"messages": task.Memory.GetMessages(),
			"size":     task.Memory.Size(),
			"max_size": task.Memory.MaxSize,
		},
	})
}

func (s *Server) handleGetMemory(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		c.JSON(400, MemoryResponse{Error: "task_id is required"})
		return
	}

	task, ok := s.taskManager.GetTask(taskID)
	if !ok {
		c.JSON(404, MemoryResponse{Error: "task not found"})
		return
	}

	response := MemoryResponse{
		Messages: task.Memory.GetMessages(),
		Size:     task.Memory.Size(),
		MaxSize:  task.Memory.MaxSize,
	}
	c.IndentedJSON(200, response)
}

func (s *Server) handleClearMemory(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		c.JSON(400, ClearMemoryResponse{Error: "task_id is required"})
		return
	}

	task, ok := s.taskManager.GetTask(taskID)
	if !ok {
		c.JSON(404, ClearMemoryResponse{Error: "task not found"})
		return
	}

	task.Memory.Clear()
	response := ClearMemoryResponse{
		Success: true,
		Message: "Memory cleared successfully",
	}
	slog.Info("Memory cleared", "task_id", taskID)
	c.JSON(200, response)
}

func (s *Server) handleCancelTask(c *gin.Context) {
	var req struct {
		TaskID string `json:"task_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid request body"})
		return
	}
	if req.TaskID == "" {
		c.JSON(400, gin.H{"error": "task_id is required"})
		return
	}

	slog.Info("Cancel task", "task_id", req.TaskID)
	success := s.taskManager.CancelTask(req.TaskID)

	if success {
		c.JSON(200, gin.H{
			"task_id": req.TaskID,
			"message": "Task cancelled successfully",
		})
		slog.Info("Task cancelled successfully", "task_id", req.TaskID)
	} else {
		c.JSON(404, gin.H{"error": "Task not found or not running"})
		slog.Warn("Failed to cancel task", "task_id", req.TaskID)
	}
}

func (s *Server) handleGetMemoryByType(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		c.JSON(400, MemoryResponse{Error: "task_id is required"})
		return
	}

	task, ok := s.taskManager.GetTask(taskID)
	if !ok {
		c.JSON(404, MemoryResponse{Error: "task not found"})
		return
	}

	messageType := c.Param("type")

	var msgType memory.MessageType
	switch messageType {
	case "system":
		msgType = memory.MessageTypeSystem
	case "human":
		msgType = memory.MessageTypeHuman
	case "assistant":
		msgType = memory.MessageTypeAssistant
	case "tool":
		msgType = memory.MessageTypeTool
	default:
		c.JSON(400, MemoryResponse{Error: "invalid message type. Valid types: system, human, assistant, tool"})
		return
	}

	messages := task.Memory.GetMessagesByType(msgType)
	response := MemoryResponse{
		Messages: messages,
		Size:     len(messages),
		MaxSize:  task.Memory.MaxSize,
	}
	c.JSON(200, response)
}

// Run 启动HTTP服务器
func (s *Server) Run(port int) error {
	slog.Info("AI Coding Assistant HTTP server started", "port", port)
	slog.Info("WebSocket server available at", "ws_url", fmt.Sprintf("ws://localhost:%d/ws", port))
	return s.router.Run(fmt.Sprintf(":%d", port))
}
