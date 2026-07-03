package alert

import (
	"errors"
	"testing"
	"time"

	"github.com/perlsaiyan/ns-mining/internal/bitaxe"
	"github.com/perlsaiyan/ns-mining/internal/ckpool"
	"github.com/perlsaiyan/ns-mining/internal/config"
	"github.com/perlsaiyan/ns-mining/internal/state"
)

func testEngine() *Engine {
	return New(config.Thresholds{
		TempWarnC: 68, VRTempWarnC: 95, HashrateFloorPct: 50,
		WorkStoppageMin: 5, PoolSilenceMin: 15,
	})
}

func newState() *state.MinerState { return &state.MinerState{Active: map[string]bool{}} }

// find returns the first alert of the given type, or nil.
func find(alerts []Alert, typ string) *Alert {
	for i := range alerts {
		if alerts[i].Type == typ {
			return &alerts[i]
		}
	}
	return nil
}

func TestReachableEdge(t *testing.T) {
	e, st := testEngine(), newState()

	// First reading up: no alert (was not offline).
	if a := e.Reachable("m", st, true, nil); len(a) != 0 {
		t.Fatalf("initial up should not alert, got %v", a)
	}
	// Goes down: one offline alert.
	if a := e.Reachable("m", st, false, errors.New("timeout")); find(a, "offline") == nil {
		t.Fatalf("expected offline alert, got %v", a)
	}
	// Still down: no repeat.
	if a := e.Reachable("m", st, false, errors.New("timeout")); len(a) != 0 {
		t.Fatalf("offline should not re-fire, got %v", a)
	}
	// Recovers: one recovery alert.
	if a := e.Reachable("m", st, true, nil); find(a, "offline") == nil {
		t.Fatalf("expected recovery alert, got %v", a)
	}
}

func TestRebootAndRecord(t *testing.T) {
	e, st := testEngine(), newState()
	now := time.Unix(1_700_000_000, 0)

	// Establish baseline: uptime 1000, bestDiff 1e9. No alerts on first sight.
	base := &bitaxe.SystemInfo{UptimeSeconds: 1000, BestDiff: 1e9, AxeOSVersion: "v2.13.1", ExpectedHashrate: 1275, HashRate1m: 1275, HashRate: 1275, FanRPM: 8000}
	if a := e.Device("m", base, st, now); len(a) != 0 {
		t.Fatalf("first sight should not alert, got %v", a)
	}

	// Uptime drops (reboot) and new record diff.
	next := &bitaxe.SystemInfo{UptimeSeconds: 30, BestDiff: 2e9, AxeOSVersion: "v2.13.1", ResetReason: "power-on", ExpectedHashrate: 1275, HashRate1m: 1275, HashRate: 1275, FanRPM: 8000}
	a := e.Device("m", next, st, now)
	if find(a, "reboot") == nil {
		t.Errorf("expected reboot alert, got %v", a)
	}
	if find(a, "record") == nil {
		t.Errorf("expected record alert, got %v", a)
	}
	if st.AllTimeBestDiff != 2e9 {
		t.Errorf("AllTimeBestDiff = %v, want 2e9", st.AllTimeBestDiff)
	}
}

func TestLowHashSustained(t *testing.T) {
	e, st := testEngine(), newState()
	start := time.Unix(1_700_000_000, 0)
	low := &bitaxe.SystemInfo{ExpectedHashrate: 1000, HashRate1m: 100, HashRate: 100, UptimeSeconds: 5000, AxeOSVersion: "v"}

	// Below floor but not yet sustained: no alert.
	if a := e.Device("m", low, st, start); find(a, "lowhash") != nil {
		t.Fatalf("should not alert before sustained window, got %v", a)
	}
	// After the window: fires once.
	a := e.Device("m", low, st, start.Add(6*time.Minute))
	if find(a, "lowhash") == nil {
		t.Fatalf("expected lowhash alert after window, got %v", a)
	}
	// Recovery when hashrate returns.
	ok := &bitaxe.SystemInfo{ExpectedHashrate: 1000, HashRate1m: 1000, HashRate: 1000, UptimeSeconds: 5100, AxeOSVersion: "v"}
	if r := e.Device("m", ok, st, start.Add(7*time.Minute)); find(r, "lowhash") == nil {
		t.Fatalf("expected lowhash recovery, got %v", r)
	}
}

func TestPoolSilenceAndRecord(t *testing.T) {
	e, st := testEngine(), newState()
	st.AllTimeBestDiff = 1e9
	now := time.Unix(1_700_000_000, 0)

	// Last share 20 min ago (> 15m threshold) and a new pool-side record.
	s := &ckpool.Stats{LastShare: now.Add(-20 * time.Minute).Unix(), Workers: 1, BestEver: 2e9}
	a := e.Pool("m", s, st, now)
	if find(a, "pool_silence") == nil {
		t.Errorf("expected pool_silence alert, got %v", a)
	}
	if find(a, "record") == nil {
		t.Errorf("expected pool record alert, got %v", a)
	}

	// Shares resume, workers present: silence recovers, no new record.
	s2 := &ckpool.Stats{LastShare: now.Unix(), Workers: 1, BestEver: 2e9}
	r := e.Pool("m", s2, st, now)
	if rec := find(r, "pool_silence"); rec == nil || rec.Severity != Info {
		t.Errorf("expected pool_silence recovery (info), got %v", r)
	}
	if find(r, "record") != nil {
		t.Errorf("record should not re-fire, got %v", r)
	}
}
