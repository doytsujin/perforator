package perf

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProfiler(t *testing.T) {
	profiler, err := NewProfiler(
		unix.PERF_TYPE_HARDWARE,
		unix.PERF_COUNT_HW_INSTRUCTIONS,
		os.Getpid(),
		-1,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := profiler.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	if err := profiler.Start(); err != nil {
		t.Fatal(err)
	}

	_, err = profiler.Profile()
	if err != nil {
		t.Fatal(err)
	}

	if err := profiler.Stop(); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkProfiler(b *testing.B) {
	profiler, err := NewProfiler(
		unix.PERF_TYPE_HARDWARE,
		unix.PERF_COUNT_HW_INSTRUCTIONS,
		os.Getpid(),
		-1,
	)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := profiler.Close(); err != nil {
			b.Fatal(err)
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()

	if err := profiler.Start(); err != nil {
		b.Fatal(err)
	}
	for n := 0; n < b.N; n++ {
		if _, err := profiler.Profile(); err != nil {
			b.Fatal(err)
		}
	}
}
