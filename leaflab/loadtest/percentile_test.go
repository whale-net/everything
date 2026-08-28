package loadtest

import (
	"testing"
	"time"
)

func TestPercentile_P95_NearestRank(t *testing.T) {
	durations := make([]time.Duration, 100)
	for i := range durations {
		durations[i] = time.Duration(i+1) * time.Millisecond // 1ms..100ms
	}
	got := Percentile(durations, 0.95)
	want := 95 * time.Millisecond
	if got != want {
		t.Errorf("Percentile(1..100ms, 0.95) = %v, want %v", got, want)
	}
}

func TestPercentile_SingleSample(t *testing.T) {
	got := Percentile([]time.Duration{42 * time.Millisecond}, 0.95)
	if want := 42 * time.Millisecond; got != want {
		t.Errorf("Percentile([42ms], 0.95) = %v, want %v", got, want)
	}
}

func TestPercentile_DoesNotMutateOrderAssumptions(t *testing.T) {
	durations := []time.Duration{5 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond}
	got := Percentile(durations, 1.0)
	if want := 5 * time.Millisecond; got != want {
		t.Errorf("Percentile(unsorted, 1.0) = %v, want max %v", got, want)
	}
}

func TestPercentile_EmptyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Percentile(nil, 0.95) did not panic")
		}
	}()
	Percentile(nil, 0.95)
}
