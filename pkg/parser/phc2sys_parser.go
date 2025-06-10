package parser

import (
	"fmt"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/shared"
	"strconv"
	"strings"
)

const (
	PHC2SYS = "phc2sys"
)

func NewPhc2SysExtractor(state shared.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: PHC2SYS,
		ExtractSummaryFn: func(messageTag, configName, logLine string, ifaces config.IFaces, _ shared.SharedState) (error, *Metrics) {
			return extractSummaryPTP4l(messageTag, configName, logLine, ifaces, state)
		},
		ExtractRegularFn: func(messageTag, configName, logLine string, ifaces config.IFaces, _ shared.SharedState) (error, *Metrics) {
			return extractRegularPTP4l(messageTag, configName, logLine, ifaces, state)
		},
		ExtraEventFn: nil,
		State:        state,
	}
}

func extractSummaryPhc2Sys(messageTag, configName, output string, ifaces config.IFaces) (error, *Metrics) {
	// ptp4l[74737.942]: [ptp4l.0.config] rms  53 max   74 freq -16642 +/-  40 delay  1089 +/-  20
	// or
	// ptp4l[365195.391]: [ptp4l.0.config] master offset         -1 s2 freq   -3972 path delay        89
	var ptpOffset, maxPtpOffset, frequencyAdjustment, delay float64
	var iface, clockState string
	var err error
	rmsIndex := strings.Index(output, rms)
	if rmsIndex < 0 {
		return fmt.Errorf("failed to find rms in output %s", output), nil
	}

	replacer := strings.NewReplacer("[", " ", "]", " ", ":", " ")
	output = replacer.Replace(output)

	indx := strings.Index(output, configName)
	if indx == -1 {
		return fmt.Errorf("failed to find rms in output %s", output), nil
	}
	output = output[indx:]
	fields := strings.Fields(output)

	// 0                1            2     3 4      5  6    7      8    9  10     11
	//ptp4l.0.config CLOCK_REALTIME rms   31 max   31 freq -77331 +/-   0 delay  1233 +/-   0
	if len(fields) < 8 {
		return fmt.Errorf("failed to parse output %s, not enough fields", output), nil
	}

	// when ptp4l log for master offset
	if fields[1] == rms { // if first field is rms , then add master
		fields = append(fields, "") // Making space for the new element
		//  0             1     2
		//ptp4l.0.config rms   53 max   74 freq -16642 +/-  40 delay  1089 +/-  20
		copy(fields[2:], fields[1:]) // Shifting elements
		fields[1] = master           // Copying/inserting the value
		//  0             0       1   2
		//ptp4l.0.config master rms   53 max   74 freq -16642 +/-  40 delay  1089 +/-  20
	}

	iface = fields[1]

	ptpOffset, err = strconv.ParseFloat(fields[3], 64)
	if err != nil {
		glog.Errorf("%s failed to parse offset from the output %s error %v", PTP4L, fields[3], err)
	}

	maxPtpOffset, err = strconv.ParseFloat(fields[5], 64)
	if err != nil {
		glog.Errorf("%s failed to parse max offset from the output %s error %v", PTP4L, fields[5], err)
	}

	frequencyAdjustment, err = strconv.ParseFloat(fields[7], 64)
	if err != nil {
		glog.Errorf("%s failed to parse frequency adjustment output %s error %v", PTP4L, fields[7], err)
	}

	if len(fields) >= 11 {
		delay, err = strconv.ParseFloat(fields[11], 64)
		if err != nil {
			glog.Errorf("%s failed to parse delay from the output %s error %v", PTP4L, fields[11], err)
		}
	} else {
		// If there is no delay from master this mean we are out of sync
		glog.Warningf("no delay from master process %s out of sync", PTP4L)
	}

	offsetSource := master

	return nil, &Metrics{
		Iface:      iface,
		Offset:     ptpOffset,
		MaxOffset:  maxPtpOffset,
		FreqAdj:    frequencyAdjustment,
		Delay:      delay,
		ClockState: clockState,
		Source:     offsetSource,
	}

}

func extractRegularPhc2Sys(configName, processName, output string, ifaces config.IFaces) (error, *Metrics) {
	indx := strings.Index(output, offset)
	if indx < 0 {
		return nil, nil
	}
	output = normalizeLine(output)

	index := strings.Index(output, configName)
	if index == -1 {
		return nil, nil
	}

	output = output[index:]
	fields := strings.Fields(output)
	var err error
	var iface, clockState string
	var ptpOffset, maxPtpOffset, frequencyAdjustment, delay float64

	//       0         1      2          3     4   5    6          7     8
	// ptp4l.0.config master offset   -2162130 s2 freq +22451884  delay 374976

	if len(fields) < 7 {
		return nil, nil
	}
	//       0         1      2          3    4   5       6     7     8
	//ptp4l.0.config master offset       4    s2  freq   -3964 path delay  91

	if len(fields) < 7 {
		err = fmt.Errorf("%s failed to parse output %s: unexpected number of fields", processName, output)
		return nil, nil
	}

	if fields[2] != offset {
		err = fmt.Errorf("%s failed to parse offset from the output %s error %s", processName, fields[1], "offset is not in right order")
		return nil, nil
	}

	iface = fields[1]
	if iface != clockRealTime {
		return nil, nil // ignore other interfaces offsets
	}

	ptpOffset, e := strconv.ParseFloat(fields[3], 64)
	if e != nil {
		err = fmt.Errorf("%s failed to parse offset from the output %s error %v", processName, fields[1], err)
		return nil, nil
	}

	maxPtpOffset, err = strconv.ParseFloat(fields[3], 64)
	if err != nil {
		err = fmt.Errorf("%s failed to parse max offset from the output %s error %v", processName, fields[1], err)
		return nil, nil
	}

	switch fields[4] {
	case "s0":
		clockState = FREERUN
	case "s1":
		clockState = FREERUN
	case "s2", "s3":
		clockState = LOCKED
	default:
		clockState = FREERUN
	}

	frequencyAdjustment, err = strconv.ParseFloat(fields[6], 64)
	if err != nil {
		err = fmt.Errorf("%s failed to parse frequency adjustment output %s error %v", processName, fields[6], err)
		return nil, nil
	}

	if len(fields) > 8 {
		delay, err = strconv.ParseFloat(fields[8], 64)
		if err != nil {
			err = fmt.Errorf("%s failed to parse delay from the output %s error %v", processName, fields[8], err)
		}
	} else {
		// If there is no delay this mean we are out of sync
		glog.Warningf("no delay from the process %s out of sync", processName)
	}
	offsetSource := master
	if strings.Contains(output, "sys offset") {
		offsetSource = sys
	} else if strings.Contains(output, "phc offset") {
		offsetSource = phc
	}

	return nil, &Metrics{
		Iface:      iface,
		Offset:     ptpOffset,
		MaxOffset:  maxPtpOffset,
		FreqAdj:    frequencyAdjustment,
		Delay:      delay,
		ClockState: clockState,
		Source:     offsetSource,
	}
}
