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

func severityDecor(s alert.Severity) (emoji, color string) {
	switch s {
	case alert.Warning:
		return "⚠️", "#E8A317"
	case alert.Critical:
		return "🚨", "#D00000"
	case alert.Celebrate:
		return "🎉", "#2EB67D"
	default:
		return "ℹ️", "#439FE0"
	}
}

// Send posts a single alert as a coloured attachment.
func (s *Slack) Send(ctx context.Context, a alert.Alert) error {
	emoji, color := severityDecor(a.Severity)
	payload := map[string]any{
		"channel": s.channel,
		"text":    fmt.Sprintf("%s %s — %s", emoji, a.Title, a.Text),
		"attachments": []map[string]any{{
			"color": color,
			"blocks": []map[string]any{
				{"type": "section", "text": map[string]any{
					"type": "mrkdwn", "text": fmt.Sprintf("%s *%s*\n%s", emoji, a.Title, a.Text)}},
				{"type": "context", "elements": []map[string]any{
					{"type": "mrkdwn", "text": fmt.Sprintf("miner `%s` · %s", a.Miner, a.Severity)}}},
			},
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
