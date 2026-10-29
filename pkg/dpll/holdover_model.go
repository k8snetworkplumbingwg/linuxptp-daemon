package dpll

import (
	"fmt"
	"math"
	"time"
)

// HoldoverModel estimates nonnegative nanosecond error from episode elapsed time.
type HoldoverModel interface {
	Estimate(elapsed time.Duration) (offsetNS int64, err error)
}

type linearHoldoverModel struct {
	slope float64
}

// NewLinearHoldoverModel creates a bounded linear estimate from positive limits.
func NewLinearHoldoverModel(maxOffsetNS uint64, timeout time.Duration) (HoldoverModel, error) {
	if maxOffsetNS == 0 || timeout <= 0 {
		return nil, fmt.Errorf("linear holdover requires positive offset and timeout")
	}
	return linearHoldoverModel{slope: float64(maxOffsetNS) / timeout.Seconds()}, nil
}

func (model linearHoldoverModel) Estimate(elapsed time.Duration) (int64, error) {
	if elapsed < 0 {
		return 0, fmt.Errorf("negative holdover elapsed time: %s", elapsed)
	}
	offset := math.Round(model.slope * elapsed.Seconds())
	if math.IsNaN(offset) || math.IsInf(offset, 0) || offset >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("holdover estimate is not representable in nanoseconds")
	}
	return int64(offset), nil
}
