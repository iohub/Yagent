package memory

import (
	"encoding/json"
	"fmt"
)

// KV keys 前缀
const (
	kvKeyPrefixUser      = "shared_dim:user:%s"
	kvKeyPrefixFeedback  = "shared_dim:feedback:%s"
	kvKeyPrefixReference = "shared_dim:reference:%s"
)

// SharedDimensionStore 提供4维共享记忆的CRUD操作
// 基于现有的 SharedMemory KV 存储实现
type SharedDimensionStore struct {
	kv *SharedMemory // 全局共享内存
}

// NewSharedDimensionStore 创建新的共享维度存储
func NewSharedDimensionStore(kv *SharedMemory) *SharedDimensionStore {
	return &SharedDimensionStore{kv: kv}
}

// ---- User Memory ----

// GetUserMemory 获取用户记忆
func (s *SharedDimensionStore) GetUserMemory(userID string) (*UserMemory, error) {
	key := fmt.Sprintf(kvKeyPrefixUser, userID)
	data, err := s.kv.GetKey(key)
	if err != nil {
		return nil, fmt.Errorf("get user memory: %w", err)
	}
	if data == "" {
		return &UserMemory{UserID: userID}, nil
	}
	var m UserMemory
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, fmt.Errorf("unmarshal user memory: %w", err)
	}
	return &m, nil
}

// SetUserMemory 保存用户记忆
func (s *SharedDimensionStore) SetUserMemory(m *UserMemory) error {
	key := fmt.Sprintf(kvKeyPrefixUser, m.UserID)
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal user memory: %w", err)
	}
	return s.kv.SetKey(key, string(data))
}

// ---- Feedback Memory ----

// GetFeedbackMemory 获取反馈记忆
func (s *SharedDimensionStore) GetFeedbackMemory(userID string) (*FeedbackMemory, error) {
	key := fmt.Sprintf(kvKeyPrefixFeedback, userID)
	data, err := s.kv.GetKey(key)
	if err != nil {
		return nil, fmt.Errorf("get feedback memory: %w", err)
	}
	if data == "" {
		return &FeedbackMemory{UserID: userID}, nil
	}
	var m FeedbackMemory
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, fmt.Errorf("unmarshal feedback memory: %w", err)
	}
	return &m, nil
}

// SetFeedbackMemory 保存反馈记忆
func (s *SharedDimensionStore) SetFeedbackMemory(m *FeedbackMemory) error {
	key := fmt.Sprintf(kvKeyPrefixFeedback, m.UserID)
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal feedback memory: %w", err)
	}
	return s.kv.SetKey(key, string(data))
}

// ---- Reference Memory ----

// GetReferenceMemory 获取参考记忆
func (s *SharedDimensionStore) GetReferenceMemory(projectID string) (*ReferenceMemory, error) {
	key := fmt.Sprintf(kvKeyPrefixReference, projectID)
	data, err := s.kv.GetKey(key)
	if err != nil {
		return nil, fmt.Errorf("get reference memory: %w", err)
	}
	if data == "" {
		return &ReferenceMemory{ProjectID: projectID}, nil
	}
	var m ReferenceMemory
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, fmt.Errorf("unmarshal reference memory: %w", err)
	}
	return &m, nil
}

// SetReferenceMemory 保存参考记忆
func (s *SharedDimensionStore) SetReferenceMemory(m *ReferenceMemory) error {
	key := fmt.Sprintf(kvKeyPrefixReference, m.ProjectID)
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal reference memory: %w", err)
	}
	return s.kv.SetKey(key, string(data))
}

// ---- Bulk Operations ----

// GetAllForUser 获取指定用户的所有共享记忆
func (s *SharedDimensionStore) GetAllForUser(userID, projectID string) (*UserMemory, *FeedbackMemory, *ReferenceMemory, error) {
	userMem, err := s.GetUserMemory(userID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get user memory: %w", err)
	}
	fbMem, err := s.GetFeedbackMemory(userID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get feedback memory: %w", err)
	}
	refMem, err := s.GetReferenceMemory(projectID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get reference memory: %w", err)
	}
	return userMem, fbMem, refMem, nil
}

// ClearDimension 清除指定维度的记忆
func (s *SharedDimensionStore) ClearDimension(dim Dimension, id string) error {
	var key string
	switch dim {
	case DimUser:
		key = fmt.Sprintf(kvKeyPrefixUser, id)
	case DimFeedback:
		key = fmt.Sprintf(kvKeyPrefixFeedback, id)
	case DimReference:
		key = fmt.Sprintf(kvKeyPrefixReference, id)
	default:
		return fmt.Errorf("unknown dimension: %s", dim)
	}
	return s.kv.DeleteKey(key)
}
