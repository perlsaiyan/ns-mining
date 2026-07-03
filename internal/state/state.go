// Package state persists per-miner monitoring state to disk so that records,
// reboot detection and active-alert tracking survive service restarts.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MinerState is the durable state for a single miner.
type MinerState struct {
	LastUptime      int64           `json:"last_uptime"`
	AllTimeBestDiff float64         `json:"all_time_best_diff"`
	BestSessionDiff float64         `json:"best_session_diff"` // peak session diff seen; resets when the miner reboots
	LastFirmware    string          `json:"last_firmware"`
	LastAccepted    int64           `json:"last_accepted"`
	LastRejected    int64           `json:"last_rejected"`
	LowHashSince    time.Time       `json:"low_hash_since,omitempty"`
	Active          map[string]bool `json:"active"` // active alert conditions, for edge-triggering
}

func (m *MinerState) active(key string) bool {
	if m.Active == nil {
		return false
	}
	return m.Active[key]
}

func (m *MinerState) setActive(key string, on bool) {
	if m.Active == nil {
		m.Active = map[string]bool{}
	}
	if on {
		m.Active[key] = true
	} else {
		delete(m.Active, key)
	}
}

// Edge reports whether a condition transitioned, updating stored state.
// It returns rising=true on off→on, falling=true on on→off, and neither when
// the condition is unchanged. Use it to fire an alert on rising and a recovery
// on falling exactly once per transition.
func (m *MinerState) Edge(key string, now bool) (rising, falling bool) {
	was := m.active(key)
	m.setActive(key, now)
	return now && !was, !now && was
}

// State is the whole persisted document.
type State struct {
	mu     sync.Mutex
	path   string
	Miners map[string]*MinerState `json:"miners"`
}

// Load reads state from path, returning an empty state if the file is absent.
func Load(path string) (*State, error) {
	s := &State{path: path, Miners: map[string]*MinerState{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", path, err)
	}
	if s.Miners == nil {
		s.Miners = map[string]*MinerState{}
	}
	return s, nil
}

// Miner returns the state for name, creating it if needed.
func (s *State) Miner(name string) *MinerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.Miners[name]
	if !ok {
		m = &MinerState{Active: map[string]bool{}}
		s.Miners[name] = m
	}
	return m
}

// Save atomically writes the state to disk.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
