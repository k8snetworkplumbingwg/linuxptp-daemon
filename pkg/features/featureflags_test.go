package features

import (
	"testing"
)

// ClearFeatures ... used by unit tests
func ClearFeatures() {
	Flags = &Features{}
}

func TestFeatureFlags(t *testing.T) {
	cases := []struct {
		versionStr    string
		expectedFlags Features
	}{
		{
			linuxPTPVersion3112,
			Features{
				oc:     true,
				bc:     true,
				dualBC: true,
			},
		},
		{
			linuxPTPVersion3116,
			Features{
				oc:       true,
				bc:       true,
				dualBC:   true,
				gm:       true,
				leapFile: true,
			},
		},
		{
			linuxPTPVersion422,
			Features{
				oc:          true,
				bc:          true,
				dualBC:      true,
				gm:          true,
				leapFile:    true,
				logSeverity: true,
				gmHoldover:  true,
				syncE:       true,
				ha:          true,
			},
		},
		{
			linuxPTPVersion441,
			Features{
				oc:          true,
				bc:          true,
				dualBC:      true,
				gm:          true,
				leapFile:    true,
				logSeverity: true,
				gmHoldover:  true,
				syncE:       true,
				ha:          true,
				twoPortOC:   true,
				bcHoldover:  true,
			},
		},
	}

	for _, c := range cases {
		ClearFeatures()
		SetVersionForTesting(c.versionStr)
		Flags.Init()
		checkFlags(t, getVersion(), c.expectedFlags)
	}
}

func checkFlags(t *testing.T, version string, expected Features) {
	if Flags.dualBC != expected.dualBC {
		t.Errorf("version %s dualBC should be %v", version, expected.dualBC)
	}
	if Flags.gm != expected.gm {
		t.Errorf("version %s gm should be %v", version, expected.gm)
	}
	if Flags.leapFile != expected.leapFile {
		t.Errorf("version %s leapFile should be %v", version, expected.leapFile)
	}
	if Flags.logSeverity != expected.logSeverity {
		t.Errorf("version %s logSeverity should be %v", version, expected.logSeverity)
	}
	if Flags.gmHoldover != expected.gmHoldover {
		t.Errorf("version %s gmHoldover should be %v", version, expected.gmHoldover)
	}
	if Flags.syncE != expected.syncE {
		t.Errorf("version %s syncE should be %v", version, expected.syncE)
	}
	if Flags.ha != expected.ha {
		t.Errorf("version %s ha should be %v", version, expected.ha)
	}
	if Flags.twoPortOC != expected.twoPortOC {
		t.Errorf("version %s twoPortOC should be %v", version, expected.twoPortOC)
	}
	if Flags.bcHoldover != expected.bcHoldover {
		t.Errorf("version %s bcHoldover should be %v", version, expected.bcHoldover)
	}
}
