// Command ns-mining monitors one or more Bitaxe miners and notifies Slack of
// status changes, reboots, work stoppages, thermal events and record shares.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/perlsaiyan/ns-mining/internal/alert"
	"github.com/perlsaiyan/ns-mining/internal/bitaxe"
	"github.com/perlsaiyan/ns-mining/internal/config"
	"github.com/perlsaiyan/ns-mining/internal/format"
	"github.com/perlsaiyan/ns-mining/internal/monitor"
	"github.com/perlsaiyan/ns-mining/internal/notify"
	"github.com/perlsaiyan/ns-mining/internal/state"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("ns-mining: ")

	cfgPath := flag.String("config", "config.yaml", "path to config file")
	once := flag.Bool("once", false, "poll each miner once, print a summary, and exit")
	heartbeat := flag.Bool("heartbeat", false, "post one heartbeat summary to Slack and exit")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if *once {
		if err := runOnce(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *heartbeat {
		if err := runHeartbeat(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

// run starts the continuous monitor until SIGINT/SIGTERM.
func run(cfg *config.Config) error {
	st, err := state.Load(cfg.StateFile)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	var notifier monitor.Notifier
	if cfg.Slack.Token != "" {
		notifier = notify.NewSlack(cfg.Slack.Token, cfg.Slack.Channel)
	} else {
		log.Print("SLACK_BOT_TOKEN not set — alerts will be logged only")
	}

	engine := alert.New(cfg.Thresholds)
	mon := monitor.New(cfg, st, engine, notifier)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Print("started")
	return mon.Run(ctx)
}

// runHeartbeat posts a single summary to Slack and exits.
func runHeartbeat(cfg *config.Config) error {
	if cfg.Slack.Token == "" {
		return fmt.Errorf("SLACK_BOT_TOKEN not set")
	}
	st, err := state.Load(cfg.StateFile)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	notifier := notify.NewSlack(cfg.Slack.Token, cfg.Slack.Channel)
	mon := monitor.New(cfg, st, alert.New(cfg.Thresholds), notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mon.Heartbeat(ctx)
	return nil
}

// runOnce polls every miner a single time and prints a human-readable summary.
func runOnce(cfg *config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, m := range cfg.Miners {
		client := bitaxe.New(m.URL, 10*time.Second)
		info, err := client.SystemInfo(ctx)
		if err != nil {
			fmt.Printf("✗ %-10s %s  UNREACHABLE: %v\n", m.Name, m.URL, err)
			continue
		}
		printSummary(m.Name, info)
	}
	return nil
}

func printSummary(name string, i *bitaxe.SystemInfo) {
	fmt.Printf("● %s  (%s, AxeOS %s)\n", name, i.ASICModel, i.AxeOSVersion)
	fmt.Printf("    hashrate   %s  (1h %s, expected %s, err %.2f%%)\n",
		format.Hashrate(i.HashRate), format.Hashrate(i.HashRate1h), format.Hashrate(i.ExpectedHashrate), i.ErrorPercentage)
	fmt.Printf("    thermal    ASIC %.1f°C  VR %.0f°C  target %.0f°C  fan %.0f%% / %d rpm\n",
		i.Temp, i.VRTemp, i.Temptarget, i.FanSpeed, i.FanRPM)
	fmt.Printf("    power      %.1f W  %.2f V  (max %.0f W)\n", i.Power, i.Voltage/1000, i.MaxPower)
	fmt.Printf("    best diff  session %s  all-time %s\n", format.Diff(i.BestSessionDiff), format.Diff(i.BestDiff))
	fmt.Printf("    network    diff %s  block %d\n", format.Diff(i.NetworkDifficulty), i.BlockHeight)
	fmt.Printf("    shares     %d accepted / %d rejected\n", i.SharesAccepted, i.SharesRejected)
	fmt.Printf("    pool       %s:%d  fallback=%d\n", i.StratumURL, i.StratumPort, i.IsUsingFallbackStrat)
	fmt.Printf("    uptime     %s  reset: %q\n", format.Uptime(i.UptimeSeconds), i.ResetReason)
	if i.BlockFound != 0 {
		fmt.Printf("    🎉 BLOCK FOUND flag is set!\n")
	}
	fmt.Println()
}
