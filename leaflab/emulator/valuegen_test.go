package main

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
)

// allSensorTypes covers every SensorType the emulator's range table knows
// about, including UNKNOWN.
var allSensorTypes = []firmwarepb.SensorType{
	firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN,
	firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE,
	firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE,
	firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY,
	firmwarepb.SensorType_SENSOR_TYPE_ECO2,
	firmwarepb.SensorType_SENSOR_TYPE_TVOC,
}

func TestWalker_InRangeForever(t *testing.T) {
	for _, st := range allSensorTypes {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(1))
			w := NewWalker(st, rng)
			r := rangeFor(st)
			for i := 0; i < 10000; i++ {
				v := w.Next()
				require.GreaterOrEqualf(t, v, r.min, "tick %d below min", i)
				require.LessOrEqualf(t, v, r.max, "tick %d above max", i)
			}
		})
	}
}

func TestWalker_BoundedStep(t *testing.T) {
	const epsilon = 1e-4
	for _, st := range allSensorTypes {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(2))
			w := NewWalker(st, rng)
			r := rangeFor(st)
			prev := w.Next()
			for i := 0; i < 10000; i++ {
				v := w.Next()
				delta := v - prev
				if delta < 0 {
					delta = -delta
				}
				require.LessOrEqualf(t, delta, r.step+epsilon, "tick %d step too large", i)
				prev = v
			}
		})
	}
}

func TestWalker_NotStatic(t *testing.T) {
	for _, st := range allSensorTypes {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(3))
			w := NewWalker(st, rng)
			seen := make(map[float32]struct{})
			for i := 0; i < 100; i++ {
				seen[w.Next()] = struct{}{}
			}
			assert.GreaterOrEqual(t, len(seen), 50, "expected at least 50 distinct values over 100 ticks")
		})
	}
}

func TestWalker_ActuallyWalks(t *testing.T) {
	for _, st := range allSensorTypes {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(4))
			w := NewWalker(st, rng)
			r := rangeFor(st)
			rangeWidth := float64(r.max - r.min)

			prev := w.Next()
			var sumAbsDelta float64
			const n = 1000
			for i := 0; i < n; i++ {
				v := w.Next()
				sumAbsDelta += math.Abs(float64(v - prev))
				prev = v
			}
			meanAbsDelta := sumAbsDelta / n
			assert.Lessf(t, meanAbsDelta, 0.25*rangeWidth,
				"mean abs consecutive delta %v too large relative to range width %v; looks like uniform resampling, not a walk",
				meanAbsDelta, rangeWidth)
		})
	}
}

func seriesOf(w *Walker, n int) []float32 {
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = w.Next()
	}
	return out
}

func TestWalker_DeterministicUnderSeed(t *testing.T) {
	for _, st := range allSensorTypes {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			w1 := NewWalker(st, rand.New(rand.NewSource(42)))
			w2 := NewWalker(st, rand.New(rand.NewSource(42)))
			series1 := seriesOf(w1, 100)
			series2 := seriesOf(w2, 100)
			assert.Equal(t, series1, series2, "same seed should produce identical series")

			w3 := NewWalker(st, rand.New(rand.NewSource(43)))
			series3 := seriesOf(w3, 100)
			assert.NotEqual(t, series1, series3, "different seed should produce a different series")
		})
	}
}

func TestWalker_IndependentPerSensor(t *testing.T) {
	for _, st := range allSensorTypes {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			wA := NewWalker(st, rand.New(rand.NewSource(100)))
			wB := NewWalker(st, rand.New(rand.NewSource(200)))
			assert.NotEqual(t, seriesOf(wA, 100), seriesOf(wB, 100), "different derived seeds should not produce identical series")
		})
	}
}

func TestWalker_Unknown_FiniteInRange(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	w := NewWalker(firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN, rng)
	for i := 0; i < 1000; i++ {
		v := w.Next()
		require.False(t, math.IsNaN(float64(v)), "tick %d: NaN", i)
		require.False(t, math.IsInf(float64(v), 0), "tick %d: Inf", i)
		require.GreaterOrEqual(t, v, float32(0))
		require.LessOrEqual(t, v, float32(100))
	}
}

func TestUnitFor_CoversEveryEnumValue(t *testing.T) {
	cases := map[firmwarepb.SensorType]string{
		firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN:     "",
		firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE: "lx",
		firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE: "°C",
		firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY:    "%RH",
		firmwarepb.SensorType_SENSOR_TYPE_ECO2:        "ppm",
		firmwarepb.SensorType_SENSOR_TYPE_TVOC:        "ppb",
	}
	for st, want := range cases {
		assert.Equal(t, want, UnitFor(st), "unit mismatch for %v", st)
	}
}
