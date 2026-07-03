// Package format renders mining quantities as human-readable strings.
package format

import (
	"fmt"
	"time"
)

// Hashrate formats a GH/s value (AxeOS reports hashRate in GH/s).
func Hashrate(gh float64) string {
	if gh >= 1000 {
		return fmt.Sprintf("%.2f TH/s", gh/1000)
	}
	return fmt.Sprintf("%.1f GH/s", gh)
}

// Diff formats a difficulty with a metric suffix (K/M/G/T/P/E).
func Diff(d float64) string {
	units := []string{"", "K", "M", "G", "T", "P", "E"}
	idx := 0
	for d >= 1000 && idx < len(units)-1 {
		d /= 1000
		idx++
	}
	return fmt.Sprintf("%.2f%s", d, units[idx])
}

// Uptime formats a duration in seconds as "1d 2h 3m".
func Uptime(secs int64) string {
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
