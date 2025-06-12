package parser

type PTPEvent struct {
	PortID int
	Iface  string
	Role   PTPPortRole // e.g. SLAVE, MASTER, FAULTY
	Raw    string      // original line
}

type Metrics struct {
	From       string
	Iface      string // Interface or CLOCK_REALTIME
	Offset     float64
	MaxOffset  float64
	FreqAdj    float64
	Delay      float64
	ClockState string // e.g. LOCKED, FREERUN, HOLDOVER
	Source     string // e.g. "phc", "sys", or "master"
}
