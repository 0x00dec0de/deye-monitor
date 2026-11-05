package main

import (
	"encoding/json"
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
	Kind    string    `json:"kind"` // grid_lost | grid_restored | state | error | start
	Message string    `json:"message"`
}

type Store struct {
	mu      sync.RWMutex
	latest  *Status
	lastErr string
	lastOK  time.Time
	samples []Sample
	events  []Event
	maxAge  time.Duration
	maxEv   int
	path    string
	dirty   bool
	onEvent func(Event)
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
				s.samples, s.events = p.Samples, p.Events
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
	s.latest, s.lastErr, s.lastOK, s.dirty = st, "", st.Time, true
	s.samples = append(s.samples, Sample{
		T: st.Time.Unix(), SOC: st.BatterySOC, Load: st.LoadPowerW,
		Battery: st.BatteryPowerW, Grid: st.GridPowerW, PV: st.PVPowerW, OnGrid: st.GridConnected,
	})
	s.trimLocked()

	if prev == nil {
		s.addEventLocked(st.Time, "start", "monitor started, source: "+st.Source)
		return
	}
	if prev.GridConnected && !st.GridConnected {
		s.addEventLocked(st.Time, "grid_lost", "grid lost, running on "+st.Source)
	} else if !prev.GridConnected && st.GridConnected {
		s.addEventLocked(st.Time, "grid_restored", "grid restored")
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
	if s.lastErr == "" {
		s.addEventLocked(time.Now(), "error", "poll failed: "+err.Error())
		s.dirty = true
	}
	s.lastErr = err.Error()
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

type Snapshot struct {
	Status *Status   `json:"status"`
	Error  string    `json:"error,omitempty"`
	LastOK time.Time `json:"last_ok"`
	Stale  bool      `json:"stale"`
	Events []Event   `json:"events"`
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
	return Snapshot{Status: s.latest, Error: s.lastErr, LastOK: s.lastOK,
		Stale: time.Since(s.lastOK) > 2*time.Minute, Events: ev}
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
