package agents

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/blueprint"
	"yagent/internal/gitinfo"
	"yagent/internal/memory"
)

// 本文件是设计稿 §8 的落盘钩子：接在 Repo 完成的那个既有接缝上。
//
// 那三行今天已经在做同一件事的另外两种版本（worker.Submit 写 KV 记忆、
// autoConsolidateSubtask 写 codeseek 知识库），蓝图是**同一时点的第三个 sink**。
//
// 四条实现纪律在这里的落点：
//   1. 不许静默丢失 —— 每条拒收各打一条 warn（带 module 与违规 token），
//      整次 fold 的账目也打一条；"一条都没更新"与"更新失败"必须是两句不同的话（I18）。
//   2. 不能影响任务结果 —— 全程只 warn，绝不向 Repo 的返回值传播错误（I11）。
//   3. 暂停/取消/max_steps 不写 —— 调用点在 agent_exit 收尾那条路径上，
//      且这里再挡一次非 agent_exit 的形态（I11）。
//   4. 没有任何新执行体：不起 goroutine、不排队、不调模型。

// BlueprintCommit 持有"往哪块盘、用哪份快照"两件事。
// 未接线（nil）时整条路径是 no-op —— 与黑图、知识注入那两处 sink 同形。
type BlueprintCommit struct {
	view *BlueprintView
}

// NewBlueprintCommit 建钩子。view 为 nil 时返回 nil（方法对 nil 接收者安全）。
func NewBlueprintCommit(view *BlueprintView) *BlueprintCommit {
	if view == nil || view.src == nil {
		return nil
	}
	return &BlueprintCommit{view: view}
}

// Commit 折叠本次提交并落盘。永不返回 error、永不 panic。
func (c *BlueprintCommit) Commit(ctx context.Context, agentName string, res AgentResult) {
	if c == nil || len(res.Blueprint) == 0 {
		return
	}
	// §8 纪律 3：只有以 agent_exit 收尾的 run 才有交付物。
	// max_steps / 取消 / 暂停那几条路径上 History 是半轮的，拿它证明模块归属等于编造证据。
	if !isAgentExitResult(res.Text) {
		slog.Info("blueprint: 本次 run 不是 agent_exit 收尾，跳过落盘", "agent", agentName)
		return
	}
	root := c.view.src.Root()
	if root == "" {
		return
	}
	snap := c.view.src.Snapshot()
	if snap == nil {
		// 这是"没更新"，不是"更新失败"：一句可区分的話（I18）。
		slog.Warn("blueprint: 派生还没就绪，本次提交没落盘",
			"agent", agentName, "items", len(res.Blueprint))
		return
	}

	evidence := map[string]bool{}
	for _, m := range snap.EvidenceModules(res.EvidenceFiles) {
		evidence[m] = true
	}
	info := gitinfo.Collect(ctx, snap.Root)
	req := blueprint.FoldRequest{
		Items:       res.Blueprint,
		Src:         blueprintSrc(ctx, agentName),
		Now:         time.Now().UTC(),
		Evidence:    evidence,
		ProjectID:   docProjectID(root, snap),
		ProjectPath: snap.Root,
		GitSHA:      info.SHA,
		GitBranch:   info.Branch,
		GitOrigin:   info.OriginURL,
	}

	foldRes, err := blueprint.Commit(root, snap, req)
	if err != nil {
		// §8 纪律 2：IO 失败只 warn。降级不是错误（§11 坑 22 同族）——
		// 把它向上抛，Director 就会以为这次委派失败了，然后把已经做完的工作重做一遍。
		slog.Warn("blueprint: fold 失败，本次不更新", "root", root, "error", err.Error())
		return
	}
	// 拒收逐条报名（带 module 与违规 token）：静默丢失的反面教训是 thinklink。
	for _, rej := range foldRes.Rejected {
		slog.Warn("blueprint: 提交被拒收", "agent", agentName, "reason", string(rej.Reason),
			"module", rej.Module, "detail", rej.Detail)
	}
	attrs := []any{
		"agent", agentName, "root", root,
		"added", foldRes.Added, "updated", foldRes.Updated, "deduped", foldRes.Deduped,
		"superseded", foldRes.Supersede, "stale", foldRes.Stale, "unanchored", foldRes.Unanchored,
		"rejected", len(foldRes.Rejected), "evicted", foldRes.Evicted,
	}
	if foldRes.NoOp {
		// "没更新"是一条独立的账：正文逐字没变（同一批结论重播了一次）。
		slog.Info("blueprint: 本次提交没有改变文档", append(attrs, "noop", true)...)
		return
	}
	slog.Info("blueprint: 已折叠落盘", attrs...)
}

// blueprintSrc 是 `{agent}@{会话坐标前 8 位}`：出处而非权限（§3.2）。
// 坐标取委派 ID —— 它同时是 rollout 文件名与父子串联用的那一个，
// 所以"这条是谁在哪个 run 里写的"在三个存储里指同一件事。
func blueprintSrc(ctx context.Context, agentName string) string {
	// 归一成 knowledge/rollout 那两处一直在用的写法（"Repo-Agent" → "repo_agent"）：
	// src 是出处坐标，它会出现在文档正文里，两种拼法混着写就没法按 agent 聚合。
	agent := strings.ToLower(strings.NewReplacer(" ", "_", "-", "_").Replace(agentName))
	id := memory.DelegationIDFrom(ctx)
	if id == "" {
		return agent
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return agent + "@" + id
}

// docProjectID 优先沿用它已写进文档的那份身份（rollout 与蓝图共用同一把主键，I12）。
// 新文档时从存储根末段取 —— 那正是 blueprint.Root(projectID) 放进去的那一段，
// 于是"第一份文档的 project_id"与"它所在的目录名"必然是同一个字符串。
func docProjectID(root string, snap *blueprint.Snapshot) string {
	if doc, err := blueprint.Load(root); err == nil && doc != nil && doc.Front.ProjectID != "" {
		return doc.Front.ProjectID
	}
	return filepath.Base(filepath.Clean(root))
}
