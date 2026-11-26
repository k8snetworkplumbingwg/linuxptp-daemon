package clock

import (
	"sync/atomic"

	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

var sourceGeneration atomic.Uint64

// PTPInstanceSource identifies a TR instance's timing stream across hitless
// upstream reselection. The selected physical port is metadata, not its identity.
type PTPInstanceSource struct {
	ConfigName string
	Generation uint64
}

// Equal reports whether other identifies the same TR instance and lifecycle.
func (s PTPInstanceSource) Equal(other utils.QualificationSource) bool {
	instance, ok := other.(PTPInstanceSource)
	return ok && s == instance
}

// String returns the TR configuration name.
func (s PTPInstanceSource) String() string { return s.ConfigName }

type ptpSourceQualification struct {
	state                   *utils.QualificationState
	configuredUpstreamPorts []string
	lastSelectedPort        string
	selection               parserconstants.BMCASelection
	configName              string
	generation              uint64 // Qualification lifecycle; stable across port reselection.
	revision                uint64 // Readiness version; rejects stale hardware acknowledgements.
	inputEnabled            bool
}

func (q *ptpSourceQualification) source() utils.QualificationSource {
	return PTPInstanceSource{ConfigName: q.configName, Generation: q.generation}
}

func (q *ptpSourceQualification) ready() bool {
	return q.selection == parserconstants.BMCAExternal && q.state.Qualified()
}
