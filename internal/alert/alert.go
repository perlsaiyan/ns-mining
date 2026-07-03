// Package alert evaluates miner telemetry into notification-worthy events.
//
// Rules are edge-triggered: a condition firing produces exactly one alert, and
// its clearing produces exactly one recovery, using the persisted per-miner
// Active map. This keeps a persistent condition (e.g. an overheating device)
// from re-notifying every poll.
package alert

import (
	"fmt"
	"time"

	"github.com/perlsaiyan/ns-mining/internal/bitaxe"
	"github.com/perlsaiyan/ns-mining/internal/ckpool"
	"github.com/perlsaiyan/ns-mining/internal/config"
	"github.com/perlsaiyan/ns-mining/internal/format"
	"github.com/perlsaiyan/ns-mining/internal/state"
)

// Severity controls the emoji/colour a notifier renders.
type Severity int

const (
	Info Severity = iota
	Warning
	Critical
	Celebrate
)

func (s Severity) String() string {
	switch s {
	case Warning:
		return "warning"
	case Critical:
		return "critical"
	case Celebrate:
		return "celebrate"
	default:
		return "info"
	}
}

// Alert is one notification-worthy event.
type Alert struct {
	Miner    string
	Type     string
	Severity Severity
	Title    string
	Text     string
	Fields   []Field // optional metric grid rendered by the notifier
}

// Field is one labelled metric shown in an alert's grid.
type Field struct {
	Label string
	Value string
}

// Engine turns telemetry deltas into alerts using configured thresholds.
type Engine struct {
	th config.Thresholds
}

// New returns an engine bound to the given thresholds.
func New(th config.Thresholds) *Engine { return &Engine{th: th} }

// Reachable manages the offline/online edge for a miner and returns any alert
// produced by the transition (offline when it goes unreachable, recovered when
// it comes back).
func (e *Engine) Reachable(miner string, st *state.MinerState, up bool, cause error) []Alert {
	rising, falling := st.Edge("offline", !up)
	switch {
	case rising:
		return []Alert{{
			Miner: miner, Type: "offline", Severity: Critical,
			Title: "Miner offline",
			Text:  fmt.Sprintf("%s is unreachable: %v", miner, cause),
		}}
	case falling:
		return []Alert{{
			Miner: miner, Type: "offline", Severity: Info,
			Title: "Miner back online",
			Text:  fmt.Sprintf("%s is reachable again", miner),
		}}
	}
	return nil
}

// Device evaluates a fresh AxeOS reading against stored state, appending alerts
// and mutating st (records, last-seen counters, active conditions). now is
// passed in for testability.
func (e *Engine) Device(miner string, i *bitaxe.SystemInfo, st *state.MinerState, now time.Time) []Alert {
	var out []Alert
	add := func(a Alert) { a.Miner = miner; out = append(out, a) }

	// --- Reboot: uptime went backwards ---
	if st.LastUptime > 0 && i.UptimeSeconds < st.LastUptime {
		add(Alert{
			Type: "reboot", Severity: Warning, Title: "Miner rebooted",
			Text: fmt.Sprintf("%s restarted — it was up %s before this.", miner, format.Uptime(st.LastUptime)),
			Fields: []Field{
				{"Reset reason", i.ResetReason},
				{"Uptime now", format.Uptime(i.UptimeSeconds)},
			},
		})
	}
	st.LastUptime = i.UptimeSeconds

	// --- Firmware version change ---
	if st.LastFirmware != "" && st.LastFirmware != i.AxeOSVersion {
		add(Alert{
			Type: "firmware", Severity: Info, Title: "Firmware changed",
			Text: fmt.Sprintf("%s firmware %s → %s", miner, st.LastFirmware, i.AxeOSVersion),
		})
	}
	st.LastFirmware = i.AxeOSVersion

	// --- Block found (the big one) ---
	blockCond := i.BlockFound != 0 || (i.NetworkDifficulty > 0 && i.BestDiff >= i.NetworkDifficulty)
	if rising, _ := st.Edge("block", blockCond); rising {
		add(Alert{
			Type: "block", Severity: Celebrate, Title: "BLOCK FOUND",
			Text: fmt.Sprintf("*%s may have solved block %d!* :tada:", miner, i.BlockHeight),
			Fields: []Field{
				{"Best diff", format.Diff(i.BestDiff)},
				{"Network diff", format.Diff(i.NetworkDifficulty)},
			},
		})
	}

	// --- New all-time record difficulty ---
	if st.AllTimeBestDiff > 0 && i.BestDiff > st.AllTimeBestDiff {
		add(Alert{
			Type: "record", Severity: Celebrate, Title: "New record difficulty",
			Text: fmt.Sprintf("%s just beat its all-time best share.", miner),
			Fields: []Field{
				{"New best", format.Diff(i.BestDiff)},
				{"Previous", format.Diff(st.AllTimeBestDiff)},
			},
		})
	}
	if i.BestDiff > st.AllTimeBestDiff {
		st.AllTimeBestDiff = i.BestDiff
	}

	// --- New session (best-this-uptime) record ---
	// Gated to a fraction of all-time so the post-reboot climb stays quiet, and
	// skipped when it's also an all-time record (that alert already fired).
	if e.th.SessionRecordPct > 0 && st.AllTimeBestDiff > 0 {
		switch {
		case i.BestSessionDiff < st.BestSessionDiff:
			st.BestSessionDiff = i.BestSessionDiff // session reset (reboot)
		case i.BestSessionDiff > st.BestSessionDiff:
			floor := st.AllTimeBestDiff * e.th.SessionRecordPct / 100
			if i.BestSessionDiff >= floor && i.BestSessionDiff < st.AllTimeBestDiff {
				add(Alert{
					Type: "session_record", Severity: Celebrate, Title: "New session best",
					Text: fmt.Sprintf("%s set a new best-this-uptime share.", miner),
					Fields: []Field{
						{"Session best", format.Diff(i.BestSessionDiff)},
						{"All-time", format.Diff(st.AllTimeBestDiff)},
						{"vs all-time", fmt.Sprintf("%.0f%%", 100*i.BestSessionDiff/st.AllTimeBestDiff)},
					},
				})
			}
			st.BestSessionDiff = i.BestSessionDiff
		}
	}

	// --- Low hashrate / work stoppage (sustained) ---
	floor := i.ExpectedHashrate * e.th.HashrateFloorPct / 100
	low := i.ExpectedHashrate > 0 && i.HashRate1m < floor
	if low {
		if st.LowHashSince.IsZero() {
			st.LowHashSince = now
		}
		sustained := now.Sub(st.LowHashSince) >= time.Duration(e.th.WorkStoppageMin)*time.Minute
		if rising, _ := st.Edge("lowhash", sustained); rising {
			add(Alert{
				Type: "lowhash", Severity: Critical, Title: "Hashrate collapsed",
				Text: fmt.Sprintf("%s has run below %.0f%% of expected for %d+ min.",
					miner, e.th.HashrateFloorPct, e.th.WorkStoppageMin),
				Fields: []Field{
					{"Current (1m)", format.Hashrate(i.HashRate1m)},
					{"Expected", format.Hashrate(i.ExpectedHashrate)},
					{"Level", format.Gauge(i.HashRate1m, 0, i.ExpectedHashrate, 10)},
					{"ASIC temp", fmt.Sprintf("%.0f°C", i.Temp)},
				},
			})
		}
	} else {
		st.LowHashSince = time.Time{}
		if _, falling := st.Edge("lowhash", false); falling {
			add(Alert{
				Type: "lowhash", Severity: Info, Title: "Hashrate recovered",
				Text: fmt.Sprintf("%s hashrate back to %s", miner, format.Hashrate(i.HashRate1m)),
			})
		}
	}

	// --- ASIC over-temperature ---
	e.threshold(&out, miner, st, "temp", i.Temp >= e.th.TempWarnC || i.OverheatMode != 0,
		fmt.Sprintf("%s crossed its ASIC thermal limit.", miner),
		fmt.Sprintf("ASIC temp back to %.1f°C.", i.Temp), Critical,
		Field{"Temp", fmt.Sprintf("%.1f°C  %s", i.Temp, format.Gauge(i.Temp, e.th.TempWarnC-20, e.th.TempWarnC+5, 9))},
		Field{"Limit", fmt.Sprintf("%.0f°C", e.th.TempWarnC)},
		Field{"Fan", fmt.Sprintf("%.0f%% · %d rpm", i.FanSpeed, i.FanRPM)},
		Field{"Hashrate", format.Hashrate(i.HashRate)})

	// --- VR (regulator) over-temperature ---
	e.threshold(&out, miner, st, "vrtemp", i.VRTemp >= e.th.VRTempWarnC,
		fmt.Sprintf("%s voltage regulator is running hot.", miner),
		fmt.Sprintf("VR temp back to %.0f°C.", i.VRTemp), Critical,
		Field{"VR temp", fmt.Sprintf("%.0f°C  %s", i.VRTemp, format.Gauge(i.VRTemp, e.th.VRTempWarnC-20, e.th.VRTempWarnC+5, 9))},
		Field{"Limit", fmt.Sprintf("%.0f°C", e.th.VRTempWarnC)},
		Field{"ASIC temp", fmt.Sprintf("%.0f°C", i.Temp)})

	// --- Fan failure: not spinning while hashing ---
	e.threshold(&out, miner, st, "fan", i.FanRPM == 0 && i.HashRate > 0,
		fmt.Sprintf("%s: primary fan reads 0 RPM while hashing — thermal risk.", miner),
		fmt.Sprintf("fan spinning again (%d RPM).", i.FanRPM), Critical,
		Field{"Fan RPM", "0"},
		Field{"ASIC temp", fmt.Sprintf("%.0f°C", i.Temp)},
		Field{"Hashrate", format.Hashrate(i.HashRate)})

	// --- Running on fallback stratum ---
	e.threshold(&out, miner, st, "fallback", i.IsUsingFallbackStrat != 0,
		fmt.Sprintf("%s switched to its fallback stratum — primary pool unreachable?", miner),
		"back on primary stratum.", Warning,
		Field{"Primary", fmt.Sprintf("%s:%d", i.StratumURL, i.StratumPort)})

	st.LastAccepted = i.SharesAccepted
	st.LastRejected = i.SharesRejected
	return out
}

// Pool evaluates a solo.ckpool.org reading against stored state. It shares
// st.AllTimeBestDiff with Device so a record is reported once regardless of
// which source observes it first.
func (e *Engine) Pool(miner string, s *ckpool.Stats, st *state.MinerState, now time.Time) []Alert {
	var out []Alert
	add := func(a Alert) { a.Miner = miner; out = append(out, a) }

	// --- Share silence: pool hasn't accepted a share in too long ---
	if s.LastShare > 0 {
		silent := now.Sub(time.Unix(s.LastShare, 0)) >= time.Duration(e.th.PoolSilenceMin)*time.Minute
		rising, falling := st.Edge("pool_silence", silent)
		if rising {
			add(Alert{Type: "pool_silence", Severity: Critical, Title: "No shares reaching pool",
				Text: fmt.Sprintf("%s: solo.ckpool.org hasn't seen a share recently.", miner),
				Fields: []Field{
					{"Last share", now.Sub(time.Unix(s.LastShare, 0)).Round(time.Minute).String() + " ago"},
					{"Threshold", fmt.Sprintf("%dm", e.th.PoolSilenceMin)},
					{"Workers", fmt.Sprintf("%d", s.Workers)},
				}})
		} else if falling {
			add(Alert{Type: "pool_silence", Severity: Info, Title: "Pool receiving shares again",
				Text: fmt.Sprintf("%s: shares landing at the pool again", miner)})
		}
	}

	// --- Worker disappeared from the pool ---
	rising, falling := st.Edge("pool_noworkers", s.Workers == 0)
	if rising {
		add(Alert{Type: "pool_noworkers", Severity: Critical, Title: "No workers at pool",
			Text: fmt.Sprintf("%s: pool reports 0 connected workers", miner)})
	} else if falling {
		add(Alert{Type: "pool_noworkers", Severity: Info, Title: "Worker reconnected",
			Text: fmt.Sprintf("%s: pool reports %d worker(s)", miner, s.Workers)})
	}

	// --- All-time record (pool-side cross-check, shares state with Device) ---
	if st.AllTimeBestDiff > 0 && s.BestEver > st.AllTimeBestDiff {
		add(Alert{Type: "record", Severity: Celebrate, Title: "New record difficulty",
			Text: fmt.Sprintf("%s set a new all-time best share (pool-confirmed).", miner),
			Fields: []Field{
				{"New best", format.Diff(s.BestEver)},
				{"Previous", format.Diff(st.AllTimeBestDiff)},
			}})
	}
	if s.BestEver > st.AllTimeBestDiff {
		st.AllTimeBestDiff = s.BestEver
	}
	return out
}

// threshold fires a warning/critical alert on rising and an info recovery on
// falling, formatting titles consistently. Fields decorate the rising alert.
func (e *Engine) threshold(out *[]Alert, miner string, st *state.MinerState, key string, cond bool, onText, offText string, sev Severity, fields ...Field) {
	rising, falling := st.Edge(key, cond)
	if rising {
		*out = append(*out, Alert{Miner: miner, Type: key, Severity: sev,
			Title: title(key, true), Text: onText, Fields: fields})
	} else if falling {
		*out = append(*out, Alert{Miner: miner, Type: key, Severity: Info,
			Title: title(key, false), Text: miner + ": " + offText})
	}
}

func title(key string, on bool) string {
	names := map[string]string{
		"temp": "ASIC over-temp", "vrtemp": "VR over-temp",
		"fan": "Fan failure", "fallback": "Fallback stratum",
	}
	n := names[key]
	if !on {
		return n + " cleared"
	}
	return n
}
