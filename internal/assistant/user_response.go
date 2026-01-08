package assistant

import (
	"sync"
)

// waitForUserResponse 等待用户回复
func (ca *CodingAssistant) waitForUserResponse(taskID string) <-chan string {
	ca.mu.Lock()
	if ca.userResponseChannels == nil {
		ca.userResponseChannels = make(map[string]chan string)
	}
	responseChan := make(chan string, 1)
	ca.userResponseChannels[taskID] = responseChan
	ca.mu.Unlock()

	return responseChan
}

// HandleUserResponse 处理用户回复
func (ca *CodingAssistant) HandleUserResponse(taskID string, response string) {
	ca.mu.Lock()
	if responseChan, exists := ca.userResponseChannels[taskID]; exists {
		select {
		case responseChan <- response:
			// 用户回复已发送
		default:
			// 通道已满或关闭
		}
		// 清理通道
		delete(ca.userResponseChannels, taskID)
	}
	ca.mu.Unlock()
}

// Add mutex for thread safety
var mu sync.Mutex