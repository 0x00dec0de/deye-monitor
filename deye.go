package main

import (
	"fmt"
	"time"
)

// Status is one decoded snapshot of a Deye single-phase hybrid inverter
// (SUN-xK-SG0xLP1 register map, holding registers 0-260).
type Status struct {
	Time        time.Time `json:"time"`
	Serial      string    `json:"serial"`
	RatedPowerW float64   `json:"rated_power_w"`

	State     int    `json:"state"`
	StateText string `json:"state_text"`
	Source    string `json:"source"` // grid | battery | solar | grid+battery

	GridConnected bool    `json:"grid_connected"`
	GridVoltage   float64 `json:"grid_voltage"`
	GridCurrent   float64 `json:"grid_current"`
	GridFrequency float64 `json:"grid_frequency"`
	GridPowerW    int     `json:"grid_power_w"` // + import, - export

	LoadVoltage float64 `json:"load_voltage"`
	LoadPowerW  int     `json:"load_power_w"`
	InverterW   int     `json:"inverter_power_w"`
	InverterHz  float64 `json:"inverter_frequency"`

	PV1Voltage float64 `json:"pv1_voltage"`
	PV1Current float64 `json:"pv1_current"`
	PV1PowerW  int     `json:"pv1_power_w"`
	PV2Voltage float64 `json:"pv2_voltage"`
	PV2Current float64 `json:"pv2_current"`
	PV2PowerW  int     `json:"pv2_power_w"`
	PVPowerW   int     `json:"pv_power_w"`

	BatterySOC       int     `json:"battery_soc"`
	BatteryVoltage   float64 `json:"battery_voltage"`
	BatteryCurrent   float64 `json:"battery_current"` // + discharge, - charge
	BatteryPowerW    int     `json:"battery_power_w"` // + discharge, - charge
	BatteryTemp      float64 `json:"battery_temp"`
	BatteryCapAh     int     `json:"battery_capacity_ah"`
	BatteryEnergyKWh float64 `json:"battery_energy_kwh"`
	UsableEnergyKWh  float64 `json:"usable_energy_kwh"`
	RuntimeMinutes   float64 `json:"runtime_minutes"` // estimated at current load

	TempDC float64 `json:"temp_dc"`
	TempAC float64 `json:"temp_ac"`

	DailyPVKWh        float64 `json:"daily_pv_kwh"`
	DailyLoadKWh      float64 `json:"daily_load_kwh"`
	DailyGridBuyKWh   float64 `json:"daily_grid_buy_kwh"`
	DailyGridSellKWh  float64 `json:"daily_grid_sell_kwh"`
	DailyChargeKWh    float64 `json:"daily_charge_kwh"`
	DailyDischargeKWh float64 `json:"daily_discharge_kwh"`
	TotalPVKWh        float64 `json:"total_pv_kwh"`
	TotalLoadKWh      float64 `json:"total_load_kwh"`
	TotalGridBuyKWh   float64 `json:"total_grid_buy_kwh"`
	TotalGridSellKWh  float64 `json:"total_grid_sell_kwh"`
	TotalChargeKWh    float64 `json:"total_charge_kwh"`
	TotalDischargeKWh float64 `json:"total_discharge_kwh"`

	Settings BatterySettings `json:"settings"`
	Faults   []int           `json:"faults,omitempty"`
}

type BatterySettings struct {
	EqualizationV float64 `json:"equalization_v"`
	AbsorptionV   float64 `json:"absorption_v"`
	FloatV        float64 `json:"float_v"`
	EmptyV        float64 `json:"empty_v"`
	ShutdownSOC   int     `json:"shutdown_soc"`
	RestartSOC    int     `json:"restart_soc"`
	LowSOC        int     `json:"low_soc"`
}

var stateNames = map[int]string{0: "standby", 1: "self-check", 2: "normal", 3: "alarm", 4: "fault"}

// ReadStatus reads registers 0-260 in chunks and decodes them.
func ReadStatus(c *SolarmanClient) (*Status, error) {
	regs := make([]uint16, 0, 260)
	for _, chunk := range [][2]uint16{{0, 100}, {100, 100}, {200, 60}} {
		v, err := c.ReadHoldingRegisters(chunk[0], chunk[1])
		if err != nil {
			return nil, fmt.Errorf("read regs %d-%d: %w", chunk[0], chunk[0]+chunk[1]-1, err)
		}
		regs = append(regs, v...)
	}
	return decode(regs), nil
}

func decode(r []uint16) *Status {
	s16 := func(i int) int { return int(int16(r[i])) }
	u32 := func(lo, hi int) float64 { return float64(uint32(r[hi])<<16 | uint32(r[lo])) }
	temp := func(i int) float64 { return (float64(r[i]) - 1000) / 10 }

	s := &Status{Time: time.Now()}
	for i := 3; i <= 7; i++ {
		s.Serial += string(rune(r[i]>>8)) + string(rune(r[i]&0xff))
	}
	s.RatedPowerW = u32(16, 17) / 10

	s.State = int(r[59])
	s.StateText = stateNames[s.State]
	if s.StateText == "" {
		s.StateText = fmt.Sprintf("unknown(%d)", s.State)
	}
	for i := 101; i <= 106; i++ {
		if r[i] != 0 {
			s.Faults = append(s.Faults, i, int(r[i]))
		}
	}

	s.GridVoltage = float64(r[150]) / 10
	s.GridCurrent = float64(r[160]) / 100
	s.GridFrequency = float64(r[192]) / 100
	s.GridPowerW = s16(169)
	s.GridConnected = r[194] == 1 && s.GridVoltage > 100

	s.LoadVoltage = float64(r[157]) / 10
	s.LoadPowerW = s16(178)
	s.InverterW = s16(175)
	s.InverterHz = float64(r[193]) / 100

	s.PV1Voltage, s.PV1Current, s.PV1PowerW = float64(r[109])/10, float64(r[110])/10, int(r[186])
	s.PV2Voltage, s.PV2Current, s.PV2PowerW = float64(r[111])/10, float64(r[112])/10, int(r[187])
	s.PVPowerW = s.PV1PowerW + s.PV2PowerW

	s.BatterySOC = int(r[184])
	s.BatteryVoltage = float64(r[183]) / 100
	s.BatteryCurrent = float64(s16(191)) / 100
	s.BatteryPowerW = s16(190)
	s.BatteryTemp = temp(182)
	s.BatteryCapAh = int(r[204])

	s.TempDC, s.TempAC = temp(90), temp(91)

	s.DailyPVKWh = float64(r[108]) / 10
	s.DailyLoadKWh = float64(r[84]) / 10
	s.DailyGridBuyKWh = float64(r[76]) / 10
	s.DailyGridSellKWh = float64(r[77]) / 10
	s.DailyChargeKWh = float64(r[70]) / 10
	s.DailyDischargeKWh = float64(r[71]) / 10
	s.TotalPVKWh = u32(96, 97) / 10
	s.TotalLoadKWh = u32(85, 86) / 10
	s.TotalGridBuyKWh = u32(78, 80) / 10
	s.TotalGridSellKWh = u32(81, 82) / 10
	s.TotalChargeKWh = u32(72, 73) / 10
	s.TotalDischargeKWh = u32(74, 75) / 10

	s.Settings = BatterySettings{
		EqualizationV: float64(r[201]) / 100,
		AbsorptionV:   float64(r[202]) / 100,
		FloatV:        float64(r[203]) / 100,
		EmptyV:        float64(r[205]) / 100,
		ShutdownSOC:   int(r[217]),
		RestartSOC:    int(r[218]),
		LowSOC:        int(r[219]),
	}

	// Power source classification for the UPS view.
	switch {
	case !s.GridConnected && s.PVPowerW > s.LoadPowerW:
		s.Source = "solar"
	case !s.GridConnected:
		s.Source = "battery"
	case s.BatteryPowerW > 50:
		s.Source = "grid+battery"
	case s.PVPowerW > s.LoadPowerW:
		s.Source = "solar"
	default:
		s.Source = "grid"
	}

	// Runtime estimate: usable energy above the shutdown SOC at the present
	// load (or the actual discharge power when already on battery).
	const nominalV = 51.2 // 16s LiFePO4 nominal
	s.BatteryEnergyKWh = float64(s.BatteryCapAh) * nominalV / 1000
	usableSOC := float64(s.BatterySOC - s.Settings.ShutdownSOC)
	if usableSOC < 0 {
		usableSOC = 0
	}
	s.UsableEnergyKWh = s.BatteryEnergyKWh * usableSOC / 100
	drain := s.LoadPowerW
	if s.BatteryPowerW > 50 {
		drain = s.BatteryPowerW
	}
	if drain > 0 && s.BatteryCapAh > 0 {
		s.RuntimeMinutes = s.UsableEnergyKWh * 1000 / float64(drain) * 60
	}
	return s
}
