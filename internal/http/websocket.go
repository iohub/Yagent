package http

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"codeactor/internal/assistant"
	"codeactor/internal/memory"
	messaging "codeactor/pkg/messaging"
	consumers "codeactor/pkg/messaging/consumers"

	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/olahol/melody"
)

// HandleWebSocket 设置WebSocket处理器
func HandleWebSocket(m *melody.Melody, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant, dataManager *assistant.DataManager) {
	m.HandleConnect(func(s *melody.Session) {
		slog.Info("WebSocket client connected")
		// 发送连接确认消息
		message := SocketMessage{
			Type:  "connection",
			Event: "connected",
			Data:  gin.H{"message": "Connected to AI Coding Assistant"},
			From:  "System",
		}
		if data, err := json.Marshal(message); err == nil {
			s.Write(data)
		}
	})

	m.HandleDisconnect(func(s *melody.Session) {
		slog.Info("WebSocket client disconnected")
	})

	m.HandleMessage(func(s *melody.Session, msg []byte) {
		var socketMsg SocketMessage
		if err := json.Unmarshal(msg, &socketMsg); err != nil {
			slog.Error("Failed to unmarshal socket message", "error", err)
			return
		}

		switch socketMsg.Event {
		case "start_task":
			handleStartTask(s, socketMsg, taskManager, codingAssistant, dataManager)
		case "chat_message":
			handleChatMessage(s, socketMsg, taskManager, codingAssistant, dataManager)
		case "get_memory":
			handleGetMemory(s, socketMsg, taskManager)
		case "clear_memory":
			handleClearMemory(s, socketMsg, taskManager)
		default:
			slog.Warn("Unknown socket event", "event", socketMsg.Event)
		}
	})
}

func handleStartTask(s *melody.Session, msg SocketMessage, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant, dataManager *assistant.DataManager) {
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
		From:  "System",
	}
	if data, err := json.Marshal(response); err == nil {
		s.Write(data)
	}

	// 发送开始执行消息
	taskManager.SetTaskProgress(task.ID, "Starting coding task...")
	// 后台执行任务
	go ExecuteTask(task.ID, taskData.ProjectDir, taskData.TaskDesc, taskManager, codingAssistant, dataManager)

	// Publish task start event to TUI
	fmt.Printf("🚀 任务 %s 已启动\n", task.ID)
}

func handleChatMessage(s *melody.Session, msg SocketMessage, taskManager *TaskManager, codingAssistant *assistant.CodingAssistant, dataManager *assistant.DataManager) {
	var chatData struct {
		TaskID     string `json:"task_id"`
		Message    string `json:"message"`
		ProjectDir string `json:"project_dir"`
	}

	if data, ok := msg.Data.(map[string]interface{}); ok {
		if taskID, exists := data["task_id"].(string); exists {
			chatData.TaskID = taskID
		}
		if message, exists := data["message"].(string); exists {
			chatData.Message = message
		}
		if projectDir, exists := data["project_dir"].(string); exists {
			chatData.ProjectDir = projectDir
		}
	}

	if chatData.TaskID == "" || chatData.Message == "" {
		sendError(s, "task_id and message are required")
		return
	}

	task, ok := taskManager.GetTask(chatData.TaskID)
	if !ok {
		// 尝试从DataManager加载
		if dataManager != nil {
			mem, err := dataManager.LoadTaskMemory(chatData.TaskID)
			if err == nil {
				// 恢复任务
				// 使用客户端传递的ProjectDir，如果未传递则为空（可能会影响功能，但这是唯一的办法）
				// 如果ProjectDir为空，我们可以尝试查找最近的一个ProjectDir，或者在Agent层面报错
				projectDir := chatData.ProjectDir

				ctx, cancel := context.WithCancel(context.Background())
				task = &Task{
					ID:         chatData.TaskID,
					Status:     TaskStatusRunning, // 重新激活
					ProjectDir: projectDir,
					CreatedAt:  time.Now(),
					UpdatedAt:  time.Now(),
					Memory:     mem,
					Socket:     s,
					Context:    ctx,
					CancelFunc: cancel,
				}
				taskManager.AddTask(task)
				slog.Info("Task restored from memory for chat", "task_id", chatData.TaskID, "project_dir", projectDir)
			} else {
				sendError(s, "task not found and failed to load memory")
				return
			}
		} else {
			sendError(s, "task not found")
			return
		}
	} else {
		// 如果任务存在，更新ProjectDir（如果提供了新的）
		if chatData.ProjectDir != "" && task.ProjectDir == "" {
			task.ProjectDir = chatData.ProjectDir
		}
	}

	// 添加用户消息到记忆
	task.Memory.AddHumanMessage(chatData.Message)
	// Save memory immediately after adding user message
	if dataManager != nil {
		if err := dataManager.SaveTaskMemory(chatData.TaskID, task.Memory); err != nil {
			slog.Error("Failed to save task memory", "error", err, "task_id", chatData.TaskID)
		}
	}

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
				From:  event.From,
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
			slog.Error("Chat processing failed", "error", err, "task_id", chatData.TaskID)

			// Publish error event
			if dispatcher != nil {
				event := messaging.NewMessageEvent("conversation_error", map[string]interface{}{
					"task_id": chatData.TaskID,
					"error":   err.Error(),
				}, "System")
				dispatcher.Publish(event)
			}

			// 发送错误消息
			errorMsg := memory.ChatMessage{
				Type:      memory.MessageTypeAssistant,
				Content:   fmt.Sprintf("处理对话时发生错误: %v", err),
				Timestamp: time.Now(),
			}

			response := SocketMessage{
				Type:  "chat_message",
				Event: "ai_response",
				Data:  errorMsg,
				From:  "System",
			}
			if data, err := json.Marshal(response); err == nil {
				s.Write(data)
			}

			// Shutdown dispatcher
			dispatcher.Shutdown()
			return
		}

		// 发送AI回复
		aiMsg := memory.ChatMessage{
			Type:      memory.MessageTypeAssistant,
			Content:   result,
			Timestamp: time.Now(),
		}

		response := SocketMessage{
			Type:  "chat_message",
			Event: "ai_response",
			Data:  aiMsg,
			From:  "CodingAgent",
		}
		if data, err := json.Marshal(response); err == nil {
			s.Write(data)
		}

		// Publish conversation result event
		if dispatcher != nil {
			event := messaging.NewMessageEvent("conversation_result", map[string]interface{}{
				"task_id": chatData.TaskID,
				"result":  result,
			}, "System")
			dispatcher.Publish(event)
		}

		// Save memory after conversation turn
		if dataManager != nil {
			if err := dataManager.SaveTaskMemory(chatData.TaskID, task.Memory); err != nil {
				slog.Error("Failed to save task memory at end of turn", "error", err, "task_id", chatData.TaskID)
			}
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
		From: "System",
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
		From:  "System",
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
		From:    "System",
	}
	if data, err := json.Marshal(errorMsg); err == nil {
		s.Write(data)
	}
}
