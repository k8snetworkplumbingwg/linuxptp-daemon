package utils_test

import (
	"math"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowExtrema(t *testing.T) {
	empty := utils.NewWindow(3)
	assert.Zero(t, empty.AbsMax())
	assert.Zero(t, empty.Max())
	assert.Zero(t, empty.AbsMin())
	assert.Zero(t, empty.Min())
	assert.Zero(t, empty.LastInserted())

	negative := utils.NewWindow(3)
	for _, value := range []float64{-8, -3, -5} {
		negative.Insert(value)
	}
	assert.Equal(t, float64(8), negative.AbsMax())
	assert.Equal(t, float64(-3), negative.Max())
	assert.Equal(t, float64(3), negative.AbsMin())

	positive := utils.NewWindow(3)
	for _, value := range []float64{2, 8, 5} {
		positive.Insert(value)
	}
	assert.Equal(t, float64(2), positive.Min())
}

func TestWindowStatistics(t *testing.T) {
	w := utils.NewWindow(3)
	for _, value := range []float64{-5, 2, 7} {
		w.Insert(value)
	}
	require.NoError(t, w.SetWeights([]float64{1, 2, 1}))

	assert.InDelta(t, 4, w.AbsMean(), 1e-12)
	assert.InDelta(t, 1.5, w.Mean(), 1e-12)
	assert.Equal(t, float64(2), w.Median())
	assert.InDelta(t, 73.0/3, w.Variance(), 1e-12)
	assert.InDelta(t, math.Sqrt(73.0/3), w.StdDev(), 1e-12)

	err := w.SetWeights([]float64{1, 1, 1, 1})
	require.EqualError(t, err, "weights of wrong size: expected=3 actual=4")
}

func TestWindowCountSamplesStopsAtFirstMismatch(t *testing.T) {
	partial := utils.NewWindow(3)
	partial.Insert(1)
	partial.Insert(2)
	assert.Equal(t, 2, partial.CountSamples(func(float64) bool { return true }))
	assert.Equal(t, 0, partial.CountSamples(func(value float64) bool { return value < 2 }))

	full := utils.NewWindow(3)
	for _, value := range []float64{1, 2, 3, 4} {
		full.Insert(value)
	}
	assert.Equal(t, 2, full.CountSamples(func(value float64) bool { return value != 2 }))
}
