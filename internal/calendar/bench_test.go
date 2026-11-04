package calendar

import (
	"fmt"
	"testing"
)

func BenchmarkMerge(b *testing.B) {
	in := make([]Event, 300)
	for i := range in {
		in[i] = Event{ID: fmt.Sprint(i), Date: fmt.Sprintf("2026-11-%02d", i%28+1), Title: fmt.Sprintf("Show %d", i%40), Season: 1, Episode: i}
	}
	b.ReportAllocs()
	for b.Loop() {
		merge(in)
	}
}
