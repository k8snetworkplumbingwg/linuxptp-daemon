package clock

import (
	"fmt"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestDPLLReportingOffsetConvention(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		for _, absent := range []bool{false, true} {
			for _, sample := range []struct {
				name     string
				hardware event.PTPState
				valid    bool
				error    bool
				estimate int64
				state    event.PTPState
				offset   int64
			}{
				{"locked", event.PTP_LOCKED, true, false, 25, event.PTP_LOCKED, -3},
				{"holdover", event.PTP_HOLDOVER, true, false, 25, event.PTP_HOLDOVER, 25},
				{"zero estimate", event.PTP_HOLDOVER, true, false, 0, event.PTP_HOLDOVER, 0},
				{"invalid estimate", event.PTP_HOLDOVER, false, false, 25, event.PTP_HOLDOVER, -3},
				{"model error", event.PTP_HOLDOVER, false, true, 0, event.PTP_FREERUN, FaultyPhaseOffset},
				{"negative estimate", event.PTP_HOLDOVER, true, false, -1, event.PTP_FREERUN, FaultyPhaseOffset},
				{"freerun", event.PTP_FREERUN, true, false, 25, event.PTP_FREERUN, FaultyPhaseOffset},
			} {
				t.Run(fmt.Sprintf("%s/absent=%t/%s", mode, absent, sample.name), func(t *testing.T) {
					var receiver Clock
					if mode == event.GM {
						gm, err := NewGM("report.config", func() int { return 37 }, &pmc.MockClient{})
						require.NoError(t, err)
						t.Cleanup(func() { <-gm.StopManagedWorkers() })
						receiver = gm
					} else {
						bc, err := NewTBC("report.config", func() int { return 37 }, &pmc.MockClient{}, "")
						require.NoError(t, err)
						receiver = bc
					}
					receiver.SetOsClock(&OsClock{State: event.PTP_NOTSET})
					receiver.SetIPC(func(ipc.Message) {})
					receiver.SetEventLoopbackFunc(func(event.Event) {})
					ev := rawHoldoverSample()
					data := ev.Data.(*event.DPLLData)
					data.Offset = utils.ToPtr[int64](-3)
					if absent {
						data.Offset = nil
					}
					data.Holdover.HardwareState = sample.hardware
					data.Holdover.Valid = sample.valid
					data.Holdover.ErrorObserved = sample.error
					data.Holdover.EstimatedErrorNS = sample.estimate
					data.Holdover.PeakErrorNS = 25
					rawData, rawObservation := *data, *data.Holdover
					result := receiver.AddEvent(ev)
					detail := receiver.GetData(event.DPLL).GetDataDetails(ev.IFace)
					stored := *detail
					if !absent {
						expectedStored := int64(-3)
						if mode == event.TBC {
							if sample.state == event.PTP_FREERUN {
								expectedStored = FaultyPhaseOffset
							} else if sample.hardware == event.PTP_HOLDOVER && sample.valid {
								expectedStored = sample.estimate
							}
						}
						require.Equal(t, expectedStored, detail.Offset)
					}
					policyState := receiver.GetState()
					require.False(t, result.SuppressReport)
					require.NotNil(t, result.Report)
					reported := result.Report
					view := reported.Data.(*event.DPLLData)
					require.Equal(t, sample.state, view.State)
					if absent {
						require.Nil(t, view.Offset)
						require.NotContains(t, reported.GetLogData(), "offset ")
					} else {
						require.Equal(t, sample.offset, *view.Offset)
						require.Contains(t, reported.GetLogData(), fmt.Sprintf("offset %d", sample.offset))
					}
					require.Equal(t, stored, *detail)
					require.Equal(t, policyState, receiver.GetState())
					require.Equal(t, rawData, *data)
					require.Equal(t, rawObservation, *data.Holdover)
					require.Same(t, data.Holdover, view.Holdover)
					if !absent {
						*view.Offset = 999
						require.Equal(t, int64(-3), *data.Offset)
						require.Equal(t, stored, *detail)
					}
					duplicate := *data
					duplicateObservation := *data.Holdover
					duplicateObservation.EstimatedErrorNS = 999
					duplicateObservation.Valid = true
					duplicate.Holdover = &duplicateObservation
					duplicateEvent := ev
					duplicateEvent.Data = &duplicate
					result = receiver.AddEvent(duplicateEvent)
					require.False(t, result.SuppressReport)
					require.NotNil(t, result.Report)
					reported = result.Report
					if !absent {
						require.Equal(t, sample.offset, *reported.Data.(*event.DPLLData).Offset)
					}
					require.Equal(t, stored, *detail)
					stale := *data
					staleObservation := *data.Holdover
					staleObservation.Sequence = 0
					stale.Holdover = &staleObservation
					staleEvent := ev
					staleEvent.Data = &stale
					result = receiver.AddEvent(staleEvent)
					require.True(t, result.SuppressReport)
					require.Equal(t, stored, *detail)
					wrongProducer := *data.Holdover
					wrongProducer.ProducerID--
					wrongData := *data
					wrongData.Holdover = &wrongProducer
					wrongEvent := ev
					wrongEvent.Data = &wrongData
					result = receiver.AddEvent(wrongEvent)
					require.True(t, result.SuppressReport)
					require.Equal(t, stored, *detail)
					recovery := *data
					recoveryObservation := *data.Holdover
					recoveryObservation.Sequence++
					recoveryObservation.HardwareState = event.PTP_LOCKED
					recovery.Holdover = &recoveryObservation
					recoveryEvent := ev
					recoveryEvent.Data = &recovery
					result = receiver.AddEvent(recoveryEvent)
					require.False(t, result.SuppressReport)
					require.NotNil(t, result.Report)
					reported = result.Report
					require.Equal(t, event.PTP_LOCKED, reported.Data.(*event.DPLLData).State)
					require.Equal(t, data.Offset, reported.Data.(*event.DPLLData).Offset)
					result = receiver.AddEvent(ev)
					require.True(t, result.SuppressReport)
					receiver.Reset()
					result = receiver.AddEvent(recoveryEvent)
					require.True(t, result.SuppressReport)
				})
			}
		}
	}
}
