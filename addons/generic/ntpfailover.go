package generic

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"regexp"
	"sync"
	"time"
)

type NtpFailoverPluginData struct {
	cmdStop        map[string]func()
	cmdRun         map[string]func(bool, *plugin.PluginManager)
	stdoutToSocket bool
	pm             *plugin.PluginManager
	pcfsmState     int
	pcfsmMutex     sync.Mutex
	pcfsmLocked    bool
	gnssValidity   time.Duration
	expiryTime     time.Time
}

const ( // phc2sys/chronyd Finite State Machine States
	PCSMS_STARTUP_DEFAULT int = iota // Just started, both are unknown
	PCSMS_STARTUP_PHC2SYS            // phc2sys setting time
	PCSMS_STARTUP_CHRONYD            // phc2sys setting time
	PCSMS_STARTUP_BOTH               // phc2sys setting time
	PCSMS_ACTIVE                     // phc2sys setting time
	PCSMS_OUTOFSPEC                  // switch to chronyd setting time
	PCSMS_FAILOVER                   // chronyd setting time

)

var (
	addingTimestampRegex = regexp.MustCompile("nmea sentence: GN")
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
	if data != nil {

		_data := *data
		var pluginData *NtpFailoverPluginData = _data.(*NtpFailoverPluginData)
		_pluginData := *pluginData
		currentTime := time.Now()

		if pname == "ts2phc" && addingTimestampRegex.MatchString(log) {
			_pluginData.expiryTime = currentTime.Add(_pluginData.gnssValidity)
			print("ProcessLogNtpFailover: ")
			print(_pluginData.expiryTime.GoString())
			print(log)
			print("\n")

		}

		_pluginData.pcfsmMutex.Lock()
		ownLock := !_pluginData.pcfsmLocked //If locked, then skip, otherwise take lock
		_pluginData.pcfsmMutex.Unlock()
		if ownLock {
		done:
			for {
				switch _pluginData.pcfsmState {
				case PCSMS_STARTUP_DEFAULT:
					print("FAILOVER: PCSMS_STARTUP_DEFAULT\n")
					_, foundChronyd := _pluginData.cmdStop["chronyd"]
					_, foundPhc2Sys := _pluginData.cmdRun["phc2sys"]
					if foundChronyd && foundPhc2Sys {
						_pluginData.pcfsmState = PCSMS_STARTUP_BOTH
					} else if foundChronyd {
						_pluginData.pcfsmState = PCSMS_STARTUP_CHRONYD
					} else if foundPhc2Sys {
						_pluginData.pcfsmState = PCSMS_STARTUP_PHC2SYS
					} else {
						break done
					}
				case PCSMS_STARTUP_PHC2SYS:
					print("FAILOVER: PCSMS_STARTUP_PHC2SYS\n")
					_, foundChronyd := _pluginData.cmdStop["chronyd"]
					if foundChronyd {
						_pluginData.pcfsmState = PCSMS_STARTUP_BOTH
					} else {
						break done
					}
				case PCSMS_STARTUP_CHRONYD:
					print("FAILOVER: PCSMS_STARTUP_CHRONYD\n")
					_, foundPhc2Sys := _pluginData.cmdStop["phc2sys"]
					if foundPhc2Sys {
						_pluginData.pcfsmState = PCSMS_STARTUP_BOTH
					} else {
						break done
					}
				case PCSMS_STARTUP_BOTH:
					print("FAILOVER: PCSMS_STARTUP_BOTH\n")
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
					_pluginData.pcfsmState = PCSMS_ACTIVE
					print("FAILOVER: goto PCSMS_ACTIVE\n")
					continue
				case PCSMS_ACTIVE:
					if pname == "ts2phc" {
						if currentTime.After(_pluginData.expiryTime) {
							print("FAILOVER: out of spec\n")
							_pluginData.pcfsmState = PCSMS_OUTOFSPEC
							continue
						}
					}
					break done
				case PCSMS_OUTOFSPEC:
					if pname == "ts2phc" {
						if currentTime.After(_pluginData.expiryTime) {
							print("FAILOVER: switching to ntp time source\n")
							_pluginData.pcfsmState = PCSMS_FAILOVER
							continue
						}
					}
					_pluginData.pcfsmState = PCSMS_FAILOVER
					break done
				case PCSMS_FAILOVER:
					if pname == "ts2phc" {
						if currentTime.Before(_pluginData.expiryTime) {
							print("FAILOVER: recovering\n")
							_pluginData.pcfsmState = PCSMS_STARTUP_DEFAULT
							continue
						}
					}
					break done
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
	pluginData := NtpFailoverPluginData{pcfsmState: PCSMS_STARTUP_DEFAULT,
		pcfsmMutex: sync.Mutex{}}
	pluginData.cmdRun = make(map[string]func(bool, *plugin.PluginManager))
	pluginData.cmdStop = make(map[string]func())
	pluginData.gnssValidity, _ = time.ParseDuration("30s")
	pluginData.expiryTime = time.Now().Add(pluginData.gnssValidity)
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
