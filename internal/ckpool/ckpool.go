// Package ckpool is a client for the solo.ckpool.org per-user JSON stats
// endpoint (https://solo.ckpool.org/users/<address>).
package ckpool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultBaseURL is the public solo.ckpool.org stats host.
const DefaultBaseURL = "https://solo.ckpool.org"

// Stats is the pool's view of a user (payout address).
type Stats struct {
	Hashrate1m  string   `json:"hashrate1m"`
	Hashrate5m  string   `json:"hashrate5m"`
	Hashrate1hr string   `json:"hashrate1hr"`
	Hashrate1d  string   `json:"hashrate1d"`
	Hashrate7d  string   `json:"hashrate7d"`
	LastShare   int64    `json:"lastshare"`  // unix seconds of most recent accepted share
	Workers     int      `json:"workers"`    // connected workers
	Shares      int64    `json:"shares"`     // cumulative share count
	BestShare   float64  `json:"bestshare"`  // best share this session
	BestEver    float64  `json:"bestever"`   // best share all-time (pool-side)
	Authorised  int64    `json:"authorised"` // unix seconds first authorised
	Worker      []Worker `json:"worker"`
}

// Worker is one named worker under the address.
type Worker struct {
	Name      string  `json:"workername"`
	LastShare int64   `json:"lastshare"`
	Shares    int64   `json:"shares"`
	BestShare float64 `json:"bestshare"`
	BestEver  float64 `json:"bestever"`
}

// Client fetches stats for a single address.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client. Pass "" for baseURL to use DefaultBaseURL.
func New(baseURL string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

// User fetches GET /users/<address>.
func (c *Client) User(ctx context.Context, address string) (*Stats, error) {
	url := c.baseURL + "/users/" + address
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ckpool: unexpected status %d", resp.StatusCode)
	}
	var s Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("ckpool: decode: %w", err)
	}
	return &s, nil
}
