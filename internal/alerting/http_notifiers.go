// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// SignatureHeader carries the webhook HMAC-SHA256 signature (SPEC §14.2).
const SignatureHeader = "X-Tidemark-Signature"

// HTTPDoer sends requests; it must be the SSRF-safe client (SPEC §15.6).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// postJSON sends body and classifies the response: 2xx ok, 408/429/5xx and transport
// errors retryable, other statuses permanent.
func postJSON(ctx context.Context, client HTTPDoer, target string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Err: errors.New("build request: invalid URL")}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // *url.Error embeds the URL, which is a secret (SPEC §15.5)
		}
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain)) // drain for connection reuse
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("endpoint returned %d", resp.StatusCode)
	default:
		return &PermanentError{Err: fmt.Errorf("endpoint returned %d", resp.StatusCode)}
	}
}

func decodeConfig(ch meta.Channel) (ChannelConfig, error) {
	c, err := DecodeChannelConfig(ch.Config)
	if err != nil {
		return c, &PermanentError{Err: err}
	}
	return c, nil
}

// Webhook posts the JSON payload of SPEC §14.2.
type Webhook struct{ Client HTTPDoer }

// Kind implements Notifier.
func (Webhook) Kind() string { return KindWebhook }

// WebhookPayload is the webhook body.
type WebhookPayload struct {
	Rule          WebhookRule       `json:"rule"`
	State         string            `json:"state"`
	PreviousState string            `json:"previousState"`
	Value         *float64          `json:"value"`
	Threshold     float64           `json:"threshold"`
	Group         map[string]string `json:"group"`
	At            time.Time         `json:"at"`
	URL           string            `json:"url"`
	Message       string            `json:"message,omitempty"`
}

// WebhookRule identifies the rule in a webhook payload.
type WebhookRule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
}

// Sign returns the signature header value for body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Notify implements Notifier.
func (w Webhook) Notify(ctx context.Context, ch meta.Channel, n Notification) error {
	c, err := decodeConfig(ch)
	if err != nil {
		return err
	}
	group := n.Group
	if group == nil {
		group = map[string]string{}
	}
	body, err := json.Marshal(WebhookPayload{Rule: WebhookRule{ID: n.RuleID, Name: n.RuleName, Severity: n.Severity},
		State: n.State, PreviousState: n.PreviousState, Value: n.Value, Threshold: n.Threshold, Group: group,
		At: n.At.UTC(), URL: n.URL, Message: n.Message})
	if err != nil {
		return &PermanentError{Err: err}
	}
	headers := make(map[string]string, len(c.Headers)+1)
	for k, v := range c.Headers {
		headers[k] = v
	}
	if c.Secret != "" {
		headers[SignatureHeader] = Sign(c.Secret, body)
	}
	return postJSON(ctx, w.Client, c.URL, body, headers)
}

// summary is the one-line title of a notification.
func summary(n Notification) string {
	return fmt.Sprintf("[%s] %s", strings.ToUpper(n.State), n.RuleName)
}

// facts are the label/value pairs shown by chat notifiers.
func facts(n Notification) [][2]string {
	out := [][2]string{{"Severity", n.Severity}, {"State", n.PreviousState + " → " + n.State}}
	if n.Value != nil {
		out = append(out, [2]string{"Value", FormatValue(*n.Value)})
	}
	if n.Op != "" {
		out = append(out, [2]string{"Condition", n.Op + " " + FormatValue(n.Threshold)})
	}
	if g := GroupKey(n.Group); g != "" {
		out = append(out, [2]string{"Group", g})
	}
	out = append(out, [2]string{"At", n.At.UTC().Format(time.RFC3339)})
	return out
}

// Slack posts a Block Kit message to an incoming webhook.
type Slack struct{ Client HTTPDoer }

// Kind implements Notifier.
func (Slack) Kind() string { return KindSlack }

// slackEscape escapes the characters Slack mrkdwn treats as control characters.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// SlackMessage builds the Block Kit payload.
func SlackMessage(n Notification) map[string]any {
	fields := []map[string]any{}
	for _, f := range facts(n) {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*" + f[0] + "*\n" + slackEscape(f[1])})
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": truncateRunes(summary(n), 150)}},
		{"type": "section", "fields": fields},
	}
	if text := strings.TrimSpace(n.Description + "\n" + n.Message); text != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": slackEscape(text)}})
	}
	if n.URL != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{{"type": "button",
			"text": map[string]any{"type": "plain_text", "text": "Open in OpenTidemark"}, "url": n.URL}}})
	}
	return map[string]any{"text": summary(n), "blocks": blocks}
}

// Notify implements Notifier.
func (s Slack) Notify(ctx context.Context, ch meta.Channel, n Notification) error {
	c, err := decodeConfig(ch)
	if err != nil {
		return err
	}
	body, err := json.Marshal(SlackMessage(n))
	if err != nil {
		return &PermanentError{Err: err}
	}
	return postJSON(ctx, s.Client, c.URL, body, nil)
}

// Teams posts an Adaptive Card to a Workflows / incoming webhook.
type Teams struct{ Client HTTPDoer }

// Kind implements Notifier.
func (Teams) Kind() string { return KindTeams }

// TeamsMessage builds the Adaptive Card payload.
func TeamsMessage(n Notification) map[string]any {
	factList := []map[string]any{}
	for _, f := range facts(n) {
		factList = append(factList, map[string]any{"title": f[0], "value": f[1]})
	}
	body := []map[string]any{
		{"type": "TextBlock", "text": summary(n), "weight": "Bolder", "size": "Medium", "wrap": true},
		{"type": "FactSet", "facts": factList},
	}
	if text := strings.TrimSpace(n.Description + "\n" + n.Message); text != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": text, "wrap": true})
	}
	card := map[string]any{"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard",
		"version": "1.4", "body": body}
	if n.URL != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open in OpenTidemark", "url": n.URL}}
	}
	return map[string]any{"type": "message", "attachments": []map[string]any{{
		"contentType": "application/vnd.microsoft.card.adaptive", "content": card}}}
}

// Notify implements Notifier.
func (t Teams) Notify(ctx context.Context, ch meta.Channel, n Notification) error {
	c, err := decodeConfig(ch)
	if err != nil {
		return err
	}
	body, err := json.Marshal(TeamsMessage(n))
	if err != nil {
		return &PermanentError{Err: err}
	}
	return postJSON(ctx, t.Client, c.URL, body, nil)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
