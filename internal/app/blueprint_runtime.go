package app

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"yagent/internal/agents"
	"yagent/internal/blueprint"
	"yagent/internal/config"
	"yagent/internal/gitinfo"
	"yagent/internal/supervise"
	"yagent/internal/tokenutil"
)

// blueprintRuntime 是本进程对"仓库蓝图"的全部持有：派生循环 + 存储根 + 生效配置。
//
// 三件事在这里收口，而不是散到各 Agent 的构造函数里：
//  1. **唯一启用判据**：config.BlueprintEnabled()（I8）。关掉就整块不建 —— 派生、注入、
//     工具、落盘钩子、GC 五处共用同一个 nil 结果，而不是各读一遍开关。
//  2. **唯一 goroutine 归属**：派生循环挂在具名 supervise 域上，Yagent.Close() 等它退。
//     `--prompt` 那条一次性路径全靠这一点才能干净收尾（裸 go 由 check_no_bare_go.sh 硬禁）。
//  3. **唯一存储根**：resolveBlueprintRoot 的三条结果（关闭 / 护栏拒绝 / 就绪）在这里分派。
type blueprintRuntime struct {
	root    string
	deriver *blueprint.Deriver
	cfg     config.BlueprintConfig

	grp  *supervise.Group
	once sync.Once
}

// buildBlueprint 装配蓝图运行时。返回 nil 表示"这个项目没有蓝图"，
// 调用方（三处注入与落盘钩子）拿到 nil 时退回改动前的行为。
func (ca *Yagent) buildBlueprint() *blueprintRuntime {
	if !config.BlueprintEnabled() {
		slog.Info("blueprint: 已关闭（[blueprint] enable=false 或 YAGENT_BLUEPRINT=0）")
		return nil
	}
	root, err := ca.resolveBlueprintRoot()
	if err != nil {
		slog.Warn("blueprint: 本项目不建蓝图", "project_path", ca.globalCtx.ProjectPath, "reason", err.Error())
		return nil
	}
	if root == "" {
		return nil
	}
	projectPath := ca.globalCtx.ProjectPath

	d := blueprint.NewDeriver(blueprint.DeriveSpec{
		ProjectPath: projectPath,
		RepoToken:   repoTokenProbe(projectPath),
	})

	// 启动时**同步**派生一次：孤儿老化要快照，而"第一个委派只看到 scanning…"的代价
	// 比这一次扫描（本仓实测 6.6ms）大得多。之后的一切都在后台循环里。
	if err := d.DeriveOnce(context.Background()); err != nil {
		slog.Warn("blueprint: 首次派生失败，本轮不建索引", "project_path", projectPath, "error", err.Error())
		return nil
	}

	if err := blueprint.EnsureRoot(root); err != nil {
		slog.Warn("blueprint: 存储根建不出来", "root", root, "error", err.Error())
	} else if removed, err := blueprint.AgeOutOrphans(root, d.Snapshot(), blueprint.DefaultOrphanTTL, time.Now()); err != nil {
		slog.Warn("blueprint: 孤儿子节老化失败", "root", root, "error", err.Error())
	} else if len(removed) > 0 {
		// 静默删除是本仓库反复清理的那类病（§8 纪律 1）：摘了什么必须报名。
		slog.Info("blueprint: 已摘除超期孤儿子节", "modules", removed, "root", root)
	}

	grp, _ := supervise.New(context.Background(), "blueprint")
	grp.Go("blueprint/derive", func(ctx context.Context) { d.Run(ctx) })

	cfg := config.GlobalBlueprint()
	st := d.CurrentStatus()
	slog.Info("blueprint: 派生起跑",
		"root", root, "modules", st.Modules, "cost", st.Cost.String(),
		"cached", st.CachedMode, "index_max_tokens", cfg.IndexMaxTokens,
		"prose_budget_tokens", cfg.ProseBudgetTokens, "inject_into", cfg.InjectInto)
	return &blueprintRuntime{root: root, deriver: d, cfg: cfg, grp: grp}
}

// stop 停派生循环并等它退。幂等（Yagent.Close 可能被多家调用）。
func (r *blueprintRuntime) stop() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.deriver.Close()
		// 一次扫描最坏是 ScanDeadline(2s)。等不到就报名字，不无限期挂住退出。
		if leaked := r.grp.Drain(3 * time.Second); len(leaked) > 0 {
			slog.Warn("blueprint: 退出时派生循环仍在跑", "leases", supervise.LeaseNames(leaked))
		}
	})
}

// BlueprintSource 的三个方法（agents.ports.go 的窄接口）。
func (r *blueprintRuntime) Request() {
	if r == nil {
		return
	}
	r.deriver.Request()
}

func (r *blueprintRuntime) Snapshot() *blueprint.Snapshot {
	if r == nil {
		return nil
	}
	return r.deriver.Snapshot()
}

func (r *blueprintRuntime) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

// Status 给 read_blueprint 的回执头部与将来的 `/blueprint` 用。
func (r *blueprintRuntime) Status() blueprint.Status {
	if r == nil {
		return blueprint.Status{}
	}
	return r.deriver.CurrentStatus()
}

// viewFor 按 [blueprint] inject_into 决定这个 Agent 拿不拿常驻骨架。
// 站点名不在列表里时 inject=false，但视图照样建出来 —— 委派前缀与 read_blueprint
// 不走 inject_into（那是"常驻太贵先摘掉某个 Agent"的阀门，不是关整套机制）。
func (r *blueprintRuntime) viewFor(site string) *agents.BlueprintView {
	if r == nil {
		return nil
	}
	inject := false
	for _, s := range r.cfg.InjectInto {
		if s == site {
			inject = true
			break
		}
	}
	return agents.NewBlueprintView(r, site, inject, r.cfg.IndexMaxTokens, r.cfg.ProseBudgetTokens, tokenutil.EstimateTokens)
}

// repoTokenProbe 返回"仓库动了就会变"的读数（§5.1 的缓存失效判据）。
//
// git 仓库用 HEAD 的 sha（一次 rev-parse ~10ms）；非 git 退化成项目根 mtime ——
// 两者都便宜到可以在每轮注入前问一次。返回 "" 会让缓存模式永远不重扫，
// 所以这里必须给真退化值（I9：非 git 仓库照常工作）。
func repoTokenProbe(projectPath string) func() string {
	return func() string {
		if info := gitinfo.Collect(context.Background(), projectPath); info.SHA != "" {
			return info.SHA
		}
		if st, err := os.Stat(projectPath); err == nil {
			return st.ModTime().UTC().Format(time.RFC3339Nano)
		}
		return ""
	}
}
