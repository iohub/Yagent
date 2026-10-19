package blueprint

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

// 本文件是设计稿 §8 的落盘口子 + §14 的多进程策略。
//
// 没有新执行体：不起 goroutine、不排队、不调模型、不需要引擎与档位。
// 它跑在委派返回的那条 goroutine 上，只做"校验 + 合并 + 一次文件写"（ms 级），
// 所以 §8 纪律 2（不能影响任务结果）是由构造保证的，不是靠约定。

// ErrFoldConflict 是乐观 CAS 两次都失败的结果（§14）。
//
// 调用方必须把它当成"这次没写上，下一次自然补上"，而不是任务失败 ——
// 与 §11 坑 22 同族：把降级写成 error，Director 就会以为这次委派失败了，
// 于是把已经做完的工作重做一遍。
var ErrFoldConflict = errors.New("blueprint: 文档在 fold 期间被别的进程改写过，本次放弃")

// commitReReadHook 是 CAS 的测试 seam（生产恒为 nil）。
// 参数是当前尝试的序号：只在第 0 次动手 = 重放成功；每次动手 = 走到放弃那条分支。
var commitReReadHook func(attempt int)

// Commit 读档 → Merge → 归档旧字节 → CAS 写。
//
// CAS 的形状（§14：不引入锁文件 —— 那是"进程崩了留给后人一个解不开的 .lock"那类设计）：
//
//	读 → fold → 写前重读一次 → 字节变了就对着最新内容重放同一个 fold
//
// 重放安全的前提是 fold 幂等 + 段内排序稳定（I20），那两条在 merge.go 里各自有测试。
func Commit(root string, snap *Snapshot, req FoldRequest) (*FoldResult, error) {
	if snap == nil {
		return nil, fmt.Errorf("blueprint: 没有派生快照，fold 无法校验模块归属")
	}
	if root == "" {
		return nil, fmt.Errorf("blueprint: 存储根为空")
	}
	var last *FoldResult
	for attempt := 0; attempt < 2; attempt++ {
		doc, raw, err := LoadForFold(root)
		if err != nil {
			return nil, err
		}
		merged, res := Merge(doc, snap, req, NewAnchorChecker(snap.Root))
		if res.NoOp || res.Added+res.Updated+res.Supersede+res.Deduped == 0 {
			// 两种"什么都没发生"都不写文件：
			//   NoOp —— 正文逐字没变（同一批原子重放）；
			//   计数为零 —— 全部被拒收，此时连一份空文档都不该长出来。
			// 空 fold 也会把 Folds/UpdatedAt 顶一格，那会让"这一轮到底更新没有"
			// 在账目上变得不可回答（I18）。
			// NoOp 在这里补一刀：调用方的日志按它分句，"全部被拒收"绝不能写成"已落盘"。
			res.NoOp = true
			return &res, nil
		}
		data, err := merged.Render()
		if err != nil {
			return &res, err
		}

		// 写前重读：另一个 TUI tab 可能已经写过同一份文档。
		//
		// commitReReadHook 是这条路径的测试 seam —— I20 的 CAS 重放要能被**逼出来**，
		// 而"起两个进程同时 fold"既不确定也进不了 -race。参数是当前尝试的序号（0 起）。
		if commitReReadHook != nil {
			commitReReadHook(attempt)
		}
		cur, err := os.ReadFile(DocPath(root))
		switch {
		case err != nil && !os.IsNotExist(err):
			return &res, err
		case err == nil && !bytes.Equal(cur, raw):
			last = &res
			continue
		}

		if len(raw) > 0 {
			if _, err := SaveRevision(root, raw, req.Now); err != nil {
				// 归档写不下去**不拦本次更新**：真源还是这份文档，
				// 因为 Undo 缺失而丢掉一次好端端的提交是更坏的取舍（§8 纪律 2）。
				res.Rejected = append(res.Rejected, Rejection{Reason: RejectRevision, Detail: err.Error()})
			}
		}
		if err := writeDoc(root, data); err != nil {
			return &res, err
		}
		return &res, nil
	}
	return last, ErrFoldConflict
}
