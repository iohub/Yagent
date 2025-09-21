package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codeactor/internal/assistant"
	"codeactor/internal/util"
	messaging "codeactor/pkg/messaging"
	consumers "codeactor/pkg/messaging/consumers"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/olahol/melody"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func init() {
	// Initialize language manager with default language (English)
	langManager = NewLanguageManager()
}

// ========== Socket.IO 消息结构 ==========
type SocketMessage struct {
	Type    string      `json:"type"`
	Event   string      `json:"event"`
	Data    interface{} `json:"data"`
	TaskID  string      `json:"task_id,omitempty"`
	Message string      `json:"message,omitempty"`
}

type ChatMessage struct {
	Type      string `json:"type"` // "human" or "assistant"
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

type TaskUpdate struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	Progress  string `json:"progress,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// ========== HTTP API 结构 ==========
type CodingTaskRequest struct {
	ProjectDir string `json:"project_dir"`
	TaskDesc   string `json:"task_desc"`
}

type CodingTaskResponse struct {
	TaskID string `json:"task_id"`
	Error  string `json:"error,omitempty"`
}

type MemoryResponse struct {
	Messages []assistant.ChatMessage `json:"messages"`
	Size     int                     `json:"size"`
	MaxSize  int                     `json:"max_size"`
	Error    string                  `json:"error,omitempty"`
}

type ClearMemoryResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// ========== 任务管理相关结构 ==========
const (
	TaskStatusRunning  = "running"
	TaskStatusFinished = "finished"
	TaskStatusFailed   = "failed"
)

type Task struct {
	ID         string
	Status     string
	Result     string
	Error      string
	Progress   string
	ProjectDir string // 添加项目目录字段
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Memory     *assistant.ConversationMemory
	Socket     *melody.Session // 关联的WebSocket连接
}

type TaskManager struct {
	tasks map[string]*Task
	lock  sync.RWMutex
}

func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks: make(map[string]*Task),
	}
}

func (tm *TaskManager) CreateTask(socket *melody.Session, projectDir string) *Task {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	taskID := uuid.New().String()
	task := &Task{
		ID:         taskID,
		Status:     TaskStatusRunning,
		ProjectDir: projectDir,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Memory:     assistant.NewConversationMemory(300),
		Socket:     socket,
	}
	tm.tasks[taskID] = task
	return task
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

// ========== WebSocket 处理器 ==========
func handleWebSocket(m *melody.Melody, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant) {
	m.HandleConnect(func(s *melody.Session) {
		log.Info().Msg("WebSocket client connected")
		// 发送连接确认消息
		message := SocketMessage{
			Type:  "connection",
			Event: "connected",
			Data:  gin.H{"message": "Connected to AI Coding Assistant"},
		}
		if data, err := json.Marshal(message); err == nil {
			s.Write(data)
		}
	})

	m.HandleDisconnect(func(s *melody.Session) {
		log.Info().Msg("WebSocket client disconnected")
	})

	m.HandleMessage(func(s *melody.Session, msg []byte) {
		var socketMsg SocketMessage
		if err := json.Unmarshal(msg, &socketMsg); err != nil {
			log.Error().Err(err).Msg("Failed to unmarshal socket message")
			return
		}

		switch socketMsg.Event {
		case "start_task":
			handleStartTask(s, socketMsg, taskManager, codingAssistant)
		case "chat_message":
			handleChatMessage(s, socketMsg, taskManager, codingAssistant)
		case "get_memory":
			handleGetMemory(s, socketMsg, taskManager)
		case "clear_memory":
			handleClearMemory(s, socketMsg, taskManager)
		default:
			log.Warn().Str("event", socketMsg.Event).Msg("Unknown socket event")
		}
	})
}

func handleStartTask(s *melody.Session, msg SocketMessage, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant) {
	var taskData struct {
		ProjectDir string `json:"project_dir"`
		TaskDesc   string `json:"task_desc"`
	}

	if data, ok := msg.Data.(map[string]interface{}); ok {
		if projectDir, exists := data["project_dir"].(string); exists {
			taskData.ProjectDir = projectDir
		}
		if taskDesc, exists := data["task_desc"].(string); exists {
			taskData.TaskDesc = taskDesc
		}
	}

	if taskData.ProjectDir == "" || taskData.TaskDesc == "" {
		sendError(s, "project_dir and task_desc are required")
		return
	}

	task := taskManager.CreateTask(s, taskData.ProjectDir)

	// 发送任务创建确认
	response := SocketMessage{
		Type:  "task_created",
		Event: "task_created",
		Data:  gin.H{"task_id": task.ID},
	}
	if data, err := json.Marshal(response); err == nil {
		s.Write(data)
	}

	// 发送开始执行消息
	taskManager.SetTaskProgress(task.ID, "Starting coding task...")

	// 创建 WebSocket 回调函数
	wsCallback := func(messageType string, content string) {
		// 发送实时消息到前端
		realTimeMsg := SocketMessage{
			Type:  "realtime",
			Event: messageType,
			Data: gin.H{
				"task_id":   task.ID,
				"content":   content,
				"timestamp": time.Now().Unix(),
			},
		}
		if data, err := json.Marshal(realTimeMsg); err == nil {
			task.Socket.Write(data)
		}
	}

	// 后台执行任务
	go executeTask(task.ID, taskData.ProjectDir, taskData.TaskDesc, taskManager, codingAssistant, wsCallback)

	// Publish task start event to TUI
	fmt.Printf("🚀 任务 %s 已启动\n", task.ID)
}

func handleChatMessage(s *melody.Session, msg SocketMessage, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant) {
	var chatData struct {
		TaskID  string `json:"task_id"`
		Message string `json:"message"`
	}

	if data, ok := msg.Data.(map[string]interface{}); ok {
		if taskID, exists := data["task_id"].(string); exists {
			chatData.TaskID = taskID
		}
		if message, exists := data["message"].(string); exists {
			chatData.Message = message
		}
	}

	if chatData.TaskID == "" || chatData.Message == "" {
		sendError(s, "task_id and message are required")
		return
	}

	task, ok := taskManager.GetTask(chatData.TaskID)
	if !ok {
		sendError(s, "task not found")
		return
	}

	// 添加用户消息到记忆
	task.Memory.AddHumanMessage(chatData.Message)

	// 后台处理AI回复
	go func() {
		ctx := context.Background()

		// Initialize message dispatcher for this conversation
		dispatcher := messaging.NewMessageDispatcher(100)

		// Create WebSocket consumer
		wsConsumer := consumers.NewWebSocketConsumer(func(data []byte) error {
			var event messaging.MessageEvent
			if err := json.Unmarshal(data, &event); err != nil {
				return err
			}
			// Convert event to SocketMessage format
			socketMsg := SocketMessage{
				Type:  "realtime",
				Event: event.Type,
				Data: gin.H{
					"task_id":   chatData.TaskID,
					"content":   event.Content,
					"timestamp": event.Timestamp.Unix(),
					"metadata":  event.Metadata,
				},
			}
			if socketData, err := json.Marshal(socketMsg); err == nil {
				// Send to WebSocket
				return s.Write(socketData)
			}
			return nil
		})
		dispatcher.RegisterConsumer(wsConsumer)

		// Create TUI consumer for terminal output
		// Wire a real publisher so the TUI can send user responses back into the dispatcher
		uiPublisher := messaging.NewMessagePublisher(dispatcher)
		tuiConsumer := consumers.NewTUIConsumer(os.Stdout, uiPublisher)
		dispatcher.RegisterConsumer(tuiConsumer)

		// Integrate messaging with coding assistant
		codingAssistant.IntegrateMessaging(dispatcher)

		// 使用新的 TaskRequest 结构调用重构后的方法
		request := assistant.NewTaskRequest(ctx, chatData.TaskID).
			WithProjectDir(task.ProjectDir).
			WithUserMessage(chatData.Message).
			WithMemory(task.Memory).
			WithMessagePublisher(assistant.NewMessagePublisher(dispatcher))

		// 调用 AI 助手处理对话
		result, err := codingAssistant.ProcessConversation(request)
		if err != nil {
			log.Error().Err(err).Str("task_id", chatData.TaskID).Msg("Chat processing failed")

			// Publish error event
			if dispatcher != nil {
				event := messaging.NewMessageEvent("conversation_error", map[string]interface{}{
					"task_id": chatData.TaskID,
					"error":   err.Error(),
				})
				dispatcher.Publish(event)
			}

			// 发送错误消息
			errorMsg := ChatMessage{
				Type:      "assistant",
				Content:   fmt.Sprintf("处理对话时发生错误: %v", err),
				Timestamp: time.Now().Unix(),
			}

			response := SocketMessage{
				Type:  "chat_message",
				Event: "ai_response",
				Data:  errorMsg,
			}
			if data, err := json.Marshal(response); err == nil {
				s.Write(data)
			}

			// Shutdown dispatcher
			dispatcher.Shutdown()
			return
		}

		// 发送AI回复
		aiMsg := ChatMessage{
			Type:      "assistant",
			Content:   result,
			Timestamp: time.Now().Unix(),
		}

		response := SocketMessage{
			Type:  "chat_message",
			Event: "ai_response",
			Data:  aiMsg,
		}
		if data, err := json.Marshal(response); err == nil {
			s.Write(data)
		}

		// Publish conversation result event
		if dispatcher != nil {
			event := messaging.NewMessageEvent("conversation_result", map[string]interface{}{
				"task_id": chatData.TaskID,
				"result":  result,
			})
			dispatcher.Publish(event)
		}

		// Shutdown dispatcher
		dispatcher.Shutdown()
	}()
}

func handleGetMemory(s *melody.Session, msg SocketMessage, taskManager *TaskManager) {
	var memoryData struct {
		TaskID string `json:"task_id"`
	}

	if data, ok := msg.Data.(map[string]interface{}); ok {
		if taskID, exists := data["task_id"].(string); exists {
			memoryData.TaskID = taskID
		}
	}

	if memoryData.TaskID == "" {
		sendError(s, "task_id is required")
		return
	}

	task, ok := taskManager.GetTask(memoryData.TaskID)
	if !ok {
		sendError(s, "task not found")
		return
	}

	response := SocketMessage{
		Type:  "memory",
		Event: "memory_data",
		Data: gin.H{
			"messages": task.Memory.GetMessages(),
			"size":     task.Memory.Size(),
			"max_size": task.Memory.MaxSize,
		},
	}
	if data, err := json.Marshal(response); err == nil {
		s.Write(data)
	}
}

func handleClearMemory(s *melody.Session, msg SocketMessage, taskManager *TaskManager) {
	var memoryData struct {
		TaskID string `json:"task_id"`
	}

	if data, ok := msg.Data.(map[string]interface{}); ok {
		if taskID, exists := data["task_id"].(string); exists {
			memoryData.TaskID = taskID
		}
	}

	if memoryData.TaskID == "" {
		sendError(s, "task_id is required")
		return
	}

	task, ok := taskManager.GetTask(memoryData.TaskID)
	if !ok {
		sendError(s, "task not found")
		return
	}

	task.Memory.Clear()

	response := SocketMessage{
		Type:  "memory",
		Event: "memory_cleared",
		Data:  gin.H{"message": "Memory cleared successfully"},
	}
	if data, err := json.Marshal(response); err == nil {
		s.Write(data)
	}
}

func sendError(s *melody.Session, message string) {
	errorMsg := SocketMessage{
		Type:    "error",
		Event:   "error",
		Message: message,
	}
	if data, err := json.Marshal(errorMsg); err == nil {
		s.Write(data)
	}
}

// executeTask 执行任务的通用函数
func executeTask(taskID, projectDir, taskDesc string, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant, wsCallback func(messageType string, content string)) {
	ctx := context.Background()
	task, ok := taskManager.GetTask(taskID)
	if !ok {
		log.Error().Str("task_id", taskID).Msg("Task not found")
		return
	}

	// Initialize message dispatcher
	dispatcher := messaging.NewMessageDispatcher(100)

	// Create WebSocket consumer if callback is provided
	if wsCallback != nil {
		wsConsumer := consumers.NewWebSocketConsumer(func(data []byte) error {
			var event messaging.MessageEvent
			if err := json.Unmarshal(data, &event); err != nil {
				return err
			}
			// Convert event to SocketMessage format
			socketMsg := SocketMessage{
				Type:  "realtime",
				Event: event.Type,
				Data: gin.H{
					"task_id":   taskID,
					"content":   event.Content,
					"timestamp": event.Timestamp.Unix(),
					"metadata":  event.Metadata,
				},
			}
			if socketData, err := json.Marshal(socketMsg); err == nil {
				wsCallback(event.Type, string(socketData))
				return nil
			}
			return nil
		})
		dispatcher.RegisterConsumer(wsConsumer)
	}

	// Create TUI consumer for terminal output
	// Wire a real publisher so the TUI can send user responses back into the dispatcher
	uip := messaging.NewMessagePublisher(dispatcher)
	tuiConsumer := consumers.NewTUIConsumer(os.Stdout, uip)
	dispatcher.RegisterConsumer(tuiConsumer)

	// Integrate messaging with coding assistant
	codingAssistant.IntegrateMessaging(dispatcher)

	var result string
	var err error

	// 使用新的 TaskRequest 结构
	request := assistant.NewTaskRequest(ctx, taskID).
		WithProjectDir(projectDir).
		WithTaskDesc(taskDesc).
		WithMemory(task.Memory)

	if wsCallback != nil {
		request = request.WithWSCallback(wsCallback)
	}

	// Add message publisher to request
	request = request.WithMessagePublisher(assistant.NewMessagePublisher(dispatcher))

	result, err = codingAssistant.ProcessCodingTaskWithCallback(request)

	if err != nil {
		log.Error().Err(err).Str("task_id", taskID).Msg("Coding task failed")
		taskManager.SetTaskError(taskID, util.WrapError(ctx, err, "coding task failed").Error())
		return
	}
	log.Info().Str("task_id", taskID).Msg("Coding task finished")
	taskManager.SetTaskResult(taskID, result)

	// Shutdown dispatcher after task completion
	dispatcher.Shutdown()
}

func main() {
	// Check if running in TUI mode or HTTP server mode based on command line arguments
	if len(os.Args) < 2 {
		fmt.Println("Usage: codeactor [tui|http]")
		os.Exit(1)
	}

	mode := os.Args[1]
	// 解析 --taskfile 参数
	var taskFilePath string
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--taskfile" && i+1 < len(os.Args) {
			taskFilePath = os.Args[i+1]
			break
		} else if strings.HasPrefix(os.Args[i], "--taskfile=") {
			taskFilePath = strings.TrimPrefix(os.Args[i], "--taskfile=")
			break
		}
	}

	switch mode {
	case "tui":
		// Run TUI mode
		projectDir, taskDesc := startTUI(taskFilePath)
		if projectDir != "" && taskDesc != "" {
			// Execute task directly
			ctx := context.Background()
			var err error

			// Load configuration
			configPath := getConfigPath()
			log.Info().Str("config_path", configPath).Msg("Loading configuration")
			config, err := assistant.LoadConfig(configPath)
			if err != nil {
				log.Fatal().Err(util.WrapError(ctx, err, "main::LoadConfig")).Msg("Failed to load configuration")
			}

			// Create client
			client, err := assistant.NewClient(config)
			if err != nil {
				log.Fatal().Err(util.WrapError(ctx, err, "main::NewClient")).Msg("Failed to create client")
			}

			// Create coding assistant
			codingAssistant, err := assistant.NewCodingAssistant(client)
			if err != nil {
				log.Fatal().Err(util.WrapError(ctx, err, "main::NewCodingAssistant")).Msg("Failed to create coding assistant")
			}

			// Create task manager
			taskManager := NewTaskManager()

			// Create task
			task := &Task{
				ID:         uuid.New().String(),
				Status:     TaskStatusRunning,
				ProjectDir: projectDir,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
				Memory:     assistant.NewConversationMemory(300),
			}

			// Add task to manager
			taskManager.lock.Lock()
			taskManager.tasks[task.ID] = task
			taskManager.lock.Unlock()

			// Execute task
			log.Info().Str("project_dir", projectDir).Str("task_desc", taskDesc).Msg("TUI coding task submitted")
			executeTask(task.ID, projectDir, taskDesc, taskManager, codingAssistant, nil)

			// Wait for task completion and display result
			for {
				time.Sleep(1 * time.Second)
				currentTask, ok := taskManager.GetTask(task.ID)
				if ok && (currentTask.Status == TaskStatusFinished || currentTask.Status == TaskStatusFailed) {
					break
				}
			}

			// Display result
			finalTask, _ := taskManager.GetTask(task.ID)
			if finalTask.Status == TaskStatusFinished {
				fmt.Printf("\n\nTask completed successfully!\nResult: %s\n", finalTask.Result)
			} else {
				fmt.Printf("\n\nTask failed!\nError: %s\n", finalTask.Error)
			}
			return
		}
		return
	case "http":
		// Run HTTP server mode
		// Setup zerolog for pretty console logging and file logging
		ctx := context.Background()
		homeDir, herr := os.UserHomeDir()
		if herr != nil {
			log.Fatal().Err(util.WrapError(ctx, herr, "main::UserHomeDir")).Msg("Failed to get user home directory")
		}
		logDir := filepath.Join(homeDir, ".codeactor", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			log.Fatal().Err(util.WrapError(ctx, err, "main::MkdirAll")).Msg("Failed to create logs directory")
		}

		logFile, err := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatal().Err(util.WrapError(ctx, err, "main::OpenFile")).Msg("Failed to open log file")
		}

		// 配置纯文本格式的日志输出
		consoleWriter := zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: time.RFC3339,
			FormatLevel: func(i interface{}) string {
				if ll, ok := i.(string); ok {
					return ll
				}
				return "INFO"
			},
			FormatMessage: func(i interface{}) string {
				if i == nil {
					return ""
				}
				return fmt.Sprintf("| %s", i)
			},
			FormatFieldName: func(i interface{}) string {
				return fmt.Sprintf("%s=", i)
			},
			FormatFieldValue: func(i interface{}) string {
				return fmt.Sprintf("%s", i)
			},
		}

		// 文件输出也使用纯文本格式
		fileWriter := zerolog.ConsoleWriter{
			Out:        logFile,
			TimeFormat: time.RFC3339,
			NoColor:    true, // 文件中不使用颜色
			FormatLevel: func(i interface{}) string {
				if ll, ok := i.(string); ok {
					return ll
				}
				return "INFO"
			},
			FormatMessage: func(i interface{}) string {
				if i == nil {
					return ""
				}
				return fmt.Sprintf("| %s", i)
			},
			FormatFieldName: func(i interface{}) string {
				return fmt.Sprintf("%s=", i)
			},
			FormatFieldValue: func(i interface{}) string {
				return fmt.Sprintf("%s", i)
			},
		}

		multi := zerolog.MultiLevelWriter(consoleWriter, fileWriter)
		log.Logger = log.Output(multi)

		// 加载配置和初始化 assistant.client
		configPath := getConfigPath()
		log.Info().Str("config_path", configPath).Msg("Loading configuration")
		config, err := assistant.LoadConfig(configPath)
		if err != nil {
			log.Fatal().Err(util.WrapError(ctx, err, "main::LoadConfig")).Msg("Failed to load configuration")
		}
		log.Info().Msg("Creating assistant.client")
		client, err := assistant.NewClient(config)
		if err != nil {
			log.Fatal().Err(util.WrapError(ctx, err, "main::NewClient")).Msg("Failed to create assistant.client")
		}

		// 创建 AI Coding Assistant
		codingAssistant, err := assistant.NewCodingAssistant(client)
		if err != nil {
			log.Fatal().Err(util.WrapError(ctx, err, "main::NewCodingAssistant")).Msg("Failed to create coding assistant")
		}

		// 创建 Director Agent
		directorAgent, err := assistant.NewDirectorAgent(client)
		if err != nil {
			log.Fatal().Err(util.WrapError(ctx, err, "main::NewDirectorAgent")).Msg("Failed to create director agent")
		}

		// 创建消息分发器并集成消息系统
		messageDispatcher := messaging.NewMessageDispatcher(100)
		directorAgent.IntegrateMessaging(messageDispatcher)

		// 创建 Director Coordinator
		directorCoordinator := assistant.NewDirectorCoordinator()
		_ = directorCoordinator // 暂时标记为已使用，避免编译错误

		// 创建全局任务管理器
		taskManager := NewTaskManager()

		// 创建 WebSocket 管理器
		m := melody.New()
		m.Config.MessageBufferSize = 256

		// 设置 WebSocket 处理器
		handleWebSocket(m, taskManager, codingAssistant)

		// 使用 gin 创建路由
		r := gin.Default()

		// 添加 CORS 支持
		r.Use(func(c *gin.Context) {
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
		r.GET("/ws", func(c *gin.Context) {
			m.HandleRequest(c.Writer, c.Request)
		})

		// 处理编程任务的API（兼容原有HTTP接口）
		r.POST("/api/start_task", func(c *gin.Context) {
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
			log.Info().Str("project_dir", req.ProjectDir).Str("task_desc", req.TaskDesc).Msg("HTTP coding task submitted")

			// 创建任务但不关联WebSocket
			task := &Task{
				ID:         uuid.New().String(),
				Status:     TaskStatusRunning,
				ProjectDir: req.ProjectDir,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
				Memory:     assistant.NewConversationMemory(300),
			}
			taskManager.lock.Lock()
			taskManager.tasks[task.ID] = task
			taskManager.lock.Unlock()

			// 后台执行任务
			go executeTask(task.ID, req.ProjectDir, req.TaskDesc, taskManager, codingAssistant, nil)

			c.JSON(200, CodingTaskResponse{TaskID: task.ID})
		})

		// 查询任务状态的API
		r.GET("/api/task_status", func(c *gin.Context) {
			taskID := c.Query("task_id")
			if taskID == "" {
				c.JSON(400, gin.H{"error": "task_id is required"})
				return
			}
			task, ok := taskManager.GetTask(taskID)
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
		})

		// 获取对话记忆的API
		r.GET("/api/memory", func(c *gin.Context) {
			taskID := c.Query("task_id")
			if taskID == "" {
				c.JSON(400, MemoryResponse{Error: "task_id is required"})
				return
			}

			task, ok := taskManager.GetTask(taskID)
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
		})

		// 清空对话记忆的API
		r.DELETE("/api/memory", func(c *gin.Context) {
			taskID := c.Query("task_id")
			if taskID == "" {
				c.JSON(400, ClearMemoryResponse{Error: "task_id is required"})
				return
			}

			task, ok := taskManager.GetTask(taskID)
			if !ok {
				c.JSON(404, ClearMemoryResponse{Error: "task not found"})
				return
			}

			task.Memory.Clear()
			response := ClearMemoryResponse{
				Success: true,
				Message: "Memory cleared successfully",
			}
			log.Info().Str("task_id", taskID).Msg("Memory cleared")
			c.JSON(200, response)
		})

		// 获取特定类型消息的API
		r.GET("/api/memory/:type", func(c *gin.Context) {
			taskID := c.Query("task_id")
			if taskID == "" {
				c.JSON(400, MemoryResponse{Error: "task_id is required"})
				return
			}

			task, ok := taskManager.GetTask(taskID)
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
		})

		// 从配置中读取HTTP服务端口
		serverPort := config.HTTP.ServerPort
		if serverPort == 0 {
			serverPort = 10080 // 默认端口
		}
		log.Info().Int("port", serverPort).Msg("AI Coding Assistant HTTP server started")
		log.Info().Str("ws_url", fmt.Sprintf("ws://localhost:%d/ws", serverPort)).Msg("WebSocket server available at")
		r.Run(fmt.Sprintf(":%d", serverPort))
	default:
		fmt.Printf("Unknown mode: %s\n", mode)
		fmt.Println("Usage: codeactor [tui|http]")
		os.Exit(1)
	}
}

// getConfigPath 返回配置文件的路径，优先使用 $HOME/.codeactor/config/config.toml
func getConfigPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// 如果无法获取用户主目录，回退到本地 config/config.toml
		return "config/config.toml"
	}
	
	configDir := filepath.Join(homeDir, ".codeactor", "config")
	configPath := filepath.Join(configDir, "config.toml")
	
	// 检查配置文件是否存在
	if _, err := os.Stat(configPath); err == nil {
		return configPath
	}
	
	// 如果用户目录下的配置文件不存在，检查并创建目录
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		if err := os.MkdirAll(configDir, 0755); err != nil {
			// 如果创建目录失败，回退到本地配置
			return "config/config.toml"
		}
		
		// 如果目录创建成功但配置文件不存在，创建默认配置文件
		defaultConfig := `# LLM Configuration
[http]
server_port = 9080

[llm]
# 选择当前使用的提供商
use_provider = "aliyun"

# 阿里云配置
[llm.providers.aliyun]
model = "qwen3-max-preview"
temperature = 0.0
max_tokens = 28000
api_base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "your-aliyun-api-key"

# SiliconFlow配置
[llm.providers.siliconflow]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://api.siliconflow.cn/v1"
api_key = "your-siliconflow-api-key"

# OpenRouter配置
[llm.providers.openrouter]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://openrouter.ai/api/v1"
api_key = "your-openrouter-api-key"

[app]
enable_streaming = true
`
		
		if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
			// 如果创建默认配置失败，回退到本地配置
			return "config/config.toml"
		}
	}
	
	return configPath
}

// ensureDefaultConfig 确保默认配置文件存在
func ensureDefaultConfig() {
	configPath := getConfigPath()
	if configPath == "config/config.toml" {
		// 如果使用的是本地配置，不需要额外处理
		return
	}
	
	// 检查配置文件是否存在
	if _, err := os.Stat(configPath); err == nil {
		return // 配置文件已存在
	}
	
	// 创建默认配置
	defaultConfig := `# LLM Configuration
[http]
server_port = 9080

[llm]
# 选择当前使用的提供商
use_provider = "aliyun"

# 阿里云配置
[llm.providers.aliyun]
model = "qwen3-max-preview"
temperature = 0.0
max_tokens = 28000
api_base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "your-aliyun-api-key"

# SiliconFlow配置
[llm.providers.siliconflow]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://api.siliconflow.cn/v1"
api_key = "your-siliconflow-api-key"

# OpenRouter配置
[llm.providers.openrouter]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://openrouter.ai/api/v1"
api_key = "your-openrouter-api-key"

[app]
enable_streaming = true
`
	
	if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
		log.Warn().Err(err).Str("config_path", configPath).Msg("Failed to create default config file")
	}
}
