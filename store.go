package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Sample is a compact history point kept in memory for the charts.
type Sample struct {
	T       int64 `json:"t"` // unix seconds
	SOC     int   `json:"soc"`
	Load    int   `json:"load"`
	Battery int   `json:"bat"`
	Grid    int   `json:"grid"`
	PV      int   `json:"pv"`
	OnGrid  bool  `json:"on_grid"`
}

type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"` // grid_lost | grid_restored | low_battery | state | start
	Message string    `json:"message"`
}

// failuresBeforeDown is how many consecutive failed polls count as a lost
// connection to the inverter; shorter blips (flaky WiFi on the logger stick)
// are ignored. Connection problems are shown as a banner, not as events, so
// they never crowd out the power events.
const failuresBeforeDown = 3

type Store struct {
	mu         sync.RWMutex
	latest     *Status
	lastErr    string
	lastOK     time.Time
	samples    []Sample
	events     []Event
	maxAge     time.Duration
	maxEv      int // per category
	failures   int
	downSince  time.Time
	gridLostAt time.Time
	path       string
	dirty      bool
	onEvent    func(Event)
}

type persisted struct {
	Samples []Sample `json:"samples"`
	Events  []Event  `json:"events"`
}

func NewStore(path string, maxAge time.Duration) *Store {
	s := &Store{maxAge: maxAge, maxEv: 300, path: path}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var p persisted
			if json.Unmarshal(b, &p) == nil {
				s.samples = p.Samples
				for _, e := range p.Events { // drop old-style connection error events
					if e.Kind != "error" {
						s.events = append(s.events, e)
					}
				}
				s.trimLocked()
			}
		}
	}
	return s
}

func (s *Store) Update(st *Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.latest
	var lastSample time.Time
	var lastOnGrid bool
	if n := len(s.samples); n > 0 {
		lastSample = time.Unix(s.samples[n-1].T, 0)
		lastOnGrid = s.samples[n-1].OnGrid
	}
	s.latest, s.lastErr, s.lastOK, s.dirty = st, "", st.Time, true
	if s.failures >= failuresBeforeDown {
		log.Printf("connection to inverter restored after %s", fmtDur(st.Time.Sub(s.downSince)))
	}
	s.failures = 0
	s.samples = append(s.samples, Sample{
		T: st.Time.Unix(), SOC: st.BatterySOC, Load: st.LoadPowerW,
		Battery: st.BatteryPowerW, Grid: st.GridPowerW, PV: st.PVPowerW, OnGrid: st.GridConnected,
	})
	s.trimLocked()

	if prev == nil {
		// A quick restart (deploy, reboot) right after the last stored sample
		// is not worth an event; only log real monitoring gaps.
		if lastSample.IsZero() || st.Time.Sub(lastSample) > 10*time.Minute {
			s.addEventLocked(st.Time, "start", "monitor started, source: "+st.Source)
		}
		// The grid may have changed state while the monitor was down (for
		// example the PC itself lost power during the switch to battery).
		if !lastSample.IsZero() {
			window := " (while monitor was down, between " + lastSample.Format("15:04:05") + " and " + st.Time.Format("15:04:05") + ")"
			if lastOnGrid && !st.GridConnected {
				s.gridLostAt = lastSample
				s.addEventLocked(st.Time, "grid_lost", "grid lost, running on "+st.Source+window)
			} else if !lastOnGrid && st.GridConnected {
				s.addEventLocked(st.Time, "grid_restored", "grid restored"+window)
			}
		}
		return
	}
	if prev.GridConnected && !st.GridConnected {
		s.gridLostAt = st.Time
		s.addEventLocked(st.Time, "grid_lost", "grid lost, running on "+st.Source)
	} else if !prev.GridConnected && st.GridConnected {
		msg := "grid restored"
		// the just-appended sample is on grid, so outageLocked finds the
		// stretch that ended now, even if the monitor restarted during it
		if o := s.outageLocked(); o != nil && !o.Ongoing {
			msg += " after " + fmtDur(st.Time.Sub(o.Since))
		} else if !s.gridLostAt.IsZero() {
			msg += " after " + fmtDur(st.Time.Sub(s.gridLostAt))
		}
		s.addEventLocked(st.Time, "grid_restored", msg)
	}
	if prev.State != st.State {
		s.addEventLocked(st.Time, "state", "inverter state: "+prev.StateText+" -> "+st.StateText)
	}
	if st.BatterySOC <= st.Settings.LowSOC && prev.BatterySOC > st.Settings.LowSOC {
		s.addEventLocked(st.Time, "low_battery", "battery low")
	}
}

func (s *Store) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err.Error()
	s.failures++
	if s.failures == 1 {
		s.downSince = time.Now()
	}
	if s.failures == failuresBeforeDown {
		log.Printf("connection to inverter lost: %v", err)
	}
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	d = d.Round(time.Minute)
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("%dh %dm", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func (s *Store) addEventLocked(t time.Time, kind, msg string) {
	ev := Event{Time: t, Kind: kind, Message: msg}
	s.events = append(s.events, ev)
	if s.onEvent != nil {
		s.onEvent(ev)
	}
	if len(s.events) > s.maxEv {
		s.events = s.events[len(s.events)-s.maxEv:]
	}
	log.Printf("event: %s: %s", kind, msg)
}

func (s *Store) trimLocked() {
	cut := time.Now().Add(-s.maxAge).Unix()
	i := 0
	for i < len(s.samples) && s.samples[i].T < cut {
		i++
	}
	s.samples = s.samples[i:]
}

// Outage describes the current (or, when on grid, the most recent) stretch of
// running without the grid, derived from the stored samples.
type Outage struct {
	Since         time.Time  `json:"since"`
	Until         *time.Time `json:"until,omitempty"` // nil while ongoing
	Ongoing       bool       `json:"ongoing"`
	DurationS     int64      `json:"duration_s"`
	AvgDischargeW int        `json:"avg_discharge_w"` // time-weighted mean of battery discharge
	AvgLoadW      int        `json:"avg_load_w"`      // time-weighted mean of the load
	EnergyKWh     float64    `json:"energy_kwh"`      // energy drawn from the battery
}

// avgLoadLocked is the time-weighted mean load over samples[from..to],
// ignoring gaps where the monitor was not running.
func (s *Store) avgLoadLocked(from, to int) int {
	var sum, dur float64
	for i := from + 1; i <= to; i++ {
		dt := float64(s.samples[i].T - s.samples[i-1].T)
		if dt > 300 {
			continue
		}
		sum += float64(s.samples[i-1].Load+s.samples[i].Load) / 2 * dt
		dur += dt
	}
	if dur == 0 {
		return 0
	}
	return int(sum / dur)
}

// outageLocked scans the samples backwards for the last off-grid stretch.
func (s *Store) outageLocked() *Outage {
	n := len(s.samples)
	end := n - 1
	for end >= 0 && s.samples[end].OnGrid {
		end--
	}
	if end < 0 {
		return nil
	}
	start := end
	for start > 0 && !s.samples[start-1].OnGrid {
		start--
	}
	o := &Outage{Since: time.Unix(s.samples[start].T, 0), Ongoing: end == n-1}
	var sum, dur float64
	for i := start + 1; i <= end; i++ {
		dt := float64(s.samples[i].T - s.samples[i-1].T)
		if dt > 300 { // monitor was down: do not count the gap as discharge time
			dt = 0
		}
		v := float64(s.samples[i-1].Battery+s.samples[i].Battery) / 2
		if v > 0 {
			sum += v * dt
			dur += dt
		}
	}
	last := time.Unix(s.samples[end].T, 0)
	if o.Ongoing {
		last = time.Now()
	} else {
		o.Until = &last
	}
	o.DurationS = int64(last.Sub(o.Since).Seconds())
	if dur > 0 {
		o.AvgDischargeW = int(sum / dur)
	}
	o.AvgLoadW = s.avgLoadLocked(start, end)
	o.EnergyKWh = sum / 3600 / 1000
	return o
}

type Snapshot struct {
	Status    *Status   `json:"status"`
	Error     string    `json:"error,omitempty"`
	LastOK    time.Time `json:"last_ok"`
	Stale     bool      `json:"stale"`
	ConnDown  bool      `json:"connection_down"`
	DownSince time.Time `json:"down_since,omitempty"`
	Outage    *Outage   `json:"outage,omitempty"`
	AvgLoad24 int       `json:"avg_load_24h_w"`
	Events    []Event   `json:"events"`
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ev := make([]Event, len(s.events))
	copy(ev, s.events)
	// newest first
	for i, j := 0, len(ev)-1; i < j; i, j = i+1, j-1 {
		ev[i], ev[j] = ev[j], ev[i]
	}
	snap := Snapshot{Status: s.latest, Error: s.lastErr, LastOK: s.lastOK,
		Stale: time.Since(s.lastOK) > 2*time.Minute, Events: ev}
	if s.failures >= failuresBeforeDown {
		snap.ConnDown, snap.DownSince = true, s.downSince
	}
	snap.Outage = s.outageLocked()
	if n := len(s.samples); n > 1 {
		cut := time.Now().Add(-24 * time.Hour).Unix()
		from := n - 1
		for from > 0 && s.samples[from-1].T >= cut {
			from--
		}
		snap.AvgLoad24 = s.avgLoadLocked(from, n-1)
	}
	return snap
}

// History returns samples within the window, thinned to at most maxPoints.
func (s *Store) History(window time.Duration, maxPoints int) []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cut := time.Now().Add(-window).Unix()
	i := 0
	for i < len(s.samples) && s.samples[i].T < cut {
		i++
	}
	src := s.samples[i:]
	if len(src) <= maxPoints {
		out := make([]Sample, len(src))
		copy(out, src)
		return out
	}
	step := float64(len(src)) / float64(maxPoints)
	out := make([]Sample, 0, maxPoints)
	for k := 0; k < maxPoints; k++ {
		out = append(out, src[int(float64(k)*step)])
	}
	return out
}

func (s *Store) Save() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	b, err := json.Marshal(persisted{Samples: s.samples, Events: s.events})
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, s.path)
	} else {
		log.Printf("save history: %v", err)
	}
}
