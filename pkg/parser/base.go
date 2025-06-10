package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	ptpMetrics "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/metrics"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/shared"
	"strings"
)

type MetricsExtractor interface {
	ProcessName() string
	Extract(configName, logLine string, ifaces config.IFaces) (error, *Metrics)
}

type BaseMetricsExtractor struct {
	ProcessNameStr   string
	ExtractSummaryFn func(messageTag, configName, logLine string, ifaces config.IFaces, _ shared.SharedState) (error, *Metrics)
	ExtractRegularFn func(messageTag, configName, logLine string, ifaces config.IFaces, _ shared.SharedState) (error, *Metrics)
	ExtraEventFn     func(configName, output string, ifaces config.IFaces, _ shared.SharedState) (error, *PTPEvent)
	State            shared.SharedState
}

func (b *BaseMetricsExtractor) ProcessName() string {
	return b.ProcessNameStr
}

func (b *BaseMetricsExtractor) Extract(messageTag, configName, logLine string, ifaces config.IFaces) (error, *Metrics) {
	logLine = removeMessageSuffix(logLine)

	if strings.Contains(logLine, " max ") {
		err, metrics := b.ExtractSummaryFn(messageTag, configName, logLine, ifaces, b.State)
		if err == nil && metrics != nil && metrics.Iface != "" {
			ptpMetrics.UpdatePTPMetrics(metrics.From, b.ProcessNameStr, metrics.Iface, metrics.Offset, metrics.MaxOffset, metrics.FreqAdj, metrics.Delay)
		}
		return nil, metrics
	}

	if strings.Contains(logLine, " offset ") {
		err, metrics := b.ExtractRegularFn(messageTag, configName, logLine, ifaces, b.State)
		if err == nil && metrics != nil && metrics.Iface != "" {
			ptpMetrics.UpdatePTPMetrics(metrics.From, b.ProcessNameStr, metrics.Iface, metrics.Offset, metrics.MaxOffset, metrics.FreqAdj, metrics.Delay)
			ptpMetrics.UpdateClockStateMetrics(b.ProcessNameStr, metrics.Iface, metrics.ClockState)
		}
		return nil, metrics
	}

	return nil, nil
}

func (b *BaseMetricsExtractor) ExtractEvent(output string) (error, *PTPEvent) {
	if b.ExtraEventFn != nil {
		err, event := b.ExtraEventFn(output)
		if err != nil {
			return err, nil
		} else {
			return nil, event
		}
	}
	return nil, nil
}
