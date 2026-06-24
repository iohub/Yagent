package peer

// PublishConfig 发布配置
type PublishConfig struct {
	CorrelationID string
	Headers       map[string]string
	Target        string
	Version       int64
}

// PublishOption 配置 Publish 调用
type PublishOption func(*PublishConfig)

// WithHeader 添加自定义 header
func WithHeader(k, v string) PublishOption {
	return func(c *PublishConfig) {
		if c.Headers == nil {
			c.Headers = make(map[string]string)
		}
		c.Headers[k] = v
	}
}

// WithTarget 指定目标 agentID
func WithTarget(targetID string) PublishOption {
	return func(c *PublishConfig) { c.Target = targetID }
}

// WithVersion 指定版本号
func WithVersion(v int64) PublishOption {
	return func(c *PublishConfig) { c.Version = v }
}

// WithCorrelationID 指定关联 ID
func WithCorrelationID(cid string) PublishOption {
	return func(c *PublishConfig) { c.CorrelationID = cid }
}
