package peer

// ─── 同域 P2P Topic（直连，Director 仅观察）───
const (
	TopicSymbolsUpdated    = "symbols.updated"     // Repo → Coding (pub/sub)
	TopicSymbolsRequest    = "symbols.request"     // Coding → Repo (req/resp)
	TopicSymbolsInvalidate = "symbols.invalidate"  // Repo → Coding (pub/sub)
	TopicPageStateChanged  = "pagestate.changed"   // Browser → Coding (pub/sub)
	TopicPageStateRequest  = "pagestate.request"   // Coding → Browser (req/resp)
	TopicImpactAnalysis    = "analysis.impact"     // Coding → Repo (req/resp)
	TopicFileChanged       = "files.changed"       // Coding → Repo (pub/sub)
)

// ─── 跨域 Director Topic（Director 仲裁）───
const (
	TopicTaskAssign     = "coordination.task-assign"
	TopicTaskComplete   = "coordination.task-complete"
	TopicConflictReport = "coordination.conflict"
	TopicHealthCheck    = "coordination.health"
	TopicResourceLock   = "coordination.lock"
	TopicAgentRegister  = "coordination.register"
)

// RoutingPolicy 定义 topic 的路由策略
type RoutingPolicy int

const (
	RoutingP2P        RoutingPolicy = iota // 纯 P2P 直连
	RoutingHybrid                           // P2P 直连 + Director 观察
	RoutingDirector                        // Director 仲裁
)

// DefaultRoutingRules 默认路由规则
var DefaultRoutingRules = map[string]RoutingPolicy{
	TopicSymbolsUpdated:    RoutingHybrid,
	TopicSymbolsRequest:    RoutingP2P,
	TopicSymbolsInvalidate: RoutingHybrid,
	TopicPageStateChanged:  RoutingHybrid,
	TopicPageStateRequest:  RoutingP2P,
	TopicImpactAnalysis:    RoutingP2P,
	TopicFileChanged:       RoutingHybrid,
	TopicTaskAssign:        RoutingDirector,
	TopicTaskComplete:      RoutingDirector,
	TopicConflictReport:    RoutingDirector,
	TopicHealthCheck:       RoutingDirector,
	TopicResourceLock:      RoutingDirector,
	TopicAgentRegister:     RoutingDirector,
}

// IsP2PTopic 判断 topic 是否走 P2P 直连
func IsP2PTopic(topic string) bool {
	policy, ok := DefaultRoutingRules[topic]
	if !ok {
		return false
	}
	return policy == RoutingP2P || policy == RoutingHybrid
}

// IsDirectorTopic 判断 topic 是否需要 Director 仲裁
func IsDirectorTopic(topic string) bool {
	policy, ok := DefaultRoutingRules[topic]
	if !ok {
		return true
	}
	return policy == RoutingDirector
}
