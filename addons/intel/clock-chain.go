package intel

import (
	"fmt"
	"os"
	"slices"
	"strconv"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type ClockChainType int
type ClockChain struct {
	Type       ClockChainType
	LeadingNIC CardInfo
	DpllPins   []*dpll.PinInfo
}
type CardInfo struct {
	name        string
	dpllClockId string
	// upstreamPort specifies the slave port in the T-BC case. For example, if the "name"
	// 	is ens4f0, the "upstreamPort" could be ens4f1, depending on ptp4l config
	upstreamPort string
	pins         map[string]dpll.PinInfo
}

const (
	ClockTypeUnset ClockChainType = iota
	ClockTypeTGM
	ClockTypeTBC
	PrioEnable  = 0
	PrioDisable = 255
)

var ClockTypesMap = map[string]ClockChainType{
	"":     ClockTypeUnset,
	"T-GM": ClockTypeTGM,
	"T-BC": ClockTypeTBC, // Use the same for T-TSC
}

func (ch *ClockChain) GetLiveDpllPinsInfo() error {
	if !unitTest {
		conn, err := dpll.Dial(nil)
		if err != nil {
			return fmt.Errorf("failed to dial DPLL: %v", err)
		}
		defer conn.Close()
		ch.DpllPins, err = conn.DumpPinGet()
		if err != nil {
			return fmt.Errorf("failed to dump DPLL pins: %v", err)
		}
	} else {
		ch.DpllPins = DpllPins
	}
	return nil
}

func (ch *ClockChain) ResolveInterconnections(e810Opts E810Opts, nodeProfile *ptpv1.PtpProfile) (*[]delayCompensation, error) {
	compensations := []delayCompensation{}
	for _, card := range e810Opts.InputDelays {
		delays, err := InitInternalDelays(card.Part)
		if err != nil {
			return nil, err
		}
		if card.Input != nil {
			externalDelay := card.Input.DelayPs
			connector := card.Input.Connector
			link := findInternalLink(delays.ExternalInputs, connector)
			if link == nil {
				return nil, fmt.Errorf("plugin E810 error: can't find connector %s in the card %s spec", connector, card.Part)
			}
			var pinLabel string
			var internalDelay int32

			pinLabel = link.Pin
			internalDelay = link.DelayPs
			clockId, err := addClockId(card.Id, nodeProfile)
			if err != nil {
				return nil, err
			}

			compensations = append(compensations, delayCompensation{
				DelayPs:   int32(externalDelay) + internalDelay,
				pinLabel:  pinLabel,
				iface:     card.Id,
				direction: "input",
				clockId:   *clockId,
			})
		} else {
			ch.LeadingNIC.name = card.Id
			ch.LeadingNIC.upstreamPort = card.UpstreamPort
			clockId, err := addClockId(card.Id, nodeProfile)
			if err != nil {
				return nil, err
			}
			ch.LeadingNIC.dpllClockId = *clockId
			if card.GnssInput {
				ch.Type = ClockTypeTGM
				gnssLink := &delays.GnssInput
				compensations = append(compensations, delayCompensation{
					DelayPs:   gnssLink.DelayPs,
					pinLabel:  gnssLink.Pin,
					iface:     card.Id,
					direction: "input",
					clockId:   *clockId,
				})
			} else {
				// if no GNSS and no external, then ptp4l input
				ch.Type = ClockTypeTBC
			}
		}
		for _, outputConn := range card.PhaseOutputConnectors {
			link := findInternalLink(delays.ExternalOutputs, outputConn)
			if link == nil {
				return nil, fmt.Errorf("plugin E810 error: can't find connector %s in the card %s spec", outputConn, card.Part)
			}
			clockId, err := addClockId(card.Id, nodeProfile)
			if err != nil {
				return nil, err
			}
			compensations = append(compensations, delayCompensation{
				DelayPs:   link.DelayPs,
				pinLabel:  link.Pin,
				iface:     card.Id,
				direction: "output",
				clockId:   *clockId,
			})
		}
	}
	return &compensations, nil
}

func InitClockChain(e810Opts E810Opts, nodeProfile *ptpv1.PtpProfile) (*ClockChain, error) {
	var chain = &ClockChain{
		LeadingNIC: CardInfo{
			pins: make(map[string]dpll.PinInfo, 0),
		},
	}

	err := chain.GetLiveDpllPinsInfo()
	if err != nil {
		return chain, err
	}
	comps, err := chain.ResolveInterconnections(e810Opts, nodeProfile)
	if err != nil {
		glog.Errorf("fail to get delay compensations, %s", err)
	}
	if !unitTest {
		err = sendDelayCompensation(comps, chain.DpllPins)
		if err != nil {
			glog.Errorf("fail to send delay compensations, %s", err)
		}
	}
	err = chain.GetLeadingCardSDP()
	if err != nil {
		glog.Fatal(err)
		return chain, err
	}
	if chain.Type == ClockTypeTBC {
		(*nodeProfile).PtpSettings["clockType"] = "T-BC"
		glog.Info("about to init TBC pins")
		_, err = chain.InitPinsTBC()
		if err != nil {
			return chain, fmt.Errorf("failed to initialize pins for T-BC operation: %s", err.Error())
		}
		glog.Info("about to enter TBC Normal mode")
		_, err = chain.EnterNormalTBC()
		if err != nil {
			return chain, fmt.Errorf("failed to enter T-BC normal mode: %s", err.Error())
		}
	} else {
		(*nodeProfile).PtpSettings["clockType"] = "T-GM"
		glog.Info("about to init TGM pins")
		_, err = chain.InitPinsTGM()
	}
	return chain, err
}

const (
	sdp20 = "CVL-SDP20"
	sdp21 = "CVL-SDP21"
	sdp22 = "CVL-SDP22"
	sdp23 = "CVL-SDP23"
	gnss  = "GNSS-1PPS"
)

var internalPinLabels = []string{sdp20, sdp21, sdp22, sdp23, gnss}

func (ch *ClockChain) GetLeadingCardSDP() error {
	clockId, err := strconv.ParseUint(ch.LeadingNIC.dpllClockId, 10, 64)
	if err != nil {
		return err
	}
	for _, pin := range ch.DpllPins {
		if pin.ClockId == clockId && slices.Contains(internalPinLabels, pin.BoardLabel) {
			ch.LeadingNIC.pins[pin.BoardLabel] = *pin
		}
	}
	return nil
}

func writeSysFs(path string, val string) error {
	glog.Infof("writing " + val + " to " + path)
	err := os.WriteFile(path, []byte(val), 0666)
	if err != nil {
		return fmt.Errorf("e810 failed to write " + val + " to " + path + ": " + err.Error())
	}
	return nil
}

func SetPinControlData(pin dpll.PinInfo, enabled bool) *dpll.PinParentDeviceCtl {
	Pin := dpll.PinParentDeviceCtl{
		Id:           pin.Id,
		PinParentCtl: make([]dpll.PinControl, 0),
	}
	for _, pin := range pin.ParentDevice {
		pc := dpll.PinControl{}
		pc.PinParentId = pin.ParentId
		if pin.Direction == dpll.DPLL_PIN_DIRECTION_INPUT {
			pc.Prio = func(enabled bool) *uint32 {
				var p uint32
				if enabled {
					p = PrioEnable
				} else {
					p = PrioDisable
				}
				return &p
			}(enabled)
		} else {
			pc.State = func(enabled bool) *uint32 {
				var s uint32
				if enabled {
					s = dpll.DPLL_PIN_STATE_CONNECTED
				} else {
					s = dpll.DPLL_PIN_STATE_DISCONNECTED
				}
				return &s
			}(enabled)
		}
		Pin.PinParentCtl = append(Pin.PinParentCtl, pc)
	}
	return &Pin
}

func (c *ClockChain) SetPinsControlData(pins []string, enable []bool) (*[]dpll.PinParentDeviceCtl, error) {
	pinCommands := []dpll.PinParentDeviceCtl{}
	if len(pins) != len(enable) {
		return nil, fmt.Errorf("pin control data invalid - 'pins' and 'enable' must be the same length")
	}
	for i := range pins {
		pin, found := c.LeadingNIC.pins[pins[i]]
		if !found {
			return nil, fmt.Errorf("%s pin not found in the leading card", pins[i])
		}
		pinCommand := SetPinControlData(pin, enable[i])
		pinCommands = append(pinCommands, *pinCommand)
	}
	return &pinCommands, nil
}

func (c *ClockChain) EnableE810Outputs() error {
	// # echo 2 0 0 1 0 > /sys/class/net/$ETH/device/ptp/ptp*/period
	var pinPath string
	if unitTest {
		glog.Info("skip pin config in unit test")
		return nil
	} else {
		deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", c.LeadingNIC.name)
		phcs, err := os.ReadDir(deviceDir)
		if err != nil {
			return fmt.Errorf("e810 failed to read " + deviceDir + ": " + err.Error())
		}
		for _, phc := range phcs {
			pinPath = fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/period", c.LeadingNIC.name, phc.Name())
			for _, value := range []string{"2 0 0 1 0"} {
				err := writeSysFs(pinPath, value)
				if err != nil {
					return fmt.Errorf("failed to write " + value + " to " + pinPath + ": " + err.Error())
				}
			}
		}
	}
	return nil
}

// InitPinsTBC initializes the leading card E810 and DPLL pins for T-BC operation
func (c *ClockChain) InitPinsTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Set periodic output on SDP20 and SDP22
	// (To synchronize the DPLL1 to the E810 PHC synced by ptp4l):
	err := c.EnableE810Outputs()
	if err != nil {
		return nil, err
	}
	// Disable GNSS-1PPS
	pins := []string{gnss}
	enable := []bool{false}
	commands, err := c.SetPinsControlData(pins, enable)
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

// EnterHoldoverTBC configures the leading card DPLL pins for T-BC holdover
func (c *ClockChain) EnterHoldoverTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Disable DPLL inputs from e810 (SDP20, SDP22)
	// Enable DPLL Outputs to e810 (SDP21, SDP23)
	pins := []string{sdp20, sdp22, sdp23}
	enable := []bool{false, false, true}
	commands, err := c.SetPinsControlData(pins, enable)
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

// EnterNormalTBC configures the leading card DPLL pins for regular T-BC operation
func (c *ClockChain) EnterNormalTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Disable DPLL Outputs to e810 (SDP23)
	// Enable DPLL inputs from e810 (SDP20, SDP22)
	pins := []string{sdp20, sdp22, sdp23}
	enable := []bool{true, true, false}
	commands, err := c.SetPinsControlData(pins, enable)
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

func (c *ClockChain) InitPinsTGM() (*[]dpll.PinParentDeviceCtl, error) {
	// Set GNSS-1PPS priority to 0 (max priority)
	// Disable DPLL inputs from e810 (SDP20, SDP22)
	// Enable DPLL Outputs to e810 (SDP21, SDP23)
	pins := []string{sdp20, sdp22, gnss, sdp21, sdp23}
	enable := []bool{false, false, true, true, true}
	commands, err := c.SetPinsControlData(pins, enable)
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

func BatchPinSet(commands *[]dpll.PinParentDeviceCtl) error {
	if unitTest {
		return nil
	}
	conn, err := dpll.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %v", err)
	}
	defer conn.Close()
	for _, command := range *commands {
		glog.Infof("DPLL pin command %++v", command)
		b, err := dpll.EncodePinControl(command)
		if err != nil {
			return err
		}
		err = conn.SendCommand(dpll.DPLL_CMD_PIN_SET, b)
		if err != nil {
			glog.Error("failed to send pin command: ", err)
			return err
		}
		info, err := conn.DoPinGet(dpll.DoPinGetRequest{Id: command.Id})
		if err != nil {
			glog.Error("failed to get pin: ", err)
			return err
		}
		reply, err := dpll.GetPinInfoHR(info)
		if err != nil {
			glog.Error("failed to convert pin reply to human readable: ", err)
			return err
		}
		glog.Info("pin reply: ", string(reply))
	}
	return nil
}
