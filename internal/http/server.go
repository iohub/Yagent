package http

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"codeactor/internal/assistant"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/olahol/melody"
)

// Server HTTP服务器结构
type Server struct {
	taskManager     *TaskManager
	codingAssistant *assistant.CodingAssistant
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

	// 设置 WebSocket 处理器
	HandleWebSocket(m, taskManager, codingAssistant)

	// 使用 gin 创建路由
	r := gin.Default()

	server := &Server{
		taskManager:     taskManager,
		codingAssistant: codingAssistant,
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
	task := &Task{
		ID:         uuid.New().String(),
		Status:     TaskStatusRunning,
		ProjectDir: req.ProjectDir,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Memory:     assistant.NewConversationMemory(300),
		Context:    ctx,
		CancelFunc: cancel,
	}
	s.taskManager.lock.Lock()
	s.taskManager.tasks[task.ID] = task
	s.taskManager.lock.Unlock()
	slog.Info("Task created", "task_id", task.ID)
	// 后台执行任务
	go ExecuteTask(task.ID, req.ProjectDir, req.TaskDesc, s.taskManager, s.codingAssistant)

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

	var msgType assistant.MessageType
	switch messageType {
	case "system":
		msgType = assistant.MessageTypeSystem
	case "human":
		msgType = assistant.MessageTypeHuman
	case "assistant":
		msgType = assistant.MessageTypeAssistant
	case "tool":
		msgType = assistant.MessageTypeTool
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
