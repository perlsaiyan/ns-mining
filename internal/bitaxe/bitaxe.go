// Package bitaxe is a client for the AxeOS HTTP API exposed by Bitaxe miners.
package bitaxe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SystemInfo is the response from GET /api/system/info. Only the fields the
// monitor cares about are modelled; AxeOS returns a superset.
type SystemInfo struct {
	// Power / thermal
	Power        float64 `json:"power"`
	Voltage      float64 `json:"voltage"` // mV
	Current      float64 `json:"current"` // mA
	Temp         float64 `json:"temp"`    // ASIC °C
	Temp2        float64 `json:"temp2"`
	VRTemp       float64 `json:"vrTemp"` // voltage regulator °C
	MaxPower     float64 `json:"maxPower"`
	OverheatMode int     `json:"overheat_mode"`
	Temptarget   float64 `json:"temptarget"`
	FanSpeed     float64 `json:"fanspeed"` // percent, may be fractional
	FanRPM       int     `json:"fanrpm"`
	Fan2RPM      int     `json:"fan2rpm"`
	AutoFanSpeed int     `json:"autofanspeed"`

	// Hashing
	HashRate         float64 `json:"hashRate"`
	HashRate1m       float64 `json:"hashRate_1m"`
	HashRate10m      float64 `json:"hashRate_10m"`
	HashRate1h       float64 `json:"hashRate_1h"`
	ExpectedHashrate float64 `json:"expectedHashrate"`
	ErrorPercentage  float64 `json:"errorPercentage"`
	Frequency        float64 `json:"frequency"`

	// Difficulty / blocks
	BestDiff          float64 `json:"bestDiff"`
	BestSessionDiff   float64 `json:"bestSessionDiff"`
	PoolDifficulty    float64 `json:"poolDifficulty"`
	NetworkDifficulty float64 `json:"networkDifficulty"`
	BlockFound        int     `json:"blockFound"`
	BlockHeight       int64   `json:"blockHeight"`

	// Shares
	SharesAccepted        int64          `json:"sharesAccepted"`
	SharesRejected        int64          `json:"sharesRejected"`
	SharesRejectedReasons []RejectReason `json:"sharesRejectedReasons"`

	// Pool / stratum
	StratumURL           string `json:"stratumURL"`
	StratumPort          int    `json:"stratumPort"`
	StratumUser          string `json:"stratumUser"`
	IsUsingFallbackStrat int    `json:"isUsingFallbackStratum"`
	PoolConnectionInfo   string `json:"poolConnectionInfo"`

	// Identity / lifecycle
	Hostname      string `json:"hostname"`
	ASICModel     string `json:"ASICModel"`
	Version       string `json:"version"`
	AxeOSVersion  string `json:"axeOSVersion"`
	BoardVersion  string `json:"boardVersion"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	ResetReason   string `json:"resetReason"`
	WifiRSSI      int    `json:"wifiRSSI"`
}

// RejectReason is one entry in sharesRejectedReasons.
type RejectReason struct {
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

// Client talks to a single Bitaxe.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for a Bitaxe reachable at baseURL (e.g. http://192.168.88.152).
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

// SystemInfo fetches GET /api/system/info.
func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/system/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitaxe: unexpected status %d", resp.StatusCode)
	}
	var info SystemInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("bitaxe: decode: %w", err)
	}
	return &info, nil
}
