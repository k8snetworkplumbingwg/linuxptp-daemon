package daemon

import (
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
)

// ts2phcMatchState keeps the latest timestamp per PHC until its offset arrives.
type ts2phcMatchState struct {
	mu      sync.Mutex
	pending map[string]string // PHC device -> interface
}

// phcToIface retrieves the interface related to the given PHC device.
func (p *ptpProcess) phcToIface(device string) (string, error) {
	iface := p.ifaces.GetPhcID2IFace(device)
	if iface == "" || iface == device {
		return "", fmt.Errorf("no configured interface associated with %s", device)
	}
	return iface, nil
}

// processTS2PhcEvent pairs timestamp and offset lines; other ts2phc messages do
// not participate in status evaluation.
func (p *ptpProcess) processTS2PhcEvent(ptpEvent *parser.PTPEvent) {
	if ptpEvent == nil || ptpEvent.Ts2Phc == nil {
		return
	}

	ts2phcEvent := ptpEvent.Ts2Phc
	if ts2phcEvent.PHCTstamp != nil {
		iface, err := p.handleAddedTstamp(ts2phcEvent)
		if err != nil {
			glog.Errorf("Failed to process ts2phc timestamp for %s: %v", p.configName, err)
			return
		}
		if iface != "" {
			p.reportUnmatchedTstamp(iface)
		}
	} else if ts2phcEvent.OffsetReported {
		p.handleOffsetFor(ptpEvent.Iface)
	}
}

// handleAddedTstamp starts a new per-device interval and returns the interface
// for the previous timestamp if its offset did not arrive in that interval.
func (p *ptpProcess) handleAddedTstamp(data *parser.Ts2PhcEvent) (string, error) {
	if data.PHCTstamp == nil || data.PHCDevice == "" {
		return "", fmt.Errorf("ts2phc timestamp event is missing its PHC or timestamp")
	}
	iface, err := p.phcToIface(data.PHCDevice)
	if err != nil {
		glog.V(4).Infof("Ignoring unmanaged ts2phc clock: %v", err)
		return "", nil
	}

	s := &p.ts2phcMatches
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[string]string)
	}
	previous := s.pending[data.PHCDevice]
	s.pending[data.PHCDevice] = iface
	return previous, nil
}

func (p *ptpProcess) handleOffsetFor(iface string) {
	device := iface
	for _, configuredIface := range p.ifaces {
		if configuredIface.Name == iface && configuredIface.PhcId != "" {
			device = configuredIface.PhcId
			break
		}
	}
	s := &p.ts2phcMatches
	s.mu.Lock()
	delete(s.pending, device)
	s.mu.Unlock()
}

func (p *ptpProcess) reportUnmatchedTstamp(iface string) {
	select {
	case p.eventCh <- event.Event{
		Source: event.TS2PHC, CfgName: p.configName, IFace: iface,
		ClockType: p.clockType, Time: time.Now().UnixMilli(),
		Data: &event.StateData{State: event.PTP_FREERUN},
	}:
	default:
	}
}

func (p *ptpProcess) resetTS2PHCMatches() {
	s := &p.ts2phcMatches
	s.mu.Lock()
	s.pending = make(map[string]string)
	s.mu.Unlock()
}
