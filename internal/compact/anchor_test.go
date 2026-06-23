package compact

import (
	"sync"
	"testing"
)

// TestNewAnchorSet_Initialization 测试1: NewAnchorSet 初始化正确性
func TestNewAnchorSet_Initialization(t *testing.T) {
	t.Run("empty set", func(t *testing.T) {
		as := NewAnchorSet(0)
		if !as.IsEmpty() {
			t.Errorf("expected empty set, got %d anchors", as.TotalCount())
		}
		if as.UnsummarizedCount() != 0 {
			t.Errorf("expected 0 unsummarized, got %d", as.UnsummarizedCount())
		}
	})

	t.Run("non-empty set", func(t *testing.T) {
		count := 10
		as := NewAnchorSet(count)
		if as.TotalCount() != count {
			t.Errorf("expected %d anchors, got %d", count, as.TotalCount())
		}
		if as.UnsummarizedCount() != count {
			t.Errorf("expected all %d unsummarized, got %d", count, as.UnsummarizedCount())
		}

		// 检查每个锚点的初始状态
		snap := as.Snapshot()
		for i, anchor := range snap {
			if anchor.OriginalIndex != i {
				t.Errorf("anchor[%d].OriginalIndex = %d, want %d", i, anchor.OriginalIndex, i)
			}
			if anchor.IsSummarized {
				t.Errorf("anchor[%d].IsSummarized = true, want false", i)
			}
			if anchor.SummaryRef != -1 {
				t.Errorf("anchor[%d].SummaryRef = %d, want -1", i, anchor.SummaryRef)
			}
		}
	})

	t.Run("negative count clamped to zero", func(t *testing.T) {
		as := NewAnchorSet(-5)
		if as.TotalCount() != 0 {
			t.Errorf("expected 0 anchors for negative count, got %d", as.TotalCount())
		}
	})
}

// TestMarkSummarized_IsSummarized 测试2: MarkSummarized 标记后 IsSummarized 返回正确
func TestMarkSummarized_IsSummarized(t *testing.T) {
	as := NewAnchorSet(10)

	// 初始状态：全部未摘要
	for i := 0; i < 10; i++ {
		if as.IsSummarized(i) {
			t.Errorf("expected anchor[%d] to be unsunnmarized initially", i)
		}
	}

	// 标记区间 [2, 6) 为已摘要
	as.MarkSummarized(2, 6, 1)

	// 验证
	for i := 0; i < 10; i++ {
		expected := i >= 2 && i < 6
		if as.IsSummarized(i) != expected {
			t.Errorf("anchor[%d]: IsSummarized = %v, want %v", i, as.IsSummarized(i), expected)
		}
	}

	// 再标记 [8, 10)
	as.MarkSummarized(8, 10, 2)
	for i := 0; i < 10; i++ {
		expected := (i >= 2 && i < 6) || (i >= 8 && i < 10)
		if as.IsSummarized(i) != expected {
			t.Errorf("anchor[%d] after second mark: IsSummarized = %v, want %v", i, as.IsSummarized(i), expected)
		}
	}

	// 验证 SummaryRef
	snap := as.Snapshot()
	for i, anchor := range snap {
		switch {
		case i >= 2 && i < 6:
			if anchor.SummaryRef != 1 {
				t.Errorf("anchor[%d].SummaryRef = %d, want 1", i, anchor.SummaryRef)
			}
		case i >= 8 && i < 10:
			if anchor.SummaryRef != 2 {
				t.Errorf("anchor[%d].SummaryRef = %d, want 2", i, anchor.SummaryRef)
			}
		default:
			if anchor.SummaryRef != -1 {
				t.Errorf("anchor[%d].SummaryRef = %d, want -1", i, anchor.SummaryRef)
			}
		}
	}
}

// TestNextUnsummarizedRange 测试3&4: NextUnsummarizedRange 区间查询
func TestNextUnsummarizedRange(t *testing.T) {
	t.Run("all unsunnmarized returns entire range", func(t *testing.T) {
		as := NewAnchorSet(5)
		start, end, ok := as.NextUnsummarizedRange(0)
		if !ok {
			t.Error("expected ok=true for fresh set")
		}
		if start != 0 || end != 5 {
			t.Errorf("expected [0:5), got [%d:%d)", start, end)
		}
	})

	t.Run("mark beginning, get rest", func(t *testing.T) {
		as := NewAnchorSet(10)
		as.MarkSummarized(0, 3, 1)

		start, end, ok := as.NextUnsummarizedRange(0)
		if !ok {
			t.Error("expected ok=true")
		}
		if start != 3 || end != 10 {
			t.Errorf("expected [3:10), got [%d:%d)", start, end)
		}
	})

	t.Run("mark middle, verify first interval is returned each time", func(t *testing.T) {
		as := NewAnchorSet(10)
		// 标记中间 [3, 7)
		as.MarkSummarized(3, 7, 1)

		// NextUnsummarizedRange 每次都从头扫描，返回第一个未摘要区间
		// 第一次调用：应返回 [0:3)
		start, end, ok := as.NextUnsummarizedRange(0)
		if !ok || start != 0 || end != 3 {
			t.Errorf("first call: expected [0:3), got [%d:%d], ok=%v", start, end, ok)
		}

		// 第二次调用：仍然返回 [0:3) —— 这不是迭代器，是"查找第一个"
		start, end, ok = as.NextUnsummarizedRange(0)
		if !ok || start != 0 || end != 3 {
			t.Errorf("second call: expected [0:3) (same as first), got [%d:%d], ok=%v", start, end, ok)
		}

		// 用 UnsummarizedRanges 获取所有区间
		ranges := as.UnsummarizedRanges()
		if len(ranges) != 2 {
			t.Fatalf("expected 2 unsummarized ranges, got %d", len(ranges))
		}
		expectedRanges := []AnchorRange{
			{StartIndex: 0, EndIndex: 3},
			{StartIndex: 7, EndIndex: 10},
		}
		for i, exp := range expectedRanges {
			if ranges[i] != exp {
				t.Errorf("range[%d]: got %v, want %v", i, ranges[i], exp)
			}
		}
	})

	t.Run("mark end, get beginning", func(t *testing.T) {
		as := NewAnchorSet(5)
		as.MarkSummarized(3, 5, 1)

		start, end, ok := as.NextUnsummarizedRange(0)
		if !ok || start != 0 || end != 3 {
			t.Errorf("expected [0:3), got [%d:%d)", start, end)
		}
	})

	t.Run("all summarized returns !ok", func(t *testing.T) {
		as := NewAnchorSet(3)
		as.MarkSummarized(0, 3, 1)

		_, _, ok := as.NextUnsummarizedRange(0)
		if ok {
			t.Error("expected ok=false when all summarized")
		}
	})

	t.Run("mark with MarkSummarizedByRange", func(t *testing.T) {
		as := NewAnchorSet(10)
		r := AnchorRange{StartIndex: 2, EndIndex: 5}
		as.MarkSummarizedByRange(r, 1)

		start, end, ok := as.NextUnsummarizedRange(0)
		if !ok || start != 0 || end != 2 {
			t.Errorf("expected [0:2), got [%d:%d)", start, end)
		}
	})
}

// TestAllSummarized_NoUnsummarized 测试5: 全部摘要后 NextUnsummarizedRange 返回 !ok
func TestAllSummarized_NoUnsummarized(t *testing.T) {
	as := NewAnchorSet(7)

	// 分多次标记，确保所有消息都被摘要
	as.MarkSummarized(0, 3, 1)
	as.MarkSummarized(3, 5, 2)
	as.MarkSummarized(5, 7, 3)

	_, _, ok := as.NextUnsummarizedRange(0)
	if ok {
		t.Error("expected ok=false after all messages summarized")
	}

	if as.UnsummarizedCount() != 0 {
		t.Errorf("expected 0 unsummarized, got %d", as.UnsummarizedCount())
	}

	ranges := as.UnsummarizedRanges()
	if len(ranges) != 0 {
		t.Errorf("expected 0 unsummarized ranges, got %d: %v", len(ranges), ranges)
	}
}

// TestExtend 测试6: Extend 扩展后新锚点默认为未摘要
func TestExtend(t *testing.T) {
	as := NewAnchorSet(5)
	as.MarkSummarized(0, 3, 1)

	// 扩展到 10
	as.Extend(10)

	if as.TotalCount() != 10 {
		t.Errorf("expected 10 anchors after extend, got %d", as.TotalCount())
	}

	// 验证原始锚点状态不变
	for i := 0; i < 3; i++ {
		if !as.IsSummarized(i) {
			t.Errorf("anchor[%d] should still be summarized", i)
		}
	}
	for i := 3; i < 5; i++ {
		if as.IsSummarized(i) {
			t.Errorf("anchor[%d] should still be unsunnmarized", i)
		}
	}

	// 验证新增锚点全部未摘要
	for i := 5; i < 10; i++ {
		if as.IsSummarized(i) {
			t.Errorf("new anchor[%d] should be unsunnmarized", i)
		}
	}

	if as.UnsummarizedCount() != 7 { // 2 (3-5) + 5 (5-10) = 7
		t.Errorf("expected 7 unsummarized, got %d", as.UnsummarizedCount())
	}

	// 扩展不会缩小
	as.Extend(3)
	if as.TotalCount() != 10 {
		t.Errorf("extend to smaller count should be no-op, got %d", as.TotalCount())
	}
}

// TestSnapshot_Isolation 测试7: Snapshot 返回的一致性快照不受后续修改影响
func TestSnapshot_Isolation(t *testing.T) {
	as := NewAnchorSet(5)
	as.MarkSummarized(1, 3, 1)

	// 获取快照
	snap1 := as.Snapshot()

	// 修改原始数据
	as.MarkSummarized(3, 5, 2)

	// 验证快照未受影响
	if len(snap1) != 5 {
		t.Fatalf("snapshot length = %d, want 5", len(snap1))
	}
	for i, anchor := range snap1 {
		switch i {
		case 1, 2:
			if !anchor.IsSummarized || anchor.SummaryRef != 1 {
				t.Errorf("snap1[%d]: IsSummarized=%v, SummaryRef=%d, want true, 1",
					i, anchor.IsSummarized, anchor.SummaryRef)
			}
		default:
			if anchor.IsSummarized {
				t.Errorf("snap1[%d]: IsSummarized should be false", i)
			}
		}
	}

	// 验证当前状态已更新
	snap2 := as.Snapshot()
	for i, anchor := range snap2 {
		expected := i >= 1 && i < 5
		if anchor.IsSummarized != expected {
			t.Errorf("snap2[%d].IsSummarized = %v, want %v", i, anchor.IsSummarized, expected)
		}
	}

	// 修改快照不应影响原始数据
	snap1[0].IsSummarized = true
	if as.IsSummarized(0) {
		t.Error("modifying snapshot should not affect original AnchorSet")
	}
}

// TestConcurrentReadWrite 测试8: 并发读写下 -race 无报警
func TestConcurrentReadWrite(t *testing.T) {
	as := NewAnchorSet(100)
	var wg sync.WaitGroup
	numWriters := 5
	numReaders := 10
	opsPerWriter := 50

	// 写协程：交替标记不同区间
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for op := 0; op < opsPerWriter; op++ {
				idx := (writerID*opsPerWriter + op) % 100
				layer := writerID + op
				as.MarkSummarized(idx, idx+1, layer)
			}
		}(w)
	}

	// 读协程：并发查询
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for op := 0; op < opsPerWriter; op++ {
				idx := (readerID*opsPerWriter + op) % 100
				_ = as.IsSummarized(idx)
				_, _, _ = as.NextUnsummarizedRange(0)
				_ = as.UnsummarizedRanges()
				_ = as.UnsummarizedCount()
				_ = as.TotalCount()
				if op%10 == 0 {
					_ = as.Snapshot()
				}
			}
		}(r)
	}

	wg.Wait()

	// 最终一致性检查
	if as.UnsummarizedCount() < 0 || as.UnsummarizedCount() > 100 {
		t.Errorf("final unsummarized count %d is out of range [0, 100]", as.UnsummarizedCount())
	}
}

// TestBoundaryConditions 测试9: 边界条件
func TestBoundaryConditions(t *testing.T) {
	t.Run("empty set operations", func(t *testing.T) {
		as := NewAnchorSet(0)

		if !as.IsEmpty() {
			t.Error("expected empty set")
		}

		if as.TotalCount() != 0 {
			t.Errorf("expected 0 total, got %d", as.TotalCount())
		}

		if as.UnsummarizedCount() != 0 {
			t.Errorf("expected 0 unsummarized, got %d", as.UnsummarizedCount())
		}

		start, end, ok := as.NextUnsummarizedRange(0)
		if ok {
			t.Errorf("expected ok=false for empty set, got [%d:%d)", start, end)
		}

		ranges := as.UnsummarizedRanges()
		if len(ranges) != 0 {
			t.Errorf("expected 0 ranges, got %d", len(ranges))
		}

		as.MarkSummarized(0, 5, 1) // 空集合上操作不应 panic
	})

	t.Run("negative index", func(t *testing.T) {
		as := NewAnchorSet(10)

		if as.IsSummarized(-1) {
			t.Error("IsSummarized(-1) should return false")
		}
		if as.IsSummarized(-100) {
			t.Error("IsSummarized(-100) should return false")
		}
	})

	t.Run("out of bounds index", func(t *testing.T) {
		as := NewAnchorSet(10)

		if as.IsSummarized(10) {
			t.Error("IsSummarized(10) should return false (out of bounds)")
		}
		if as.IsSummarized(1000) {
			t.Error("IsSummarized(1000) should return false (out of bounds)")
		}
	})

	t.Run("MarkSummarized with negative start", func(t *testing.T) {
		as := NewAnchorSet(10)
		as.MarkSummarized(-5, 3, 1) // 应被截断到 [0, 3)

		for i := 0; i < 3; i++ {
			if !as.IsSummarized(i) {
				t.Errorf("anchor[%d] should be summarized", i)
			}
		}
		for i := 3; i < 10; i++ {
			if as.IsSummarized(i) {
				t.Errorf("anchor[%d] should not be summarized", i)
			}
		}
	})

	t.Run("MarkSummarized with end beyond length", func(t *testing.T) {
		as := NewAnchorSet(5)
		as.MarkSummarized(3, 100, 1) // 应被截断到 [3, 5)

		for i := 0; i < 3; i++ {
			if as.IsSummarized(i) {
				t.Errorf("anchor[%d] should not be summarized", i)
			}
		}
		for i := 3; i < 5; i++ {
			if !as.IsSummarized(i) {
				t.Errorf("anchor[%d] should be summarized", i)
			}
		}
	})

	t.Run("MarkSummarized with start >= end", func(t *testing.T) {
		as := NewAnchorSet(5)
		as.MarkSummarized(3, 3, 1) // start == end
		as.MarkSummarized(3, 2, 1) // start > end

		for i := 0; i < 5; i++ {
			if as.IsSummarized(i) {
				t.Errorf("anchor[%d] should not be summarized", i)
			}
		}
	})

	t.Run("Reset", func(t *testing.T) {
		as := NewAnchorSet(5)
		as.MarkSummarized(0, 5, 1)

		as.Reset()

		if as.UnsummarizedCount() != 5 {
			t.Errorf("expected 5 unsummarized after reset, got %d", as.UnsummarizedCount())
		}
		for i := 0; i < 5; i++ {
			if as.IsSummarized(i) {
				t.Errorf("anchor[%d] should be unsunnmarized after reset", i)
			}
		}

		snap := as.Snapshot()
		for _, anchor := range snap {
			if anchor.SummaryRef != -1 {
				t.Errorf("anchor.SummaryRef should be -1 after reset, got %d", anchor.SummaryRef)
			}
		}
	})

	t.Run("AnchorRange IsValid", func(t *testing.T) {
		tests := []struct {
			name   string
			r      AnchorRange
			valid  bool
		}{
			{"valid", AnchorRange{StartIndex: 0, EndIndex: 5}, true},
			{"zero length", AnchorRange{StartIndex: 3, EndIndex: 3}, true},
			{"negative start", AnchorRange{StartIndex: -1, EndIndex: 5}, false},
			{"start > end", AnchorRange{StartIndex: 5, EndIndex: 3}, false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.r.IsValid() != tt.valid {
					t.Errorf("AnchorRange{%d:%d}.IsValid() = %v, want %v",
						tt.r.StartIndex, tt.r.EndIndex, tt.r.IsValid(), tt.valid)
				}
			})
		}
	})

	t.Run("AnchorRange String", func(t *testing.T) {
		r := AnchorRange{StartIndex: 2, EndIndex: 5}
		if s := r.String(); s != "[2:5]" {
			t.Errorf("AnchorRange.String() = %q, want %q", s, "[2:5]")
		}
	})

	t.Run("AnchorRange Len", func(t *testing.T) {
		tests := []struct {
			r      AnchorRange
			length int
		}{
			{AnchorRange{StartIndex: 0, EndIndex: 5}, 5},
			{AnchorRange{StartIndex: 3, EndIndex: 3}, 0},
			{AnchorRange{StartIndex: 5, EndIndex: 3}, 0},
		}

		for _, tt := range tests {
			if tt.r.Len() != tt.length {
				t.Errorf("AnchorRange{%d:%d}.Len() = %d, want %d",
					tt.r.StartIndex, tt.r.EndIndex, tt.r.Len(), tt.length)
			}
		}
	})
}

// TestNewAnchorSetFromSnapshots 测试从快照恢复
func TestNewAnchorSetFromSnapshots(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		as := NewAnchorSetFromSnapshots(nil)
		if !as.IsEmpty() {
			t.Error("expected empty set from nil input")
		}
	})

	t.Run("recover with data", func(t *testing.T) {
		original := NewAnchorSet(5)
		original.MarkSummarized(1, 3, 2)

		snap := original.Snapshot()
		recovered := NewAnchorSetFromSnapshots(snap)

		if recovered.TotalCount() != 5 {
			t.Errorf("expected 5 anchors, got %d", recovered.TotalCount())
		}
		for i, expected := range map[int]bool{
			0: false, 1: true, 2: true, 3: false, 4: false,
		} {
			if recovered.IsSummarized(i) != expected {
				t.Errorf("recovered[%d]: IsSummarized = %v, want %v", i, recovered.IsSummarized(i), expected)
			}
		}

		// 验证 SummaryRef
		recoveredSnap := recovered.Snapshot()
		if recoveredSnap[1].SummaryRef != 2 {
			t.Errorf("recovered[1].SummaryRef = %d, want 2", recoveredSnap[1].SummaryRef)
		}
	})

	t.Run("SummaryRef zero correction", func(t *testing.T) {
		anchors := []MessageAnchor{
			{OriginalIndex: 0, IsSummarized: false, SummaryRef: 0}, // 应为 -1
			{OriginalIndex: 1, IsSummarized: true, SummaryRef: 0},  // 保留为 0（合法层号）
		}
		as := NewAnchorSetFromSnapshots(anchors)
		snap := as.Snapshot()

		if snap[0].SummaryRef != -1 {
			t.Errorf("snap[0].SummaryRef = %d, want -1 (was 0 and not summarized)", snap[0].SummaryRef)
		}
		if snap[1].SummaryRef != 0 {
			t.Errorf("snap[1].SummaryRef = %d, want 0 (was 0 and summarized)", snap[1].SummaryRef)
		}
	})
}

// TestUnsummarizedRanges 测试 UnsummarizedRanges 完整功能
func TestUnsummarizedRanges(t *testing.T) {
	t.Run("all unsunnmarized returns single range", func(t *testing.T) {
		as := NewAnchorSet(10)
		ranges := as.UnsummarizedRanges()
		if len(ranges) != 1 {
			t.Fatalf("expected 1 range, got %d", len(ranges))
		}
		if ranges[0] != (AnchorRange{StartIndex: 0, EndIndex: 10}) {
			t.Errorf("expected [0:10), got %v", ranges[0])
		}
	})

	t.Run("split by summarized middle", func(t *testing.T) {
		as := NewAnchorSet(10)
		as.MarkSummarized(3, 7, 1)

		ranges := as.UnsummarizedRanges()
		if len(ranges) != 2 {
			t.Fatalf("expected 2 ranges, got %d", len(ranges))
		}
		expected := []AnchorRange{
			{StartIndex: 0, EndIndex: 3},
			{StartIndex: 7, EndIndex: 10},
		}
		for i, exp := range expected {
			if ranges[i] != exp {
				t.Errorf("range[%d]: got %v, want %v", i, ranges[i], exp)
			}
		}
	})

	t.Run("multiple gaps", func(t *testing.T) {
		as := NewAnchorSet(12)
		as.MarkSummarized(0, 2, 1)
		as.MarkSummarized(5, 7, 2)
		as.MarkSummarized(10, 12, 3)

		ranges := as.UnsummarizedRanges()
		expected := []AnchorRange{
			{StartIndex: 2, EndIndex: 5},
			{StartIndex: 7, EndIndex: 10},
		}
		if len(ranges) != len(expected) {
			t.Fatalf("expected %d ranges, got %d", len(expected), len(ranges))
		}
		for i, exp := range expected {
			if ranges[i] != exp {
				t.Errorf("range[%d]: got %v, want %v", i, ranges[i], exp)
			}
		}
	})
}

// BenchmarkAnchorSet 性能基准测试
func BenchmarkNewAnchorSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewAnchorSet(1000)
	}
}

func BenchmarkMarkSummarized(b *testing.B) {
	as := NewAnchorSet(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		as.MarkSummarized(i%1000, (i%1000)+1, i)
	}
}

func BenchmarkNextUnsummarizedRange(b *testing.B) {
	as := NewAnchorSet(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = as.NextUnsummarizedRange(0)
	}
}

func BenchmarkConcurrentAccess(b *testing.B) {
	as := NewAnchorSet(100)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = as.IsSummarized(i % 100)
			_, _, _ = as.NextUnsummarizedRange(0)
			i++
		}
	})
}
