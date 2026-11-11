package dpll

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

func TestModelSelectedFromResolvedConstructorConfiguration(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			source := event.GNSS
			if mode == event.TBC {
				source = event.PTP4l
			}
			resolved := func(d *DpllConfig) {
				d.LocalMaxHoldoverOffSet, d.LocalHoldoverTimeout = 200, 50
			}
			dpll := NewDpll(1, 100, 100, 40, "fixed-model", []event.EventSource{source}, MOCK, nil,
				0, 0, 0, config.ProcessConfig{ClockType: mode}, nil, resolved)
			t.Cleanup(dpll.ticker.Stop)
			require.NotNil(t, dpll.holdoverModel)
			offset, err := dpll.holdoverModel.Estimate(2 * time.Second)
			require.NoError(t, err)
			require.Equal(t, int64(8), offset)
			other := NewDpll(2, 300, 100, 40, "other-model", []event.EventSource{source}, MOCK, nil,
				0, 0, 0, config.ProcessConfig{ClockType: mode}, nil)
			t.Cleanup(other.ticker.Stop)
			offset, err = other.holdoverModel.Estimate(2 * time.Second)
			require.NoError(t, err)
			require.Equal(t, int64(6), offset)
		})
	}
}

type recordingHoldoverModel struct {
	elapsed atomic.Int64
}

func (model *recordingHoldoverModel) Estimate(elapsed time.Duration) (int64, error) {
	model.elapsed.Store(int64(elapsed))
	return 10, nil
}

func TestInstanceModelReusedAcrossAcquiredEpisodes(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			source := event.GNSS
			if mode == event.TBC {
				source = event.PTP4l
			}
			model := &recordingHoldoverModel{}
			dpll := newEstimatorDPLL(t, mode, source, modelOption(t, model))
			events := make(chan event.Event, 256)
			dpll.processConfig.EventChannel = events
			var previousEpisode uint64
			for range 2 {
				setModeledHardware(dpll, DPLL_LOCKED_HO_ACQ)
				dpll.RLock()
				startCleared := dpll.holdoverStart.IsZero()
				dpll.RUnlock()
				require.True(t, startCleared)
				setModeledHardware(dpll, DPLL_HOLDOVER)
				dpll.RLock()
				episode, start := dpll.holdoverGeneration, dpll.holdoverStart
				dpll.RUnlock()
				require.Greater(t, episode, previousEpisode)
				require.Same(t, model, dpll.holdoverModel)
				deadline := time.NewTimer(time.Second)
				t.Cleanup(func() { deadline.Stop() })
				accepted := false
				for !accepted {
					select {
					case ev := <-events:
						observation := ev.Data.(*event.DPLLData).Holdover
						if observation.Episode == episode && observation.Valid {
							require.Equal(t, int64(10), observation.EstimatedErrorNS)
							require.GreaterOrEqual(t, observation.Elapsed, time.Duration(0))
							require.LessOrEqual(t, observation.Elapsed, time.Since(start))
							accepted = true
						}
					case <-deadline.C:
						t.Fatal("instance model did not report the new episode")
					}
				}
				require.GreaterOrEqual(t, model.elapsed.Load(), int64(0))
				require.LessOrEqual(t, time.Duration(model.elapsed.Load()), time.Since(start))
				previousEpisode = episode
			}
		})
	}
}
