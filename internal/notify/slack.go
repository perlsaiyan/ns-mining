// Package notify delivers alerts to Slack via chat.postMessage using a bot token.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/perlsaiyan/ns-mining/internal/alert"
)

// Slack posts messages to a channel with a bot token (xoxb-...). Name and
// avatar are taken from the Slack app's own configuration.
type Slack struct {
	token   string
	channel string
	http    *http.Client
}

// NewSlack returns a notifier. token comes from $SLACK_BOT_TOKEN.
func NewSlack(token, channel string) *Slack {
	return &Slack{token: token, channel: channel, http: &http.Client{Timeout: 10 * time.Second}}
}

func severityColor(s alert.Severity) string {
	switch s {
	case alert.Warning:
		return "#E8A317"
	case alert.Critical:
		return "#D00000"
	case alert.Celebrate:
		return "#2EB67D"
	default:
		return "#439FE0"
	}
}

// typeEmoji maps an alert type to a header glyph. Recoveries (Info severity)
// override to a checkmark below.
var typeEmoji = map[string]string{
	"offline":        "🔌",
	"reboot":         "🔄",
	"firmware":       "🧬",
	"block":          "🎉🧱",
	"record":         "💎",
	"session_record": "🔥",
	"lowhash":        "📉",
	"temp":           "🌡️",
	"vrtemp":         "🌡️",
	"fan":            "🌀",
	"fallback":       "🔀",
	"pool_silence":   "📡",
	"pool_noworkers": "👷",
}

func emojiFor(a alert.Alert) string {
	// A cleared/recovered condition reads as an all-clear.
	if a.Severity == alert.Info && a.Type != "firmware" {
		return "✅"
	}
	if e, ok := typeEmoji[a.Type]; ok {
		return e
	}
	return "ℹ️"
}

// Send posts a single alert as a Block Kit message: an emoji header, the body
// text, an optional 2-column metric grid, and a context footer, all inside a
// severity-coloured attachment (the vertical colour bar).
func (s *Slack) Send(ctx context.Context, a alert.Alert) error {
	emoji := emojiFor(a)

	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{
			"type": "plain_text", "emoji": true,
			"text": fmt.Sprintf("%s  %s", emoji, a.Title)}},
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": a.Text}},
	}

	if len(a.Fields) > 0 {
		fields := make([]map[string]any, 0, len(a.Fields))
		for _, f := range a.Fields {
			fields = append(fields, map[string]any{
				"type": "mrkdwn", "text": fmt.Sprintf("*%s*\n%s", f.Label, f.Value)})
		}
		blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
	}

	blocks = append(blocks, map[string]any{"type": "context", "elements": []map[string]any{
		{"type": "mrkdwn", "text": fmt.Sprintf("miner `%s`  ·  %s  ·  %s",
			a.Miner, a.Severity, time.Now().Format("Mon 3:04pm"))}}})

	payload := map[string]any{
		"channel": s.channel,
		"text":    fmt.Sprintf("%s %s — %s", emoji, a.Title, a.Text), // notification fallback
		"attachments": []map[string]any{{
			"color":  severityColor(a.Severity),
			"blocks": blocks,
		}},
	}
	return s.post(ctx, payload)
}

// SendText posts a plain informational line (used for startup/heartbeat).
func (s *Slack) SendText(ctx context.Context, text string) error {
	return s.post(ctx, map[string]any{"channel": s.channel, "text": text})
}

func (s *Slack) post(ctx context.Context, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fmt.Errorf("slack: decode response: %w", err)
	}
	if !res.OK {
		return fmt.Errorf("slack: api error: %s", res.Error)
	}
	return nil
}
