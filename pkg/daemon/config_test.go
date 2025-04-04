package daemon

import (
	"reflect"
	"testing"

	//"github.com/golang/glog"
	//"github.com/hexops/valast"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
)

var ptp4lConfig1 = `[global]
#
# Default Data Set
#
twoStepFlag 1
domainNumber 24
#utc_offset 37
clockClass 255
clockAccuracy 0xFE
offsetScaledLogVariance 0xFFFF
free_running 0
freq_est_interval 1
dscp_event 0
dscp_general 0
dataset_comparison G.8275.x
G.8275.defaultDS.localPriority 128
#
# Port Data Set
#
logAnnounceInterval -3
logSyncInterval -4
logMinDelayReqInterval -4
logMinPdelayReqInterval -4
announceReceiptTimeout 6
syncReceiptTimeout 0
delayAsymmetry 0
fault_reset_interval -4
neighborPropDelayThresh 20000000
G.8275.portDS.localPriority 128
#
# Run time options
#
assume_two_step 0
logging_level 6
path_trace_enabled 0
follow_up_info 0
hybrid_e2e 0
inhibit_multicast_service 0
net_sync_monitor 0
tc_spanning_tree 0
tx_timestamp_timeout 50
unicast_listen 0
unicast_master_table 0
unicast_req_duration 3600
use_syslog 1
verbose 1
summary_interval -4
kernel_leap 1
check_fup_sync 0
clock_class_threshold 7
#
# Servo Options
#
pi_proportional_const 0.0
pi_integral_const 0.0
pi_proportional_scale 0.0
pi_proportional_exponent -0.3
pi_proportional_norm_max 0.7
pi_integral_scale 0.0
pi_integral_exponent 0.4
pi_integral_norm_max 0.3
step_threshold 2.0
first_step_threshold 0.00002
max_frequency 900000000
clock_servo pi
sanity_freq_limit 200000000
ntpshm_segment 0
#
# Transport options
#
transportSpecific 0x0
ptp_dst_mac 01:1B:19:00:00:00
p2p_dst_mac 01:80:C2:00:00:0E
udp_ttl 1
udp6_scope 0x0E
uds_address /var/run/ptp4l
#
# Default interface options
#
network_transport L2
delay_mechanism E2E
time_stamping hardware
tsproc_mode filter
delay_filter moving_median
delay_filter_length 10
egressLatency 0
ingressLatency 0
#
# Clock description
#
productDescription ;;
revisionData ;;
manufacturerIdentity 00:00:00
userDescription ;
timeSource 0xA0

slaveOnly 1
[ens3f0]
masterOnly 0

[ens3f1]
masterOnly 0`

var ptp4lconfig1Object = ptp4lConf{
	sections: []ptp4lConfSection{
		{
			sectionName: "[global]",
			options: map[string]string{
				"G.8275.defaultDS.localPriority": "128",
				"G.8275.portDS.localPriority":    "128",
				"announceReceiptTimeout":         "6",
				"assume_two_step":                "0",
				"check_fup_sync":                 "0",
				"clockAccuracy":                  "0xFE",
				"clockClass":                     "255",
				"clock_class_threshold":          "7",
				"clock_servo":                    "pi",
				"dataset_comparison":             "G.8275.x",
				"delayAsymmetry":                 "0",
				"delay_filter":                   "moving_median",
				"delay_filter_length":            "10",
				"delay_mechanism":                "E2E",
				"domainNumber":                   "24",
				"dscp_event":                     "0",
				"dscp_general":                   "0",
				"egressLatency":                  "0",
				"fault_reset_interval":           "-4",
				"first_step_threshold":           "0.00002",
				"follow_up_info":                 "0",
				"free_running":                   "0",
				"freq_est_interval":              "1",
				"hybrid_e2e":                     "0",
				"ingressLatency":                 "0",
				"inhibit_multicast_service":      "0",
				"kernel_leap":                    "1",
				"logAnnounceInterval":            "-3",
				"logMinDelayReqInterval":         "-4",
				"logMinPdelayReqInterval":        "-4",
				"logSyncInterval":                "-4",
				"logging_level":                  "6",
				"manufacturerIdentity":           "00:00:00",
				"max_frequency":                  "900000000",
				"neighborPropDelayThresh":        "20000000",
				"net_sync_monitor":               "0",
				"network_transport":              "L2",
				"ntpshm_segment":                 "0",
				"offsetScaledLogVariance":        "0xFFFF",
				"p2p_dst_mac":                    "01:80:C2:00:00:0E",
				"path_trace_enabled":             "0",
				"pi_integral_const":              "0.0",
				"pi_integral_exponent":           "0.4",
				"pi_integral_norm_max":           "0.3",
				"pi_integral_scale":              "0.0",
				"pi_proportional_const":          "0.0",
				"pi_proportional_exponent":       "-0.3",
				"pi_proportional_norm_max":       "0.7",
				"pi_proportional_scale":          "0.0",
				"productDescription":             ";;",
				"ptp_dst_mac":                    "01:1B:19:00:00:00",
				"revisionData":                   ";;",
				"sanity_freq_limit":              "200000000",
				"slaveOnly":                      "1",
				"step_threshold":                 "2.0",
				"summary_interval":               "-4",
				"syncReceiptTimeout":             "0",
				"tc_spanning_tree":               "0",
				"timeSource":                     "0xA0",
				"time_stamping":                  "hardware",
				"transportSpecific":              "0x0",
				"tsproc_mode":                    "filter",
				"twoStepFlag":                    "1",
				"tx_timestamp_timeout":           "50",
				"udp6_scope":                     "0x0E",
				"udp_ttl":                        "1",
				"uds_address":                    "/var/run/ptp4l",
				"unicast_listen":                 "0",
				"unicast_master_table":           "0",
				"unicast_req_duration":           "3600",
				"use_syslog":                     "1",
				"userDescription":                ";",
				"verbose":                        "1",
			},
		},
		{
			sectionName: "[ens3f0]",
			options:     map[string]string{"masterOnly": "0"},
		},
		{
			sectionName: "[ens3f1]",
			options:     map[string]string{"masterOnly": "0"},
		},
	},
	clock_type: event.ClockType("OC"),
}
var ptp4lConfig2 = `[global]
#
# Default Data Set
#
twoStepFlag 1
domainNumber 24
#utc_offset 37
clockClass 255
clockAccuracy 0xFE
offsetScaledLogVariance 0xFFFF
free_running 0
freq_est_interval 1
dscp_event 0
dscp_general 0
dataset_comparison G.8275.x
G.8275.defaultDS.localPriority 128
#
# Port Data Set
#
logAnnounceInterval -3
logSyncInterval -4
logMinDelayReqInterval -4
logMinPdelayReqInterval -4
announceReceiptTimeout 6
syncReceiptTimeout 0
delayAsymmetry 0
fault_reset_interval -4
neighborPropDelayThresh 20000000
G.8275.portDS.localPriority 128
#
# Run time options
#
assume_two_step 0
logging_level 6
path_trace_enabled 0
follow_up_info 0
hybrid_e2e 0
inhibit_multicast_service 0
net_sync_monitor 0
tc_spanning_tree 0
tx_timestamp_timeout 50
unicast_listen 0
unicast_master_table 0
unicast_req_duration 3600
use_syslog 1
verbose 1
summary_interval -4
kernel_leap 1
check_fup_sync 0
clock_class_threshold 7
#
# Servo Options
#
pi_proportional_const 0.0
pi_integral_const 0.0
pi_proportional_scale 0.0
pi_proportional_exponent -0.3
pi_proportional_norm_max 0.7
pi_integral_scale 0.0
pi_integral_exponent 0.4
pi_integral_norm_max 0.3
step_threshold 2.0
first_step_threshold 0.00002
max_frequency 900000000
clock_servo pi
sanity_freq_limit 200000000
ntpshm_segment 0
#
# Transport options
#
transportSpecific 0x0
ptp_dst_mac 01:1B:19:00:00:00
p2p_dst_mac 01:80:C2:00:00:0E
udp_ttl 1
udp6_scope 0x0E
uds_address /var/run/ptp4l
#
# Default interface options
#
network_transport L2
delay_mechanism E2E
time_stamping hardware
tsproc_mode filter
delay_filter moving_median
delay_filter_length 10
egressLatency 0
ingressLatency 0
#
# Clock description
#
productDescription ;;
revisionData ;;
manufacturerIdentity 00:00:00
userDescription ;
timeSource 0xA0

[ens3f0]
masterOnly 1

[ens3f1]
masterOnly 0`

var ptp4lconfig2Object = ptp4lConf{
	sections: []ptp4lConfSection{
		{
			sectionName: "[global]",
			options: map[string]string{
				"G.8275.defaultDS.localPriority": "128",
				"G.8275.portDS.localPriority":    "128",
				"announceReceiptTimeout":         "6",
				"assume_two_step":                "0",
				"check_fup_sync":                 "0",
				"clockAccuracy":                  "0xFE",
				"clockClass":                     "255",
				"clock_class_threshold":          "7",
				"clock_servo":                    "pi",
				"dataset_comparison":             "G.8275.x",
				"delayAsymmetry":                 "0",
				"delay_filter":                   "moving_median",
				"delay_filter_length":            "10",
				"delay_mechanism":                "E2E",
				"domainNumber":                   "24",
				"dscp_event":                     "0",
				"dscp_general":                   "0",
				"egressLatency":                  "0",
				"fault_reset_interval":           "-4",
				"first_step_threshold":           "0.00002",
				"follow_up_info":                 "0",
				"free_running":                   "0",
				"freq_est_interval":              "1",
				"hybrid_e2e":                     "0",
				"ingressLatency":                 "0",
				"inhibit_multicast_service":      "0",
				"kernel_leap":                    "1",
				"logAnnounceInterval":            "-3",
				"logMinDelayReqInterval":         "-4",
				"logMinPdelayReqInterval":        "-4",
				"logSyncInterval":                "-4",
				"logging_level":                  "6",
				"manufacturerIdentity":           "00:00:00",
				"max_frequency":                  "900000000",
				"neighborPropDelayThresh":        "20000000",
				"net_sync_monitor":               "0",
				"network_transport":              "L2",
				"ntpshm_segment":                 "0",
				"offsetScaledLogVariance":        "0xFFFF",
				"p2p_dst_mac":                    "01:80:C2:00:00:0E",
				"path_trace_enabled":             "0",
				"pi_integral_const":              "0.0",
				"pi_integral_exponent":           "0.4",
				"pi_integral_norm_max":           "0.3",
				"pi_integral_scale":              "0.0",
				"pi_proportional_const":          "0.0",
				"pi_proportional_exponent":       "-0.3",
				"pi_proportional_norm_max":       "0.7",
				"pi_proportional_scale":          "0.0",
				"productDescription":             ";;",
				"ptp_dst_mac":                    "01:1B:19:00:00:00",
				"revisionData":                   ";;",
				"sanity_freq_limit":              "200000000",
				"step_threshold":                 "2.0",
				"summary_interval":               "-4",
				"syncReceiptTimeout":             "0",
				"tc_spanning_tree":               "0",
				"timeSource":                     "0xA0",
				"time_stamping":                  "hardware",
				"transportSpecific":              "0x0",
				"tsproc_mode":                    "filter",
				"twoStepFlag":                    "1",
				"tx_timestamp_timeout":           "50",
				"udp6_scope":                     "0x0E",
				"udp_ttl":                        "1",
				"uds_address":                    "/var/run/ptp4l",
				"unicast_listen":                 "0",
				"unicast_master_table":           "0",
				"unicast_req_duration":           "3600",
				"use_syslog":                     "1",
				"userDescription":                ";",
				"verbose":                        "1",
			},
		},
		{
			sectionName: "[ens3f0]",
			options:     map[string]string{"masterOnly": "1"},
		},
		{
			sectionName: "[ens3f1]",
			options:     map[string]string{"masterOnly": "0"},
		},
	},
	clock_type: event.ClockType("BC"),
}
var ptp4lConfig3 = `[global]
#
# Default Data Set
#
twoStepFlag 1
domainNumber 24
#utc_offset 37
clockClass 255
clockAccuracy 0xFE
offsetScaledLogVariance 0xFFFF
free_running 0
freq_est_interval 1
dscp_event 0
dscp_general 0
dataset_comparison G.8275.x
G.8275.defaultDS.localPriority 128
#
# Port Data Set
#
logAnnounceInterval -3
logSyncInterval -4
logMinDelayReqInterval -4
logMinPdelayReqInterval -4
announceReceiptTimeout 6
syncReceiptTimeout 0
delayAsymmetry 0
fault_reset_interval -4
neighborPropDelayThresh 20000000
G.8275.portDS.localPriority 128
#
# Run time options
#
assume_two_step 0
logging_level 6
path_trace_enabled 0
follow_up_info 0
hybrid_e2e 0
inhibit_multicast_service 0
net_sync_monitor 0
tc_spanning_tree 0
tx_timestamp_timeout 50
unicast_listen 0
unicast_master_table 0
unicast_req_duration 3600
use_syslog 1
verbose 1
summary_interval -4
kernel_leap 1
check_fup_sync 0
clock_class_threshold 7
#
# Servo Options
#
pi_proportional_const 0.0
pi_integral_const 0.0
pi_proportional_scale 0.0
pi_proportional_exponent -0.3
pi_proportional_norm_max 0.7
pi_integral_scale 0.0
pi_integral_exponent 0.4
pi_integral_norm_max 0.3
step_threshold 2.0
first_step_threshold 0.00002
max_frequency 900000000
clock_servo pi
sanity_freq_limit 200000000
ntpshm_segment 0
#
# Transport options
#
transportSpecific 0x0
ptp_dst_mac 01:1B:19:00:00:00
p2p_dst_mac 01:80:C2:00:00:0E
udp_ttl 1
udp6_scope 0x0E
uds_address /var/run/ptp4l
#
# Default interface options
#
network_transport L2
delay_mechanism E2E
time_stamping hardware
tsproc_mode filter
delay_filter moving_median
delay_filter_length 10
egressLatency 0
ingressLatency 0
#
# Clock description
#
productDescription ;;
revisionData ;;
manufacturerIdentity 00:00:00
userDescription ;
timeSource 0xA0

[ens3f0]
masterOnly 1

[ens3f1]
masterOnly 1`

var ptp4lconfig3Object = ptp4lConf{
	sections: []ptp4lConfSection{
		{
			sectionName: "[global]",
			options: map[string]string{
				"G.8275.defaultDS.localPriority": "128",
				"G.8275.portDS.localPriority":    "128",
				"announceReceiptTimeout":         "6",
				"assume_two_step":                "0",
				"check_fup_sync":                 "0",
				"clockAccuracy":                  "0xFE",
				"clockClass":                     "255",
				"clock_class_threshold":          "7",
				"clock_servo":                    "pi",
				"dataset_comparison":             "G.8275.x",
				"delayAsymmetry":                 "0",
				"delay_filter":                   "moving_median",
				"delay_filter_length":            "10",
				"delay_mechanism":                "E2E",
				"domainNumber":                   "24",
				"dscp_event":                     "0",
				"dscp_general":                   "0",
				"egressLatency":                  "0",
				"fault_reset_interval":           "-4",
				"first_step_threshold":           "0.00002",
				"follow_up_info":                 "0",
				"free_running":                   "0",
				"freq_est_interval":              "1",
				"hybrid_e2e":                     "0",
				"ingressLatency":                 "0",
				"inhibit_multicast_service":      "0",
				"kernel_leap":                    "1",
				"logAnnounceInterval":            "-3",
				"logMinDelayReqInterval":         "-4",
				"logMinPdelayReqInterval":        "-4",
				"logSyncInterval":                "-4",
				"logging_level":                  "6",
				"manufacturerIdentity":           "00:00:00",
				"max_frequency":                  "900000000",
				"neighborPropDelayThresh":        "20000000",
				"net_sync_monitor":               "0",
				"network_transport":              "L2",
				"ntpshm_segment":                 "0",
				"offsetScaledLogVariance":        "0xFFFF",
				"p2p_dst_mac":                    "01:80:C2:00:00:0E",
				"path_trace_enabled":             "0",
				"pi_integral_const":              "0.0",
				"pi_integral_exponent":           "0.4",
				"pi_integral_norm_max":           "0.3",
				"pi_integral_scale":              "0.0",
				"pi_proportional_const":          "0.0",
				"pi_proportional_exponent":       "-0.3",
				"pi_proportional_norm_max":       "0.7",
				"pi_proportional_scale":          "0.0",
				"productDescription":             ";;",
				"ptp_dst_mac":                    "01:1B:19:00:00:00",
				"revisionData":                   ";;",
				"sanity_freq_limit":              "200000000",
				"step_threshold":                 "2.0",
				"summary_interval":               "-4",
				"syncReceiptTimeout":             "0",
				"tc_spanning_tree":               "0",
				"timeSource":                     "0xA0",
				"time_stamping":                  "hardware",
				"transportSpecific":              "0x0",
				"tsproc_mode":                    "filter",
				"twoStepFlag":                    "1",
				"tx_timestamp_timeout":           "50",
				"udp6_scope":                     "0x0E",
				"udp_ttl":                        "1",
				"uds_address":                    "/var/run/ptp4l",
				"unicast_listen":                 "0",
				"unicast_master_table":           "0",
				"unicast_req_duration":           "3600",
				"use_syslog":                     "1",
				"userDescription":                ";",
				"verbose":                        "1",
			},
		},
		{
			sectionName: "[ens3f0]",
			options:     map[string]string{"masterOnly": "1"},
		},
		{
			sectionName: "[ens3f1]",
			options:     map[string]string{"masterOnly": "1"},
		},
	},
	clock_type: event.ClockType("GM"),
}

func Test_ptp4lConf_populatePtp4lConf(t *testing.T) {
	type args struct {
		config *string
	}
	tests := []struct {
		name        string
		ptp4lConfig *ptp4lConf
		args        args
		wantErr     bool
	}{
		{
			name:        "OC",
			ptp4lConfig: &ptp4lconfig1Object,
			args:        args{config: &ptp4lConfig1},
			wantErr:     false,
		},
		{
			name:        "BC",
			ptp4lConfig: &ptp4lconfig2Object,
			args:        args{config: &ptp4lConfig2},
			wantErr:     false,
		},
		{
			name:        "GM",
			ptp4lConfig: &ptp4lconfig3Object,
			args:        args{config: &ptp4lConfig3},
			wantErr:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := &ptp4lConf{}
			if err := output.populatePtp4lConf(tt.args.config); (err != nil) != tt.wantErr {
				t.Errorf("ptp4lConf.populatePtp4lConf() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				if !reflect.DeepEqual(output, tt.ptp4lConfig) {
					t.Fail()
				}
			}
		})
	}
}
