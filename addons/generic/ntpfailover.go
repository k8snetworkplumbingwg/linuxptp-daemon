package generic

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"regexp"
	"sync"
)

type NtpFailoverPluginData struct {
	cmdStop        map[string]func()
	cmdRun         map[string]func(bool, *plugin.PluginManager)
	stdoutToSocket bool
	pm             *plugin.PluginManager
	pcfsmState     int
	pcfsmMutex     sync.Mutex
	pcfsmLocked    bool
}

const ( // phc2sys/chronyd Finite State Machine States
	PCFSMS_STARTUP_UNKNOWN      int = iota // Just started, both are unknown
	PCFSMS_STARTUP_PHC2SYS                 // phc2sys setting time
	PCFSMS_STARTUP_CHRONYD                 // phc2sys setting time
	PCFSMS_STARTUP_BOTH                    // phc2sys setting time
	PCFSMS_ACTIVE                          // phc2sys setting time
	PCFSMS_HOLDOVERIN                      // phc2sys setting time, in holdover
	PCFSMS_HOLDOVEROUT                     // phc2sys setting time, out of holdover
	PCFSMS_FAILOVER                        // chronyd setting time
	PCFSMS_FAILOVER_RECOVERABLE            // chronyd setting time, phc2sys can again

)

var (
	nmeaStatusTS2PhcRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s*\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+(?P<interface>[\w|\/]+)` +
			`\s+nmea_status\s+(?P<status>[0|1])` +
			`\s+offset\s+(?P<offset>-?\d+)` +
			`\s+(?P<servo_state>s\d+)?`,
	)
)

func OnPTPConfigChangeNtpFailover(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	if data != nil {
		_data := *data
		var pluginData *NtpFailoverPluginData = _data.(*NtpFailoverPluginData)
		_pluginData := *pluginData
		if _pluginData.cmdRun == nil {
			_pluginData.cmdRun = make(map[string]func(bool, *plugin.PluginManager))
			print("FAILOVER: OnPTPConfigChangeFailover rebuild cmdRun dict")
		}
		if _pluginData.cmdStop == nil {
			_pluginData.cmdStop = make(map[string]func())
			print("FAILOVER: OnPTPConfigChangeFailover rebuild cmdStop dict")
		}
	}
	return nil
}

func RegisterProcessNtpFailover(data *interface{}, pname string, cmdRun func(bool, *plugin.PluginManager), cmdStop func(), stdoutToSocket bool, pm *plugin.PluginManager) {
	print("FAILOVER: RegisterProcessFailover")
	if data != nil {
		print("RegisterProcessFailover " + pname + "\n")
		_data := *data
		var pluginData *NtpFailoverPluginData = _data.(*NtpFailoverPluginData)
		_pluginData := *pluginData
		if _pluginData.cmdRun == nil {
			_pluginData.cmdRun = make(map[string]func(bool, *plugin.PluginManager))
			print("FAILOVER: RegisterProcessFailover rebuild cmdRun dict")
		}
		if _pluginData.cmdStop == nil {
			_pluginData.cmdStop = make(map[string]func())
			print("FAILOVER: RegisterProcessFailover rebuild cmdStop dict")
		}
		_pluginData.cmdStop[pname] = cmdStop
		_pluginData.cmdRun[pname] = cmdRun
		_pluginData.stdoutToSocket = stdoutToSocket
		_pluginData.pm = pm
	}
}

func ProcessLogNtpFailover(data *interface{}, pname string, log string) string {
	ret := log
	print(pname)
	print(nmeaStatusTS2PhcRegex.MatchString(log))
	print("ProcessLogFailover: ")
	print(log)
	print("\n")
	if data != nil {
		if pname == "ts2phc" || nmeaStatusTS2PhcRegex.MatchString(log) {
			print(pname)
			print(nmeaStatusTS2PhcRegex.MatchString(log))
			print("ProcessLogFailover: ")
			print(log)
			print("\n")

			_data := *data
			var pluginData *NtpFailoverPluginData = _data.(*NtpFailoverPluginData)
			_pluginData := *pluginData

			_pluginData.pcfsmMutex.Lock()
			ownLock := !_pluginData.pcfsmLocked //If locked, then skip, otherwise take lock
			_pluginData.pcfsmMutex.Unlock()
			if ownLock {
			done:
				for {
					switch _pluginData.pcfsmState {
					case PCFSMS_STARTUP_UNKNOWN:
						print("FAILOVER: PCFSMS_STARTUP_UNKNOWN\n")
						_, foundChronyd := _pluginData.cmdStop["chronyd"]
						_, foundPhc2Sys := _pluginData.cmdRun["phc2sys"]
						if foundChronyd && foundPhc2Sys {
							_pluginData.pcfsmState = PCFSMS_STARTUP_BOTH
						} else if foundChronyd {
							_pluginData.pcfsmState = PCFSMS_STARTUP_CHRONYD
						} else if foundPhc2Sys {
							_pluginData.pcfsmState = PCFSMS_STARTUP_PHC2SYS
						} else {
							break done
						}
					case PCFSMS_STARTUP_PHC2SYS:
						print("FAILOVER: PCFSMS_STARTUP_PHC2SYS\n")
						_, foundChronyd := _pluginData.cmdStop["chronyd"]
						if foundChronyd {
							_pluginData.pcfsmState = PCFSMS_STARTUP_BOTH
						} else {
							break done
						}
					case PCFSMS_STARTUP_CHRONYD:
						print("FAILOVER: PCFSMS_STARTUP_CHRONYD\n")
						_, foundPhc2Sys := _pluginData.cmdStop["phc2sys"]
						if foundPhc2Sys {
							_pluginData.pcfsmState = PCFSMS_STARTUP_BOTH
						} else {
							break done
						}
					case PCFSMS_STARTUP_BOTH:
						print("FAILOVER: PCFSMS_STARTUP_BOTH\n")
						//cmdStop, ok := _pluginData.cmdStop["chronyd"]
						//if ok {
						//	print("FAILOVER: Disabling chronyd at startup\n")
						//	cmdStop()
						//}
						print("FAILOVER: DONE disabling chronyd at startup\n")
						//cmdRun, ok := _pluginData.cmdRun["phc2sys"]
						//if ok {
						//	print("FAILOVER: Enabling phc2sys at startup\n")
						//	cmdRun(_pluginData.stdoutToSocket, _pluginData.pm)
						//}
						_pluginData.pcfsmState = PCFSMS_ACTIVE
						print("FAILOVER: goto PCFSMS_ACTIVE\n")
						continue
					case PCFSMS_ACTIVE:
						if pname == "ts2phc" {
							if log[:14][12:] == "500" {
								print("FAILOVER: Going into holdover\n")
								_pluginData.pcfsmState = PCFSMS_HOLDOVERIN
								continue
							}
						}
						break done
					case PCFSMS_HOLDOVERIN:
						if pname == "ts2phc" {
							if log[:14][12:] == "600" {
								print("FAILOVER: Going out of holdover\n")
							}
							_pluginData.pcfsmState = PCFSMS_HOLDOVEROUT
							continue
						}
						break done
					case PCFSMS_HOLDOVEROUT:
						//cmdRun, ok := _pluginData.cmdRun["chronyd"]
						//if ok {
						//	print("FAILOVER: Enabling chronyd after going out of holdover\n")
						//	cmdRun(_pluginData.stdoutToSocket, _pluginData.pm)
						//}
						//cmdStop, ok := _pluginData.cmdStop["phc2sys"]
						//if ok {
						//	print("FAILOVER: Disabling phc2sys after going out of holdover\n")
						//	cmdStop()
						//}
						_pluginData.pcfsmState = PCFSMS_FAILOVER
						continue
					case PCFSMS_FAILOVER:
						if pname == "ts2phc" {
							if log[:14][12:] == "90" {
								print("FAILOVER: becoming recoverable\n")
							}
							_pluginData.pcfsmState = PCFSMS_FAILOVER_RECOVERABLE
							continue
						}
						break done
					case PCFSMS_FAILOVER_RECOVERABLE:
						//cmdStop, ok := _pluginData.cmdStop["chronyd"]
						//if ok {
						//	print("FAILOVER: Disabling chronyd at recovery\n")
						//	cmdStop()
						//}
						//cmdRun, ok := _pluginData.cmdRun["phc2sys"]
						//if ok {
						//	print("FAILOVER: Enabling phc2sys at recovery\n")
						//	cmdRun(_pluginData.stdoutToSocket, _pluginData.pm)
						//}
						_pluginData.pcfsmState = PCFSMS_ACTIVE
						continue
					}
				}
				_pluginData.pcfsmMutex.Lock()
				_pluginData.pcfsmLocked = false //If took lock, then return it
				_pluginData.pcfsmMutex.Unlock()
			} else {
				print("FAILOVER: mutex unavailable, skipping \n")
			}
			*pluginData = _pluginData
		}
	}
	return ret
}

func NtpFailover(name string) (*plugin.Plugin, *interface{}) {
	if name != "ntpfailover" {
		glog.Errorf("Plugin must be initialized as 'ntpfailover'")
		return nil, nil
	}
	glog.Infof("FAILOVER: initializing plugin")
	_plugin := plugin.Plugin{Name: "ntpfailover",
		OnPTPConfigChange: OnPTPConfigChangeNtpFailover,
		RegisterProcess:   RegisterProcessNtpFailover,
		ProcessLog:        ProcessLogNtpFailover,
	}
	pluginData := NtpFailoverPluginData{pcfsmState: PCFSMS_STARTUP_UNKNOWN,
		pcfsmMutex: sync.Mutex{}}
	pluginData.cmdRun = make(map[string]func(bool, *plugin.PluginManager))
	pluginData.cmdStop = make(map[string]func())
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
