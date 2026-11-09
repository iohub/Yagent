package main

import (
	"fmt"
	"io"
	"strings"

	"yagent/internal/config"
	"yagent/internal/plasma"
)

// 本文件是三个"store 管理"子命令：manifest（能力清单）、list（store 目录）、
// promote（升档 + 指针切换）。三者的共同点是**不装配 Agent**：只要 config + store。

func runPlasmaManifest(asJSON bool, out io.Writer) error {
	m := (&plasma.Compiler{Palette: plasma.PlasmaPalette}).Manifest()
	return renderPlasmaManifest(out, m, asJSON)
}

// runPlasmaList 列 store。空 store 不是错误（exit 0）：一台还没生成过任何 Agent 的
// 机器上，"还没有"是正确答案，不该让脚本因为它失败。
func runPlasmaList(asJSON bool, out io.Writer) error {
	plasmaRuntime() // 只为了把配置读进全局单例（store 路径可能被显式配过）
	entries := plasma.NewStore(config.PlasmaStoreDir()).List()
	return renderPlasmaList(out, entries, asJSON)
}

// runPlasmaPromote 升档并把 index 指针指过去。
//
// 三条分支的语义：
//   - --force：跳过判据，直接写 promoted；
//   - 已经是 promoted：不再重复升档，只把指针指过去（**回滚**一个旧版本的路径）；
//   - 其余：CanPromote(grade, runs, minRuns) 为真才升。
//
// 判据不过时**不动 index**：一次失败的 promote 静默切换当前版本，比拒绝更危险。
func runPlasmaPromote(name, hash string, minRuns int, force, asJSON bool, out io.Writer) error {
	if strings.TrimSpace(name) == "" {
		return newUsageError("promote 需要 --name <agent 名>")
	}
	settings, _ := plasmaRuntime()
	store := plasma.NewStore(config.PlasmaStoreDir())
	if strings.TrimSpace(hash) == "" {
		current, ok := store.Current(name)
		if !ok {
			return fmt.Errorf("store 里没有 %q", name)
		}
		hash = current
	}
	_, meta, err := store.Load(name, hash)
	if err != nil {
		return fmt.Errorf("load %s@%s: %w", name, hash, err)
	}
	if minRuns <= 0 {
		minRuns = settings.PromoteMinRuns
	}

	var grade plasma.Grade
	upgraded := false
	switch {
	case force:
		grade = plasma.GradePromoted
		upgraded = true
	case meta.Grade == plasma.GradePromoted:
		grade = plasma.GradePromoted
	default:
		if !plasma.CanPromote(meta.Grade, meta.Runs, minRuns) {
			res := &plasmaPromoteResult{Name: name, Hash: hash, Grade: string(meta.Grade),
				Runs: meta.Runs, MinRuns: minRuns, Promoted: false}
			if renderErr := renderPlasmaPromote(out, res, asJSON); renderErr != nil {
				return renderErr
			}
			return fmt.Errorf("%s@%s 未升档：grade=%s runs=%d（需要 verified 且 runs ≥ %d）",
				name, hash, meta.Grade, meta.Runs, minRuns)
		}
		grade = plasma.GradePromoted
		upgraded = true
	}
	if upgraded {
		if err := store.SetGrade(name, hash, grade); err != nil {
			return err
		}
	}
	if err := store.Promote(name, hash); err != nil {
		return err
	}
	return renderPlasmaPromote(out, &plasmaPromoteResult{
		Name: name, Hash: hash, Grade: string(grade), Runs: meta.Runs, MinRuns: minRuns, Promoted: true,
	}, asJSON)
}
