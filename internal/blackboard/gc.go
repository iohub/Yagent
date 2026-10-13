package blackboard

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GCOptions 是便签板的清理参数。零值都是"不做这件事"，由调用方（config 层）负责填默认值。
type GCOptions struct {
	// TTL 是 board 目录的存活时长：目录 mtime 超过它就整板删除。<=0 表示不按时间清理。
	TTL time.Duration
	// MaxNotesPerBoard 是每个 board 保留的最新便签数，更旧的删除。<=0 表示不限条数。
	MaxNotesPerBoard int
}

// GC 清理 notes 根目录，返回删除的便签数与被删除的 board 数。
//
// 递归删除之所以是安全的，靠三条：**只处理 notes 根目录的一层子目录**；
// symlink 在 DirEntry.IsDir() 上就是 false，永远不会被 RemoveAll 跟进去；
// 正在使用的 board（activeBoardIDs）一律跳过。
func GC(opts GCOptions, activeBoardIDs []string) (notesRemoved, boardsRemoved int, err error) {
	root := NotesRootDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("blackboard: gc read root: %w", err)
	}
	if opts.TTL <= 0 && opts.MaxNotesPerBoard <= 0 {
		return 0, 0, nil
	}

	active := make(map[string]bool, len(activeBoardIDs))
	for _, id := range activeBoardIDs {
		if id = SanitizeBoardID(id); id != "" {
			active[id] = true
		}
	}

	now := time.Now()
	for _, e := range entries {
		name := e.Name()
		// 显式 Lstat 而不是只看 DirEntry 的类型位：本包绝不跟随 symlink，
		// RemoveAll 跟进去删用户别处的目录是不可接受的。
		fi, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			slog.Warn("blackboard: gc refuses to remove a symlink in the notes root",
				"root", root, "entry", name)
			continue
		}
		if !e.IsDir() {
			// 只有根目录这一层的意外条目值得提示：那多半是人手放进去的东西。
			// agent 子目录里的意外内容不播报——那只会把日志刷满。
			slog.Warn("blackboard: gc found a non-directory entry in the notes root, leaving it",
				"root", root, "entry", name)
			continue
		}
		if active[name] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			slog.Warn("blackboard: gc cannot stat board dir, skipping", "board", name, "error", err)
			continue
		}
		expired := opts.TTL > 0 && now.Sub(info.ModTime()) > opts.TTL

		removed, deleted := cleanBoard(filepath.Join(root, name), name, opts.MaxNotesPerBoard, expired)
		notesRemoved += removed
		if deleted {
			boardsRemoved++
		}
	}

	if notesRemoved > 0 || boardsRemoved > 0 {
		slog.Info("blackboard: gc", "notes_removed", notesRemoved, "boards_removed", boardsRemoved,
			"ttl", opts.TTL.String(), "max_notes_per_board", opts.MaxNotesPerBoard)
	}
	return notesRemoved, boardsRemoved, nil
}

// cleanBoard 清理单个 board。
// expired ⇒ 整板 RemoveAll（嵌套目录因此有出口，不会永久堆积）；
// 否则只按条数上限淘汰最旧的 *.md，**不回收目录**——条数淘汰之后目录里通常还有内容，
// 空壳的回收交给 TTL 那条路。
func cleanBoard(boardDir, boardName string, maxNotes int, expired bool) (removed int, deleted bool) {
	notes, err := listBoardNotes(boardDir)
	if err != nil {
		slog.Warn("blackboard: gc cannot scan board, skipping", "board", boardName, "error", err)
		return 0, false
	}

	if expired {
		if err := os.RemoveAll(boardDir); err != nil {
			slog.Warn("blackboard: gc failed to remove expired board", "board", boardName, "error", err)
			return 0, false
		}
		return len(notes), true
	}

	if maxNotes <= 0 || len(notes) <= maxNotes {
		return 0, false
	}
	// 文件名以 UTC 时间戳开头 ⇒ 按名字升序排列后，前面就是最旧的。
	// 注意排序键是文件名而不是整条路径：路径会先按 agent 目录分组的。
	sort.Slice(notes, func(i, j int) bool { return notes[i].name < notes[j].name })
	for _, n := range notes[:len(notes)-maxNotes] {
		if err := os.Remove(n.path); err != nil && !os.IsNotExist(err) {
			slog.Warn("blackboard: gc failed to remove note", "path", n.path, "error", err)
			continue
		}
		removed++
	}
	return removed, false
}

type notePath struct {
	path string
	name string
}

// listBoardNotes 收集 board 下各 agent 目录里的 *.md（不含 *.md.tmp，那是原子写的中间态）。
// 只下探一层；未知内容一概不删，但也不为它写日志。
func listBoardNotes(boardDir string) ([]notePath, error) {
	agents, err := os.ReadDir(boardDir)
	if err != nil {
		return nil, err
	}
	var out []notePath
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		sub := filepath.Join(boardDir, a.Name())
		entries, err := os.ReadDir(sub)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), noteFileExt) {
				continue
			}
			out = append(out, notePath{path: filepath.Join(sub, e.Name()), name: e.Name()})
		}
	}
	return out, nil
}
