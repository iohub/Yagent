package config

import "os"

// ConcurrencyEnabled 是"允许并发"的唯一判据。
//
// 环境变量 YAGENT_CONCURRENCY=0 把两层并发一起退回串行：
// ① 只读工具批（agents.NormalizeMaxParallelReadOnlyTools）、
// ② 一次 Director 运行里的委派并发（agents.NormalizeMaxParallelDelegations）。
// 与 YAGENT_BLACKBOARD=0 / YAGENT_RESUME=0 同构：并发出问题时先一键回串行再排查，
// 而不是去改配置文件重启。配置值本身不动，所以关掉再打开不需要改任何东西。
func ConcurrencyEnabled() bool {
	return os.Getenv("YAGENT_CONCURRENCY") != "0"
}
