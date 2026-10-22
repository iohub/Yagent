package agents

// spawn.go — 每次委派一个新实例（docs/structured-concurrency-design.md D2）。
//
// 为什么这件事值得做：内建子 Agent 的结构体几乎全是**不可变接线**
// （promptFmt / env / params / maxSteps / engine / guard …），
// 而 Run() 里一处都不写自身字段（五家逐个核过）。唯一的例外是 CodingAgent.thinkLink ——
// 它挂在构造出来的单例上，于是：
//   ① 两次 Coding 委派共用一份思维日志，第三档压缩的重建原料里混着**上一次委派**的任务。
//      AGENTS.md §6.5 明写 Browser/Repo/DevOps 的 store 必须按委派建，就是为了避免这件事，
//      Coding 是唯一漏网的那一个。
//   ② 委派一旦被并行化（C6），并发写同一份 store 就是数据竞态。
//
// 规则："浅拷不可变接线，新建可变状态"。嵌套的 Agent 指针（Repo→DevOps、Coding→Browser）
// 不能跟着浅拷了事 —— 拷来的仍是同一个共享单例，隔离是假的，所以它们各自再 Spawn 一次。
//
// 四个 Agent 目前没有任何可变状态，克隆看起来是空动作；仍然统一做，理由是把
// "委派从不碰共享实例"变成一条**不需要记住**的不变量：以后谁往结构体上加可变字段，
// 默认就是每委派一份，而不是像 thinkLink 那样悄悄共享到第一次并行。

import (
	"context"

	"yagent/internal/thinklink"
)

// SpawnForDelegation 返回一个用于本次委派的 RepoAgent。
// 没有可变状态可重置（surface 与 thinklink store 都在 Run() 里按委派建）。
func (a *RepoAgent) SpawnForDelegation(_ context.Context) *RepoAgent {
	if a == nil {
		return nil // 未装配：与改动前"取到 nil 再报友好提示"的路径保持一致
	}
	cp := *a
	return &cp
}

// SpawnForDelegation 返回一个用于本次委派的 ChatAgent。
func (a *ChatAgent) SpawnForDelegation(_ context.Context) *ChatAgent {
	if a == nil {
		return nil // 未装配：与改动前"取到 nil 再报友好提示"的路径保持一致
	}
	cp := *a
	return &cp
}

// SpawnForDelegation 返回一个用于本次委派的 DevOpsAgent。
func (a *DevOpsAgent) SpawnForDelegation(_ context.Context) *DevOpsAgent {
	if a == nil {
		return nil // 未装配：与改动前"取到 nil 再报友好提示"的路径保持一致
	}
	cp := *a
	return &cp
}

// SpawnForDelegation 返回一个用于本次委派的 BrowserAgent。
// 浏览器侧的隔离本来就按 delegationID 分（browserctl 的工作区、chrome 句柄都在 manager 里
// 以委派坐标为键），这里克隆只是为了与其余几家同构。
func (a *BrowserAgent) SpawnForDelegation(_ context.Context) *BrowserAgent {
	if a == nil {
		return nil // 未装配：与改动前"取到 nil 再报友好提示"的路径保持一致
	}
	cp := *a
	return &cp
}

// SpawnForDelegation 返回一个用于本次委派的 CodingAgent，并**换一份新的 thinklink store**。
//
// 这一句就是整个文件存在的理由：store 挂在单例上时，上一次委派的用户输入与 T&P 块会进入
// 这一次委派的第三档压缩重建（跨任务串味），并行时更是直接竞态。
func (a *CodingAgent) SpawnForDelegation(_ context.Context) *CodingAgent {
	if a == nil {
		return nil // 未装配：与改动前"取到 nil 再报友好提示"的路径保持一致
	}
	cp := *a
	cp.thinkLink = thinklink.NewStore(0) // 容量 0 = 全量，与构造时同参数
	return &cp
}

// devopsFor 取一个本次调用专用的 DevOps 实例（Repo → delegate_devops）。
// 返回 nil 表示没装配 —— 调用方必须像原来一样退回友好提示，而不是 panic。
func (a *RepoAgent) devopsFor(ctx context.Context) *DevOpsAgent {
	if a.devops == nil {
		return nil
	}
	return a.devops.SpawnForDelegation(ctx)
}

// browserFor 取一个本次调用专用的 Browser 实例（Coding → delegate_browser）。
func (a *CodingAgent) browserFor(ctx context.Context) *BrowserAgent {
	if a.BrowserAgent == nil {
		return nil
	}
	return a.BrowserAgent.SpawnForDelegation(ctx)
}
