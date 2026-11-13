package daemon

import (
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// ts2phcConfig snapshots the already-rendered interfaces for setup-time clock
// configuration. It performs no separate profile parsing or PHC resolution.
func (p *ptpProcess) ts2phcConfig() event.TS2PHCConfig {
	cfg := event.TS2PHCConfig{}
	for _, iface := range p.ifaces {
		cfg.Interfaces = append(cfg.Interfaces, event.TS2PHCInterface{
			Name: iface.Name, PHCDevice: iface.PhcId, Source: iface.Source,
		})
	}
	if p.nodeProfile != nil {
		cfg.LeadingInterface = p.nodeProfile.PtpSettings["leadingInterface"]
	}
	if p.ptpClockThreshold != nil {
		cfg.Threshold = event.PtpClockThreshold{
			MaxOffsetThreshold: p.ptpClockThreshold.MaxOffsetThreshold,
			MinOffsetThreshold: p.ptpClockThreshold.MinOffsetThreshold,
			HoldOverTimeout:    p.ptpClockThreshold.HoldOverTimeout,
		}
	}
	return cfg
}

// processTS2PHCOutput combines the metric and event from one log line. Numeric
// measurements are exported here, but no inferred clock state is published.
func (p *ptpProcess) processTS2PHCOutput(metric *parser.Metrics, parsed *parser.PTPEvent) {
	if metric == nil && (parsed == nil || parsed.Ts2Phc == nil) {
		return
	}

	// Offset lines can carry both a metric and an event. Prefer the metric's
	// identifier, then the event's interface, then its PHC device.
	iface := ""
	switch {
	case metric != nil && metric.Iface != "":
		iface = metric.Iface
	case parsed != nil && parsed.Iface != "":
		iface = parsed.Iface
	case parsed != nil && parsed.Ts2Phc != nil:
		iface = parsed.Ts2Phc.PHCDevice
	}
	if iface != "" {
		iface = p.ifaces.GetPhcID2IFace(iface)
	}

	observation := &event.TS2PHCObservation{}
	if parsed != nil && parsed.Ts2Phc != nil {
		data := parsed.Ts2Phc
		observation.Timestamp = data.Timestamp
		observation.PHCDevice = data.PHCDevice
		observation.Role = data.Role
		observation.PollTimeout = data.PollTimeout
		observation.PollSuccessful = data.PollSuccessful
		observation.Holdover = data.Holdover
		if data.PHCTstamp != nil {
			stamp := *data.PHCTstamp
			observation.PHCTstamp = &stamp
		}
	}
	if metric != nil {
		p.offset = metric.Offset
		updatePTPMetrics(metric.Source, p.name, alias.GetAlias(iface), metric.Offset, metric.MaxOffset, metric.FreqAdj, metric.Delay)
		if configName := p.ts2phcConfigName(); metric.Source == master && configName != "" {
			masterOffsetSource.set(configName, p.name)
		}
		observation.Offset = utils.ToPtr(metric.Offset)
		observation.State = convertParserClockStateEventPTPState(metric.ClockState)
		if p.ifaces.GetEventSource(iface) == event.GNSS {
			observation.NMEALocked = utils.ToPtr(true)
		}
	}
	p.forwardTS2PHCEvent(iface, observation)
}

func (p *ptpProcess) forwardTS2PHCEvent(iface string, data event.EventData) {
	if p.eventCh == nil {
		return
	}
	// Do not drop individual timestamp/offset facts on a full queue: doing so
	// could itself create a false missing-offset failure. Stop remains cancellable.
	select {
	case p.eventCh <- event.Event{
		Source: event.TS2PHC, CfgName: p.ts2phcConfigName(), IFace: iface,
		ClockType: p.clockType, Time: time.Now().UnixMilli(), Data: data,
	}:
	case <-p.stopCh:
	}
}

func (p *ptpProcess) ts2phcConfigName() string {
	if p.configName != "" {
		return p.configName
	}
	return configNameFromMessageTag(p.messageTag)
}

func (p *ptpProcess) processTS2PhcEvent(parsed *parser.PTPEvent) {
	p.processTS2PHCOutput(nil, parsed)
}
