# ns-mining

A small Go service that monitors one or more [Bitaxe](https://bitaxe.org) miners
and posts status updates to Slack: reboots, work stoppages, thermal events,
record share difficulties, block finds, and pool-side health.

It polls two sources:

- **The Bitaxe directly** (`AxeOS` HTTP API) — the source of truth for device
  health: hashrate, temps, power, fans, uptime, firmware, shares, best diff.
- **solo.ckpool.org** (`/users/<address>`) — the pool's view: when it last saw
  a share, connected workers, and all-time best share.

## Alerts

Each alert is **edge-triggered** — it fires once when a condition begins and
once (as an ℹ️ recovery) when it clears. A standing condition never re-notifies.

| Source | Alert |
|--------|-------|
| Device | Offline / recovered |
| Device | Reboot detected (includes `resetReason`) |
| Device | Firmware version changed |
| Device | 🎉 Block found (`blockFound` flag, or best diff ≥ network diff) |
| Device | New record difficulty (all-time best share) |
| Device | New session best (best-this-uptime, gated to a % of all-time) |
| Device | Hashrate collapse / work stoppage (sustained) + recovery |
| Device | ASIC over-temperature + recovery |
| Device | VR (regulator) over-temperature + recovery |
| Device | Fan failure (0 RPM while hashing) + recovery |
| Device | Running on fallback stratum + recovery |
| Pool   | No shares reaching pool (silence) + recovery |
| Pool   | No workers connected + recovery |
| Pool   | New record difficulty (pool-side cross-check) |

The all-time-best-difficulty record is shared between the device and pool
checks, so a new record notifies exactly once regardless of which sees it first.

## Configuration

Copy `config.example.yaml` to `config.yaml` and edit. The Slack **bot token is
never stored in the file** — it is read from `$SLACK_BOT_TOKEN`.

```yaml
device_poll_interval: 20s
pool_poll_interval: 60s
state_file: /var/lib/ns-mining/state.json
slack:
  channel: C0BEQ56HM0B          # channel ID or #name
miners:
  - name: bitaxe1
    url: http://192.168.88.152
    pool: { type: ckpool, address: bc1q...your-address }
thresholds:
  temp_warn_c: 68
  vr_temp_warn_c: 95
  hashrate_floor_pct: 50        # alert below this % of expectedHashrate
  work_stoppage_min: 5
  pool_silence_min: 15
```

Adding a second Bitaxe is just another list entry under `miners:`.

## Slack bot token

1. Create an app at <https://api.slack.com/apps> → *From scratch*.
2. **OAuth & Permissions → Bot Token Scopes**: add `chat:write` (and optionally
   `chat:write.public`).
3. *Install to Workspace*, copy the `xoxb-…` **Bot User OAuth Token**.
4. Invite the bot to your channel: `/invite @your-app`.

Provide it via the environment: `export SLACK_BOT_TOKEN=xoxb-…`. If unset, the
service logs alerts instead of posting (useful for a dry run).

> The bot is **send-only**. Reading channel messages (for two-way control like a
> `status` command) requires extra scopes and Socket Mode — not yet implemented.

## Build & run

```sh
go build -o ns-mining ./cmd/ns-mining

# One-shot summary of every configured miner (no Slack, good smoke test):
./ns-mining -once -config config.yaml

# Continuous monitoring:
SLACK_BOT_TOKEN=xoxb-… ./ns-mining -config config.yaml
```

## Deploy (systemd)

**System-wide** — use the hardened template in [`deploy/ns-mining.service`](deploy/ns-mining.service):

```sh
sudo install -m0755 ns-mining /usr/local/bin/ns-mining
sudo mkdir -p /etc/ns-mining
sudo cp config.example.yaml /etc/ns-mining/config.yaml   # then edit
echo 'SLACK_BOT_TOKEN=xoxb-…' | sudo tee /etc/ns-mining/env   # chmod 600
sudo cp deploy/ns-mining.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ns-mining
```

The unit uses `DynamicUser=yes` and `StateDirectory=ns-mining`, so systemd owns
`/var/lib/ns-mining` and no dedicated user setup is needed.

**Per-user** (no root) — place a unit in `~/.config/systemd/user/` pointing at
the binary, config, and an `EnvironmentFile` holding the token, then
`systemctl --user enable --now ns-mining`.

## State

`state_file` holds a small JSON document (per-miner uptime, all-time best diff,
firmware, active-alert flags) written atomically each poll. It lets records and
alert state survive restarts, so the service doesn't lose the all-time best or
re-fire standing alerts on boot.
```
