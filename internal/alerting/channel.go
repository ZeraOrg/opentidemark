// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/httpx"
)

// Channel kinds (SPEC §14.2).
const (
	KindWebhook = "webhook"
	KindSlack   = "slack"
	KindTeams   = "teams"
	KindEmail   = "email"
)

// Channel config limits.
const (
	maxHeaders        = 20
	maxHeaderValueLen = 1024
	maxSecretLen      = 1024
	maxURLLen         = 2048
	maxRecipients     = 20
)

// ErrInvalidChannel is wrapped by channel validation errors.
var ErrInvalidChannel = errors.New("invalid channel")

// reservedHeaders may not be overridden by custom webhook headers.
var reservedHeaders = map[string]bool{"Host": true, "Content-Length": true, "Content-Type": true, "Transfer-Encoding": true,
	"Connection": true, "User-Agent": true, SignatureHeader: true}

// ChannelConfig is the configuration of every channel kind. Secret fields (URL, Secret and
// header values) are sealed at rest and masked in API output (SPEC §15.5).
type ChannelConfig struct {
	URL     string            `json:"url,omitempty"`
	Secret  string            `json:"secret,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	To      []string          `json:"to,omitempty"`
}

// ChannelPolicy holds the deployment settings that affect channel validation.
type ChannelPolicy struct {
	AllowPrivate   bool // TIDEMARK_ALLOW_PRIVATE_WEBHOOKS
	SMTPConfigured bool
}

// DecodeChannelConfig strictly decodes a config.
func DecodeChannelConfig(raw json.RawMessage) (ChannelConfig, error) {
	var c ChannelConfig
	if len(raw) == 0 {
		return c, fmt.Errorf("%w: config is required", ErrInvalidChannel)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%w: %w", ErrInvalidChannel, err)
	}
	return c, nil
}

// MapSecrets replaces every secret value with f(value) (seal, open or mask).
func (c *ChannelConfig) MapSecrets(f func(string) (string, error)) error {
	var err error
	if c.URL, err = f(c.URL); err != nil {
		return err
	}
	if c.Secret, err = f(c.Secret); err != nil {
		return err
	}
	for k, v := range c.Headers {
		if c.Headers[k], err = f(v); err != nil {
			return err
		}
	}
	return nil
}

// ValidateChannel checks a plaintext config for kind.
func ValidateChannel(kind string, c ChannelConfig, p ChannelPolicy) error {
	switch kind {
	case KindWebhook:
		if err := validateURL(c.URL, p); err != nil {
			return err
		}
		if len(c.Secret) > maxSecretLen || len(c.To) > 0 {
			return fmt.Errorf("%w: webhook takes url, secret (≤ 1024) and headers", ErrInvalidChannel)
		}
		return validateHeaders(c.Headers)
	case KindSlack, KindTeams:
		if c.Secret != "" || len(c.Headers) > 0 || len(c.To) > 0 {
			return fmt.Errorf("%w: %s takes only url", ErrInvalidChannel, kind)
		}
		return validateURL(c.URL, p)
	case KindEmail:
		return validateEmail(c, p)
	default:
		return fmt.Errorf("%w: kind must be webhook, slack, teams or email", ErrInvalidChannel)
	}
}

func validateURL(raw string, p ChannelPolicy) error {
	if raw == "" || len(raw) > maxURLLen {
		return fmt.Errorf("%w: url is required (at most %d characters)", ErrInvalidChannel, maxURLLen)
	}
	if _, err := httpx.ValidateURL(raw, p.AllowPrivate); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidChannel, err)
	}
	return nil
}

func validateHeaders(h map[string]string) error {
	if len(h) > maxHeaders {
		return fmt.Errorf("%w: at most %d headers", ErrInvalidChannel, maxHeaders)
	}
	for k, v := range h {
		if !validHeaderName(k) || reservedHeaders[http.CanonicalHeaderKey(k)] {
			return fmt.Errorf("%w: header %q is not allowed", ErrInvalidChannel, k)
		}
		if len(v) > maxHeaderValueLen || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("%w: header %q has an invalid value", ErrInvalidChannel, k)
		}
	}
	return nil
}

// validHeaderName accepts RFC 7230 token characters.
func validHeaderName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !isTokenRune(r) {
			return false
		}
	}
	return true
}

func isTokenRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

func validateEmail(c ChannelConfig, p ChannelPolicy) error {
	if c.URL != "" || c.Secret != "" || len(c.Headers) > 0 {
		return fmt.Errorf("%w: email takes only to", ErrInvalidChannel)
	}
	if !p.SMTPConfigured {
		return fmt.Errorf("%w: SMTP is not configured (TIDEMARK_SMTP_HOST, TIDEMARK_SMTP_FROM)", ErrInvalidChannel)
	}
	if len(c.To) == 0 || len(c.To) > maxRecipients {
		return fmt.Errorf("%w: 1 to %d recipients are required", ErrInvalidChannel, maxRecipients)
	}
	for _, to := range c.To {
		if a, err := mail.ParseAddress(to); err != nil || a.Name != "" || strings.ContainsAny(to, "\r\n") {
			return fmt.Errorf("%w: invalid recipient %q", ErrInvalidChannel, to)
		}
	}
	return nil
}
