package toolbatch

import (
	"reflect"
	"testing"
)

// segSlices 便于断言的段结构展开
func segSlices(segs []Segment) ([2][]any) {
	out := [2][]any{}
	for _, s := range segs {
		if s.Parallel {
			out[0] = append(out[0], append([]int(nil), s.Indices...))
		} else {
			out[1] = append(out[1], append([]int(nil), s.Indices...))
		}
	}
	return out
}

func TestPlan_AllParallel(t *testing.T) {
	segs := Plan(3, func(i int) bool { return true })
	if len(segs) != 1 || !segs[0].Parallel {
		t.Fatalf("want single parallel segment, got %+v", segs)
	}
	if !reflect.DeepEqual(segs[0].Indices, []int{0, 1, 2}) {
		t.Fatalf("want indices [0 1 2], got %v", segs[0].Indices)
	}
}

func TestPlan_AllSerial(t *testing.T) {
	segs := Plan(3, func(i int) bool { return false })
	if len(segs) != 3 {
		t.Fatalf("want 3 serial segments, got %d: %+v", len(segs), segs)
	}
	for i, s := range segs {
		if s.Parallel {
			t.Fatalf("segment %d should be serial", i)
		}
		if !reflect.DeepEqual(s.Indices, []int{i}) {
			t.Fatalf("segment %d indices = %v, want [%d]", i, s.Indices, i)
		}
	}
}

func TestPlan_Mixed(t *testing.T) {
	// 模式 [p p w p] → 并行[0,1]、串行[2]、并行[3]
	segs := Plan(4, func(i int) bool { return i != 2 })
	got := segSlices(segs)
	want := [2][]any{ {[]int{0, 1}, []int{3}}, {[]int{2}} }
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestPlan_Empty(t *testing.T) {
	if segs := Plan(0, func(i int) bool { return true }); segs != nil {
		t.Fatalf("want nil for total=0, got %+v", segs)
	}
	if segs := Plan(-1, func(i int) bool { return true }); segs != nil {
		t.Fatalf("want nil for negative total, got %+v", segs)
	}
}

func TestPlan_SingleElementParallel(t *testing.T) {
	segs := Plan(1, func(i int) bool { return true })
	if len(segs) != 1 || !segs[0].Parallel || !reflect.DeepEqual(segs[0].Indices, []int{0}) {
		t.Fatalf("want single parallel [0], got %+v", segs)
	}
}

func TestPlan_SingleElementSerial(t *testing.T) {
	segs := Plan(1, nil) // fail-safe: nil 谓词全串行
	if len(segs) != 1 || segs[0].Parallel {
		t.Fatalf("want single serial segment, got %+v", segs)
	}
}

func TestPlan_Alternating(t *testing.T) {
	// [p w p w p] → 1 并行 + 2 串行 + 2 并行 交替
	segs := Plan(5, func(i int) bool { return i%2 == 0 })
	want := []Segment{
		{Parallel: true, Indices: []int{0}},
		{Parallel: false, Indices: []int{1}},
		{Parallel: true, Indices: []int{2}},
		{Parallel: false, Indices: []int{3}},
		{Parallel: true, Indices: []int{4}},
	}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("got %+v want %+v", segs, want)
	}
}
