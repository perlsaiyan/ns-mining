// Package monitor runs the poll → evaluate → notify → persist loop.
package monitor

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/perlsaiyan/ns-mining/internal/alert"
	"github.com/perlsaiyan/ns-mining/internal/bitaxe"
	"github.com/perlsaiyan/ns-mining/internal/ckpool"
	"github.com/perlsaiyan/ns-mining/internal/config"
	"github.com/perlsaiyan/ns-mining/internal/format"
	"github.com/perlsaiyan/ns-mining/internal/state"
)

// Notifier delivers alerts. Implemented by notify.Slack.
type Notifier interface {
	Send(ctx context.Context, a alert.Alert) error
	SendText(ctx context.Context, text string) error
}

// Monitor holds everything needed to run the loop.
type Monitor struct {
	cfg      *config.Config
	st       *state.State
	engine   *alert.Engine
	notifier Notifier
	bitaxe   map[string]*bitaxe.Client
	ckpool   *ckpool.Client
}

// New wires a monitor. notifier may be nil, in which case alerts are logged only.
func New(cfg *config.Config, st *state.State, engine *alert.Engine, notifier Notifier) *Monitor {
	clients := make(map[string]*bitaxe.Client, len(cfg.Miners))
	for _, m := range cfg.Miners {
		clients[m.Name] = bitaxe.New(m.URL, cfg.DevicePollInterval.D())
	}
	return &Monitor{
		cfg: cfg, st: st, engine: engine, notifier: notifier,
		bitaxe: clients,
		ckpool: ckpool.New("", 15*time.Second),
	}
}

// Run polls until ctx is cancelled. The device is polled every tick; the pool
// is polled on a slower cadence. Device and pool phases run sequentially within
// a tick so a miner's state is never mutated by two goroutines at once.
func (m *Monitor) Run(ctx context.Context) error {
	interval := m.cfg.DevicePollInterval.D()
	log.Printf("monitoring %d miner(s): device every %s, pool every %s",
		len(m.cfg.Miners), interval, m.cfg.PoolPollInterval.D())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Daily heartbeat schedule (validated as HH:MM by config.Load).
	var hbHour, hbMin int
	hbEnabled := m.cfg.HeartbeatTime != ""
	var nextHB time.Time
	if hbEnabled {
		t, _ := time.Parse("15:04", m.cfg.HeartbeatTime)
		hbHour, hbMin = t.Hour(), t.Minute()
		nextHB = nextDaily(time.Now(), hbHour, hbMin)
		log.Printf("daily heartbeat at %s (next %s)", m.cfg.HeartbeatTime, nextHB.Format(time.RFC1123))
	}

	m.tick(ctx, time.Now(), true) // first pass polls both
	nextPool := time.Now().Add(m.cfg.PoolPollInterval.D())
	for {
		select {
		case <-ctx.Done():
			return m.st.Save()
		case now := <-ticker.C:
			doPool := !now.Before(nextPool)
			m.tick(ctx, now, doPool)
			if doPool {
				nextPool = now.Add(m.cfg.PoolPollInterval.D())
			}
			if hbEnabled && !now.Before(nextHB) {
				m.sendHeartbeat(ctx, now)
				nextHB = nextDaily(now, hbHour, hbMin)
			}
		}
	}
}

// Heartbeat sends a single daily-summary message immediately. Useful for a
// manual "status now" and for testing.
func (m *Monitor) Heartbeat(ctx context.Context) { m.sendHeartbeat(ctx, time.Now()) }

// nextDaily returns the next occurrence of hh:mm strictly after now.
func nextDaily(now time.Time, hh, mm int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return t
}

// sendHeartbeat posts a daily digest of every miner's current state. It polls
// fresh so the summary is authoritative rather than reusing cached readings.
func (m *Monitor) sendHeartbeat(ctx context.Context, now time.Time) {
	if m.notifier == nil {
		return
	}
	var b strings.Builder
	b.WriteString(":bar_chart: *ns-mining daily summary*\n")

	for _, mn := range m.cfg.Miners {
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		info, err := m.bitaxe[mn.Name].SystemInfo(dctx)
		cancel()
		if err != nil {
			fmt.Fprintf(&b, "\n*%s* — 🔴 offline (%v)\n", mn.Name, err)
			continue
		}
		fmt.Fprintf(&b, "\n*%s* — 🟢 online · %s · AxeOS %s\n", mn.Name, info.ASICModel, info.AxeOSVersion)
		fmt.Fprintf(&b, "   hashrate %s (1h %s / exp %s) · ASIC %.0f°C · uptime %s\n",
			format.Hashrate(info.HashRate), format.Hashrate(info.HashRate1h),
			format.Hashrate(info.ExpectedHashrate), info.Temp, format.Uptime(info.UptimeSeconds))
		fmt.Fprintf(&b, "   best ever %s · shares %d acc / %d rej\n",
			format.Diff(info.BestDiff), info.SharesAccepted, info.SharesRejected)

		if mn.Pool.Type == "ckpool" && mn.Pool.Address != "" {
			pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
			stats, perr := m.ckpool.User(pctx, mn.Pool.Address)
			pcancel()
			if perr == nil && stats.LastShare > 0 {
				age := now.Sub(time.Unix(stats.LastShare, 0)).Round(time.Second)
				fmt.Fprintf(&b, "   pool: last share %s ago · %d worker(s)\n", age, stats.Workers)
			}
		}
	}

	sctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := m.notifier.SendText(sctx, b.String()); err != nil {
		log.Printf("heartbeat: %v", err)
	}
}

// tick runs the device phase (always) and the pool phase (when doPool), then
// dispatches all alerts in a stable order and persists state once.
func (m *Monitor) tick(ctx context.Context, now time.Time, doPool bool) {
	var alerts []alert.Alert
	alerts = append(alerts, m.fanout(m.cfg.Miners, func(mn config.Miner) []alert.Alert {
		return m.pollDevice(ctx, mn, now)
	})...)

	if doPool {
		poolMiners := make([]config.Miner, 0, len(m.cfg.Miners))
		for _, mn := range m.cfg.Miners {
			if mn.Pool.Type == "ckpool" && mn.Pool.Address != "" {
				poolMiners = append(poolMiners, mn)
			}
		}
		alerts = append(alerts, m.fanout(poolMiners, func(mn config.Miner) []alert.Alert {
			return m.pollPool(ctx, mn, now)
		})...)
	}

	for _, a := range alerts {
		m.dispatch(ctx, a)
	}
	if err := m.st.Save(); err != nil {
		log.Printf("state save: %v", err)
	}
}

// fanout runs fn for every miner concurrently and returns the alerts in miner order.
func (m *Monitor) fanout(miners []config.Miner, fn func(config.Miner) []alert.Alert) []alert.Alert {
	results := make([][]alert.Alert, len(miners))
	var wg sync.WaitGroup
	for idx, mn := range miners {
		wg.Add(1)
		go func(idx int, mn config.Miner) {
			defer wg.Done()
			results[idx] = fn(mn)
		}(idx, mn)
	}
	wg.Wait()

	var out []alert.Alert
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

func (m *Monitor) pollDevice(ctx context.Context, miner config.Miner, now time.Time) []alert.Alert {
	st := m.st.Miner(miner.Name)
	rctx, cancel := context.WithTimeout(ctx, m.cfg.DevicePollInterval.D())
	defer cancel()

	info, err := m.bitaxe[miner.Name].SystemInfo(rctx)
	if err != nil {
		return m.engine.Reachable(miner.Name, st, false, err)
	}
	alerts := m.engine.Reachable(miner.Name, st, true, nil)
	return append(alerts, m.engine.Device(miner.Name, info, st, now)...)
}

func (m *Monitor) pollPool(ctx context.Context, miner config.Miner, now time.Time) []alert.Alert {
	st := m.st.Miner(miner.Name)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	stats, err := m.ckpool.User(rctx, miner.Pool.Address)
	if err != nil {
		log.Printf("pool poll %s: %v", miner.Name, err)
		return nil
	}
	return m.engine.Pool(miner.Name, stats, st, now)
}

func (m *Monitor) dispatch(ctx context.Context, a alert.Alert) {
	log.Printf("[%s] %s: %s", a.Severity, a.Title, a.Text)
	if m.notifier == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := m.notifier.Send(sctx, a); err != nil {
		log.Printf("notify: %v", err)
	}
}
