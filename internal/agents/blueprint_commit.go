package agents

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"yagent/internal/blueprint"
	"yagent/internal/gitinfo"
	"yagent/internal/memory"
)

// 本文件是设计稿 §8 的落盘钩子：接在 Repo 完成的那个既有接缝上。
// 仓库级持久知识只有这一个 sink —— KV 仓库记忆与 codeseek 自动沉淀已整体删除。
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

// Commit 折叠本次提交并落盘，返回一句"这一轮蓝图发生了什么"的账目（可空 = 无话可说）。
// 永不返回 error、永不 panic。
//
// 返回值是 2026-10-20 加的第二条出口：`agent_exit` 是终止点，fold 的拒收如果只进 slog，
// 模型不知道自己白交了四条、Director 看到的仍是"这次委派成功了"（实测那一轮 0 次读取、
// 4 条 outside-evidence-set 拒收，账目在日志里，交付物里一个字都没有）。
// 本函数不决定要不要写文件 —— 它只把 fold 已经做出的判据搬回对话。
func (c *BlueprintCommit) Commit(ctx context.Context, agentName string, res AgentResult) string {
	if c == nil {
		return ""
	}
	if len(res.Blueprint) == 0 {
		// "这一轮为什么没有蓝图"必须是日志答得出的问题，而不是留给用户的一个空目录。
		note := c.noSubmissionNote(res)
		if note != "" {
			slog.Info("blueprint: 本次没有提交", "agent", agentName, "reason", note)
		}
		return note
	}
	// §8 纪律 3：只有以 agent_exit 收尾的 run 才有交付物。
	// max_steps / 取消 / 暂停那几条路径上 History 是半轮的，拿它证明模块归属等于编造证据。
	if !isAgentExitResult(res.Text) {
		slog.Info("blueprint: 本次 run 不是 agent_exit 收尾，跳过落盘", "agent", agentName)
		return fmt.Sprintf("本次 run 不是以 agent_exit 收尾，%d 条蓝图散文没有落盘", len(res.Blueprint))
	}
	root := c.view.src.Root()
	if root == "" {
		return ""
	}
	snap := c.view.src.Snapshot()
	if snap == nil {
		// 这是"没更新"，不是"更新失败"：一句可区分的話（I18）。
		slog.Warn("blueprint: 派生还没就绪，本次提交没落盘",
			"agent", agentName, "items", len(res.Blueprint))
		return fmt.Sprintf("派生快照还没就绪，本次 %d 条蓝图提交没落盘（不是被拒收，重跑一次委派即可）", len(res.Blueprint))
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
		return fmt.Sprintf("蓝图 fold 失败，本次不更新：%s", err.Error())
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
	account := blueprintAccount(len(req.Items), len(evidence), foldRes)
	if foldRes.NoOp {
		// "没更新"是一条独立的账：正文逐字没变（同一批结论重播了一次）。
		slog.Info("blueprint: 本次提交没有改变文档", append(attrs, "noop", true)...)
		return account
	}
	slog.Info("blueprint: 已折叠落盘", attrs...)
	return account
}

// blueprintAccount 把 fold 的账目压成一行给 Director 读的话。
//
// 落得干干净净时也说话（"写入 N 条"）—— "这一轮的结论进没进蓝图"是 Director 决定
// 要不要再来一次委派的依据。但本函数不重复任何校验：判据只有 fold 一份，
// 这里只搬它的结论（§11 坑 18 那一类"两份同形实现"就是这么长出来的）。
func blueprintAccount(submitted, evidenceModules int, res *blueprint.FoldResult) string {
	landed := res.Added + res.Updated + res.Supersede + res.Deduped
	parts := []string{fmt.Sprintf("蓝图：提交 %d 条，写入 %d 条（本轮证据覆盖 %d 片）",
		submitted, landed, evidenceModules)}
	if res.NoOp && landed == 0 {
		parts = append(parts, "正文逐字没变，文件未重写")
	}
	if len(res.Rejected) > 0 {
		parts = append(parts, rejectAccount(res.Rejected))
	}
	if res.Stale+res.Unanchored > 0 {
		parts = append(parts, fmt.Sprintf("%d 条锚点判过期、%d 条没有锚点（已写入，只是标了状态）",
			res.Stale, res.Unanchored))
	}
	return strings.Join(parts, "；")
}

// rejectAccount 按原因归堆：一条原因只报一次，带上被点名的片，上限 3 堆 ——
// 一次提交最多 8 条，全堆出来会把对话挤成日志（多余的那几种仍然进 slog）。
func rejectAccount(rejs []blueprint.Rejection) string {
	type tally struct {
		modules []string
		count   int
	}
	var order []string
	byReason := map[string]*tally{}
	for _, r := range rejs {
		key := string(r.Reason)
		t, ok := byReason[key]
		if !ok {
			t = &tally{}
			byReason[key] = t
			order = append(order, key)
		}
		t.count++
		if r.Module != "" && !slices.Contains(t.modules, r.Module) {
			t.modules = append(t.modules, r.Module)
		}
	}
	sort.Strings(order)
	shown := order
	if len(shown) > 3 {
		shown = shown[:3]
	}
	lines := make([]string, 0, len(shown)+1)
	for _, key := range shown {
		t := byReason[key]
		line := fmt.Sprintf("拒收 %d 条：%s", t.count, key)
		if len(t.modules) > 0 {
			line += "（片：" + strings.Join(t.modules, ", ") + "）"
		}
		lines = append(lines, line)
	}
	if extra := len(order) - len(shown); extra > 0 {
		lines = append(lines, fmt.Sprintf("另有 %d 种拒收原因，见日志", extra))
	}
	return strings.Join(lines, "；")
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

// noSubmissionNote 回答"这一轮为什么没有蓝图"。返回空串表示**不该说话**：
// 没装配蓝图（root 空）、或这一轮根本没读过任何目录 —— 后一种情况提不出结论是正常的，
// 给它记一条账只会把真正要紧的那一条淹掉。
//
// 判据只用**入参侧**（工具调用里的路径参数）。出参侧那一遍是为"检索到 8 个、读了 3 个"
// 的提交校验准备的；这里只是解释一句话，不该为它多解析一轮工具结果。
func (c *BlueprintCommit) noSubmissionNote(res AgentResult) string {
	if c == nil || c.view == nil || c.view.src == nil {
		return ""
	}
	root := c.view.src.Root()
	if root == "" {
		return ""
	}
	snap := c.view.src.Snapshot()
	if snap == nil {
		return "派生还没就绪"
	}
	mods := snap.EvidenceModules(openedPaths(res.Memory))
	if len(mods) == 0 {
		return ""
	}
	if !isAgentExitResult(res.Text) {
		// 这条是真实世界最常中的：Repo 用一段文本收尾，写通道（agent_exit.blueprint[]）
		// 因此根本没有被给到机会 —— 与 §11 坑 16（StopOnFinish）是同一个病灶。
		return "本次以纯文本收尾，没走 agent_exit，写通道没有被调用" +
			"（读过 " + strconv.Itoa(len(mods)) + " 片：" + strings.Join(mods, ", ") + "）"
	}
	return "以 agent_exit 收尾但没有 blueprint[] 条目" +
		"（读过 " + strconv.Itoa(len(mods)) + " 片：" + strings.Join(mods, ", ") + "）"
}

// openedPaths 从子 Agent 的记忆里取工具调用参数中的路径（入参侧证据的轻量版）。
func openedPaths(mem []memory.ChatMessage) []string {
	var out []string
	for _, m := range mem {
		for _, tc := range m.ToolCalls {
			out = append(out, pathsFromJSON(tc.Function.Arguments, pathArgKeys)...)
		}
	}
	return out
}
