package blueprint

import (
	"os"
	"sort"
	"strings"
	"time"
)

// 本文件是 §5.3 的第 2、3 条：孤儿子节的老化清理。
//
// 为什么必须有它：模块集合是**派生**的，所以目录改名/删除后，旧注解永远不会再被扫到，
// 也永远不会被自己删。第 1 条（可见集合 == 派生集合，见 prose.go / index.go）已经让它
// 既渲染不出来也读不到 —— 但那只是止血，文件还会无限长下去。
// 所以清理是"删除一个已经没人能看见的段落"，而不是"删除一条可能有用的结论"。
//
// 形状照 blackboard/gc.go：一层遍历、os.Lstat 拒符号链接、ttl<=0 关闭。
// 只在启动时跑一次（不在任务关键路径上动文件）。

// DefaultOrphanTTL 是 §5.3 的常数 90 天。它不是配置键（§10 把键数钉在 5 个）——
// "留多久"没有用户可观察的语义，只有"删不删"有。
const DefaultOrphanTTL = 90 * 24 * time.Hour

// AgeOutOrphans 把"不在派生集合里、且最后一次被确认已经超过 ttl"的
// `## Modules/<path>` 子节从文档里摘除。
//
// 摘除前整档先落进 revisions/（§5.3 第 3 条：删错了要有 Undo）。
// 返回被摘除的片路径（供调用方打一条 warn —— 静默删除是本仓库反复清理的那类病）。
func AgeOutOrphans(root string, snap *Snapshot, ttl time.Duration, now time.Time) ([]string, error) {
	if snap == nil || ttl <= 0 {
		return nil, nil
	}
	raw, err := os.ReadFile(DocPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 还没有蓝图，没有东西可清
		}
		return nil, err
	}
	doc, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	derived := snap.DerivedModuleSet()
	var doomed []string
	// 坏行不在这里记账：GC 只按段落边界切，段落里坏掉的那一行跟着整段一起处理，
	// fold 侧的五种拒绝账目（§8 纪律 1）才是回报它的地方。
	blocks, _ := doc.Sections()
	for _, b := range blocks {
		if b.Module == "" || derived[b.Module] {
			continue
		}
		if newestSeen(b.Atoms).Add(ttl).After(now) {
			continue // 还没到期：留着，下一轮启动再看
		}
		doomed = append(doomed, b.Module)
	}
	if len(doomed) == 0 {
		return nil, nil
	}

	if _, err := SaveRevision(root, raw, now); err != nil {
		// 归档写不下去就**不删**：没有 Undo 的删除不是清理。
		return nil, err
	}

	newBody := stripModuleSections(doc.Body, doomed)
	fresh := *doc
	fresh.Body = newBody
	fresh.Front.UpdatedAt = now.UTC().Format(time.RFC3339)
	// 指纹表里孤儿那几条一起清掉：留着等于把"这片曾经存在"写进唯一还有它痕迹的地方，
	// 而那份痕迹既不会被比较也没有读者。
	if len(fresh.Front.ModuleFingerprints) > 0 {
		fp := make(map[string]string, len(fresh.Front.ModuleFingerprints))
		for k, v := range fresh.Front.ModuleFingerprints {
			if !contains(doomed, k) {
				fp[k] = v
			}
		}
		fresh.Front.ModuleFingerprints = fp
	}
	if err := Save(root, &fresh); err != nil {
		return nil, err
	}
	sort.Strings(doomed)
	return doomed, nil
}

// stripModuleSections 做行级摘除：从 `## Modules/<path>` 那行起，到下一个 `## ` 或文件末尾。
// 保留段落之外的行（一级标题由 Render 恒定重写，不在这里处理）。
func stripModuleSections(body string, paths []string) string {
	drop := make(map[string]bool, len(paths))
	for _, p := range paths {
		drop[ModuleHeading(p)] = true
	}
	var out []string
	skipping := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			skipping = drop[strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))]
		}
		if skipping {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// newestSeen 取一段里最新的 seen 时间。全部读不出来时返回零值 ——
// 判据是"没有证据说它现在还新"，那么它就没有资格被永久留着。
func newestSeen(atoms []Atom) time.Time {
	var newest time.Time
	for _, a := range atoms {
		t, err := time.Parse(time.RFC3339, a.Seen)
		if err != nil {
			continue
		}
		if t.After(newest) {
			newest = t
		}
	}
	return newest
}

func contains(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
