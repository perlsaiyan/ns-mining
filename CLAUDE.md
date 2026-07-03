# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`ns-mining` is a single-binary Go service that monitors one or more Bitaxe miners
and posts status changes to Slack. It polls two independent sources and reconciles
them into edge-triggered alerts. Module path: `github.com/perlsaiyan/ns-mining`
(Go 1.25, only dependency is `gopkg.in/yaml.v3`).

## Commands

```sh
go build -o ns-mining ./cmd/ns-mining      # build
go test ./...                              # all tests
go test ./internal/alert -run TestName     # single test (most logic lives in internal/alert)
go vet ./...                               # vet

./ns-mining -once   -config config.yaml    # poll each miner once, print summary, exit (no Slack — smoke test)
./ns-mining -heartbeat -config config.yaml # post one daily-summary to Slack and exit (needs token)
SLACK_BOT_TOKEN=xoxb-… ./ns-mining -config config.yaml   # continuous monitor
```

Without `SLACK_BOT_TOKEN`, the service runs and logs alerts to stdout instead of
posting — a valid dry-run mode. `config.yaml`, `.env.local`, and the compiled
binary are git-ignored; `config.example.yaml` is the committed template.

## Architecture

The data flow is **poll → evaluate → notify → persist**, orchestrated by
`internal/monitor`. Understanding it requires reading these pieces together:

- `internal/monitor` — the loop. On each device tick every miner is polled
  concurrently (`fanout`); the pool is polled on a slower cadence (`pool_poll_interval`).
  Device and pool phases run **sequentially within a tick** so two goroutines never
  mutate one miner's state at once. All alerts from a tick are dispatched in a stable
  order, then state is saved once. Also owns the daily heartbeat scheduler.
- `internal/alert` — the brain. `Engine.StepDevice`, `Engine.StepPool`, and
  `Engine.StepReachable` are state-machine steps: each takes a telemetry reading +
  stored state, **mutates the passed `*MinerState`** (recording new bests, timestamps,
  active flags), and returns the `[]Alert` that transition produced. The `Step` prefix
  signals the mutation. This is where all the monitoring rules live and where most
  tests belong.
- `internal/state` — durable per-miner state, saved atomically (temp file + rename)
  every tick. Lets records and active-alert flags survive restarts.
- `internal/bitaxe` — client for the AxeOS `GET /api/system/info` endpoint (device
  truth: hashrate, temps, shares, best diff, uptime).
- `internal/ckpool` — client for `solo.ckpool.org/users/<address>` (pool truth: last
  share time, worker count, all-time best).
- `internal/notify` — Slack Block Kit renderer. `monitor.Notifier` is the interface;
  a nil notifier means log-only.
- `internal/config`, `internal/format` — YAML load/validate/defaults, and human
  formatting of hashrate/diff/uptime/gauges.

### Two invariants that drive the design

1. **Alerts are edge-triggered.** A condition fires exactly one alert when it begins
   and one Info "recovery" when it clears — never on every poll. This is implemented
   by `MinerState.Edge(key, cond)` returning `(rising, falling)` against the persisted
   `Active` map. Any new standing-condition alert should go through `Edge` (or the
   `Engine.threshold` helper), not a bare `if cond`.

2. **The all-time best difficulty is shared between the device and pool checks.**
   Both `Engine.Device` and `Engine.Pool` read and write `st.AllTimeBestDiff`, so a
   new record notifies exactly once regardless of which source observes it first.
   Preserve this when touching record logic.

`now time.Time` is threaded into the engine methods rather than read from the clock,
so time-dependent rules (work-stoppage duration, pool silence) are testable.

## Adding things

- **Another miner**: just another entry under `miners:` in config — no code change.
- **A new alert**: add a rule in `internal/alert` (gate it through `Edge`/`threshold`),
  add its emoji to `typeEmoji` in `internal/notify/slack.go`, and persist any new
  cross-tick state in `internal/state.MinerState`.
- **A new threshold/tunable**: add the field to `config.Thresholds`, give it a default
  in `applyDefaults`, and document it in `config.example.yaml`.

## Deploy

Runs under systemd. `deploy/ns-mining.service` is the hardened **system-wide**
template (`DynamicUser=yes`, `StateDirectory=ns-mining`, reads the token from an
`EnvironmentFile`). In practice this repo is deployed as a **`systemctl --user`**
service; see README for both paths. The device is at `http://192.168.88.152`.

## Deferred / out of scope

Two-way Slack control (a `status`/`mute`/`restart` command) was designed but
intentionally not built — it needs extra scopes and Socket Mode. The bot is
**send-only**; don't assume inbound message handling exists.
