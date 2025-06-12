package shared

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"sync"
)

type masterOffsetInterface struct { // by slave iface with masked index
	sync.RWMutex
	iface map[string]PtpInterface
}
type PtpInterface struct {
	Name  string
	Alias string
}
type slaveInterface struct { // current slave iface name
	sync.RWMutex
	name map[string]string
}

type masterOffsetSourceProcess struct { // current slave iface name
	sync.RWMutex
	name map[string]string
}

type SharedState struct {
	masterOffsetIface  *masterOffsetInterface
	slaveIface         *slaveInterface
	masterOffsetSource *masterOffsetSourceProcess
}

func NewSharedState() *SharedState {
	return &SharedState{
		masterOffsetIface: &masterOffsetInterface{
			RWMutex: sync.RWMutex{},
			iface:   map[string]PtpInterface{},
		},
		slaveIface: &slaveInterface{
			RWMutex: sync.RWMutex{},
			name:    map[string]string{},
		},
		masterOffsetSource: &masterOffsetSourceProcess{
			RWMutex: sync.RWMutex{},
			name:    map[string]string{},
		},
	}
}

// --- Master Offset Iface Methods ---

func (s *SharedState) DeleteMasterOffsetIface(configName string) {
	s.masterOffsetIface.Lock()
	defer s.masterOffsetIface.Unlock()
	delete(s.masterOffsetIface.iface, configName)
}

// --- Slave Iface Methods ---

func (s *SharedState) GetSlaveIface(configName string) string {
	s.slaveIface.RLock()
	defer s.slaveIface.RUnlock()
	return s.slaveIface.name[configName]
}

func (s *SharedState) DeleteSlaveIface(configName string) {
	s.slaveIface.Lock()
	defer s.slaveIface.Unlock()
	delete(s.slaveIface.name, configName)
}

func (s *SharedState) GetMasterInterface(configName string) PtpInterface {
	s.masterOffsetIface.RLock()
	defer s.masterOffsetIface.RUnlock()
	if mIface, found := s.masterOffsetIface.iface[configName]; found {
		return mIface
	}
	return PtpInterface{
		name:  "",
		alias: "",
	}
}
func (s *SharedState) GetMasterInterfaceByAlias(configName string, alias string) PtpInterface {
	s.masterOffsetIface.RLock()
	defer s.masterOffsetIface.RUnlock()
	if mIface, found := s.masterOffsetIface.iface[configName]; found {
		if mIface.alias == alias {
			return mIface
		}
	}
	return PtpInterface{
		name:  alias,
		alias: alias,
	}
}

func (s *SharedState) GetAliasByName(configName string, name string) PtpInterface {
	if name == "CLOCK_REALTIME" || name == "master" {
		return PtpInterface{
			name:  name,
			alias: name,
		}
	}
	s.masterOffsetIface.RLock()
	defer s.masterOffsetIface.RUnlock()
	if mIface, found := s.masterOffsetIface.iface[configName]; found {
		if mIface.name == name {
			return mIface
		}
	}
	return PtpInterface{
		name:  name,
		alias: name,
	}
}
func (s *SharedState) SetMasterOffsetIface(configName string, value string) {
	s.masterOffsetIface.Lock()
	defer s.masterOffsetIface.Unlock()
	s.masterOffsetIface.iface[configName] = PtpInterface{
		name:  value,
		alias: utils.GetAlias(value),
	}
}

func (s *SharedState) SetSlaveIface(configName string, value string) {
	s.slaveIface.Lock()
	defer s.slaveIface.Unlock()
	s.slaveIface.name[configName] = value
}

func (s *SharedState) IsFaultySlaveIface(configName string, iface string) bool {
	s.slaveIface.RLock()
	defer s.slaveIface.RUnlock()

	if si, found := s.slaveIface.name[configName]; found {
		if si == iface {
			return true
		}
	}
	return false
}

func (s *SharedState) SetMasterOffsetSource(configName string, value string) {
	s.masterOffsetSource.Lock()
	defer s.masterOffsetSource.Unlock()
	s.masterOffsetSource.name[configName] = value
}

func (s *SharedState) GetMasterOffsetSource(configName string) string {
	if s, found := s.masterOffsetSource.name[configName]; found {
		return s
	}
	return parser.PTP4L // default is ptp4l
}
