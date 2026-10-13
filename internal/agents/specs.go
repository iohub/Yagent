package agents

import (
	"context"

	"yagent/internal/toolspec"
)

// git 快照工具。执行器由 Coding 每次 Run 用当次的 GitCheckpointManager 绑定，
// 因此这里只声明能力与档位；绑定缺失（非 git 仓库或配置关闭）即不可用。
//
// 三个都放 Cold：一次编码任务里通常最多用一两次，而回滚能力恰恰是"出问题时才需要"，
// 每步都发全量声明是给不发生的收益付常驻成本。
func init() {
	for _, s := range []struct {
		name    string
		mutates bool
		brief   string
		pick    func(*toolspec.Deps) toolspec.ToolFunc
	}{
		{"git_checkpoint_list", false, "列举本会话可用的 git 快照点", func(d *toolspec.Deps) toolspec.ToolFunc { return d.CheckpointList }},
		{"git_checkpoint_create", true, "在当前状态手动创建快照点", func(d *toolspec.Deps) toolspec.ToolFunc { return d.CheckpointCreate }},
		{"git_checkpoint_rollback", true, "回滚到指定快照点（丢弃其后改动）", func(d *toolspec.Deps) toolspec.ToolFunc { return d.CheckpointRollback }},
	} {
		spec := s // 闭包捕获副本，Go 1.22 前的循环变量语义不假设
		toolspec.MustRegister(toolspec.Spec{
			Name:      spec.name,
			Effect:    toolspec.EffectCheckpoint,
			Frequency: toolspec.Cold,
			Mutates:   spec.mutates,
			Serial:    true, // git 子进程操作同一个工作树
			Brief:     spec.brief,
			Available: func(d *toolspec.Deps) (bool, string) {
				if spec.pick(d) == nil {
					return false, "当前项目不是 git 仓库或 [git_checkpoint] 未启用"
				}
				return true, ""
			},
			Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
				fn := spec.pick(d)
				if fn == nil {
					return nil, errNoBinding(spec.name)
				}
				return fn, nil
			},
		})
	}

	registerDelegateSpecs()
	registerContextSpecs()
}

// 委派类工具。执行器是 d.Delegate 这一个泛化步骤（Director 提供完整流水线：
// context_refs 解析 → 便签 → 档位路由 → 超预算落盘；Repo 只提供自己的 devops 直连实现）。
//
// Audience 显式声明受众：今天"Coding 只能委派 browser、Repo 只能委派 devops"
// 是靠各 Agent 的 switch 硬编码的，收到 Spec 上之后新增委派必须写明给谁用。
//
// Mutates 的取值即历史并发语义：delegate_repo/chat 当年被显式 WithReadOnly(true)
// （纯分析，可并行），coding/devops/browser 会改状态，meta 会改 Director 的工具表。
func registerDelegateSpecs() {
	for _, s := range []struct {
		name     string
		target   string
		audience []string
		mutates  bool
		serial   bool
	}{
		{"delegate_repo", "repo", []string{"director"}, false, false},
		{"delegate_coding", "coding", []string{"director"}, true, false},
		{"delegate_chat", "chat", []string{"director"}, false, false},
		{"delegate_devops", "devops", []string{"director", "repo"}, true, false},
		{"delegate_meta", "meta", []string{"director"}, false, true},
		{"delegate_browser", "browser", []string{"director", "coding"}, true, false},
	} {
		spec := s
		toolspec.MustRegister(toolspec.Spec{
			Name:      spec.name,
			Effect:    toolspec.EffectDelegate,
			Frequency: toolspec.Hot,
			Mutates:   spec.mutates,
			Serial:    spec.serial,
			Audience:  spec.audience,
			Brief:     "把任务委派给 " + spec.target + " 子 Agent",
			Available: delegateAvailable(spec.target),
			Build:     buildDelegate(spec.target),
		})
	}
}

func delegateAvailable(target string) func(*toolspec.Deps) (bool, string) {
	return func(d *toolspec.Deps) (bool, string) {
		if d.Delegate == nil {
			return false, "当前 Agent 不具备委派能力"
		}
		if d.IsDisabled(target) {
			return false, "子 Agent " + target + " 已被 --disable-agents 关闭"
		}
		return true, ""
	}
}

func buildDelegate(target string) func(*toolspec.Deps) (toolspec.ToolFunc, error) {
	return func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
		if d.Delegate == nil {
			return nil, errNoBinding(target)
		}
		return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			return d.Delegate(ctx, target, params)
		}, nil
	}
}

// 跨 Agent 便签与超预算产物回读。今天由 executor 在 ctx 带 boardID 时运行期注入，
// 现在收敛成能力标签 + Available：语义相同，但注册点只剩一处。
func registerContextSpecs() {
	for _, s := range []struct {
		name  string
		brief string
	}{
		{"read_note", "按引用读取其他 Agent 的便签全文"},
		{"read_artifact", "分页读取超预算的委派结果全文"},
	} {
		toolspec.MustRegister(toolspec.Spec{
			Name:      s.name,
			Effect:    toolspec.EffectContext,
			Frequency: toolspec.Warm,
			Brief:     s.brief,
			Available: func(d *toolspec.Deps) (bool, string) {
				if !d.HasBoard() {
					return false, "本次运行没有 blackboard 便签板"
				}
				return true, ""
			},
			Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
				boardID := d.BoardID
				if s.name == "read_note" {
					return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
						return executeReadNote(ctx, params, func() string { return boardID })
					}, nil
				}
				return executeReadArtifact, nil
			},
		})
	}
}

type noBindingError struct{ what string }

func (e noBindingError) Error() string { return e.what + " 未绑定执行器" }

func errNoBinding(what string) error { return noBindingError{what: what} }
