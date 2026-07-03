// Package monitor runs the poll → evaluate → notify → persist loop.
package monitor

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/perlsaiyan/ns-mining/internal/alert"
	"github.com/perlsaiyan/ns-mining/internal/bitaxe"
	"github.com/perlsaiyan/ns-mining/internal/ckpool"
	"github.com/perlsaiyan/ns-mining/internal/config"
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
		}
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
