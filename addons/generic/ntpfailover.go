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
	cmdSetEnabled  map[string]func(string, bool)
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
		if _pluginData.cmdSetEnabled == nil {
			_pluginData.cmdSetEnabled = make(map[string]func(string, bool))
			print("FAILOVER: OnPTPConfigChangeFailover rebuild cmdRun dict")
		}
	}
	return nil
}

func RegisterProcessNtpFailover(data *interface{}, pname string, cmdSetEnabled func(string, bool)) {
	print("FAILOVER: RegisterProcessFailover")
	if data != nil {
		print("RegisterProcessFailover " + pname + "\n")
		_data := *data
		var pluginData *NtpFailoverPluginData = _data.(*NtpFailoverPluginData)
		_pluginData := *pluginData
		if _pluginData.cmdSetEnabled == nil {
			_pluginData.cmdSetEnabled = make(map[string]func(string, bool))
			print("FAILOVER: RegisterProcessNtpFailover rebuild cmdRun dict")
		}
		_pluginData.cmdSetEnabled[pname] = cmdSetEnabled
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
					_, foundChronyd := _pluginData.cmdSetEnabled["chronyd"]
					_, foundPhc2Sys := _pluginData.cmdSetEnabled["phc2sys"]
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
					_, foundChronyd := _pluginData.cmdSetEnabled["chronyd"]
					if foundChronyd {
						_pluginData.pcfsmState = PCSMS_STARTUP_BOTH
					} else {
						break done
					}
				case PCSMS_STARTUP_CHRONYD:
					print("FAILOVER: PCSMS_STARTUP_CHRONYD\n")
					_, foundPhc2Sys := _pluginData.cmdSetEnabled["phc2sys"]
					if foundPhc2Sys {
						_pluginData.pcfsmState = PCSMS_STARTUP_BOTH
					} else {
						break done
					}
				case PCSMS_STARTUP_BOTH:
					print("FAILOVER: PCSMS_STARTUP_BOTH\n")
					chronydSetEnabled, ok := _pluginData.cmdSetEnabled["chronyd"]
					if ok {
						print("FAILOVER: Disabling chronyd at startup\n")
						chronydSetEnabled("chronyd", false)
					}
					print("FAILOVER: DONE disabling chronyd at startup\n")
					phc2sysSetEnabled, ok := _pluginData.cmdSetEnabled["phc2sys"]
					if ok {
						phc2sysSetEnabled("phc2sys", true)
					}
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
							chronydSetEnabled, ok := _pluginData.cmdSetEnabled["chronyd"]
							if ok {
								chronydSetEnabled("chronyd", true)
							}
							phc2sysSetEnabled, ok := _pluginData.cmdSetEnabled["phc2sys"]
							if ok {
								phc2sysSetEnabled("phc2sys", false)
							}
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
	pluginData.cmdSetEnabled = make(map[string]func(string, bool))
	print("FAILOVER: OnPTPConfigChangeFailover rebuild cmdRun dict")
	pluginData.gnssValidity, _ = time.ParseDuration("30s")
	pluginData.expiryTime = time.Now().Add(pluginData.gnssValidity)
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
