// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/alerting"
	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// Channel audit actions and the mask shown instead of secrets (SPEC §15.5).
const (
	ActionChannelCreate = "channel.create"
	ActionChannelUpdate = "channel.update"
	ActionChannelDelete = "channel.delete"
	ActionChannelTest   = "channel.test"
	SecretMask          = "••••" //nolint:gosec // G101: the display mask that replaces secrets, not a credential
)

// ErrChannelInUse is returned when deleting a channel that alert rules reference.
var ErrChannelInUse = errors.New("channel is used by alert rules")

// ChannelRepo is the channel persistence.
type ChannelRepo interface {
	Create(ctx context.Context, c *meta.Channel) error
	Get(ctx context.Context, id string) (meta.Channel, error)
	List(ctx context.Context) ([]meta.Channel, error)
	Update(ctx context.Context, c *meta.Channel) error
	Delete(ctx context.Context, id string) error
}

// Sealer encrypts secrets at rest (auth.SecretBox).
type Sealer interface {
	Seal(plaintext string) (string, error)
	Open(sealed string) (string, error)
}

// ChannelTester sends a test notification.
type ChannelTester interface {
	Test(ctx context.Context, ch meta.Channel, publicURL string) error
}

// RuleLister lists alert rules (to protect referenced channels).
type RuleLister interface {
	List(ctx context.Context) ([]meta.AlertRule, error)
}

// ChannelsDeps bundles the collaborators of Channels.
type ChannelsDeps struct {
	Repo      ChannelRepo
	Rules     RuleLister
	Box       Sealer
	Policy    alerting.ChannelPolicy
	Tester    ChannelTester
	Audit     *Auditor
	PublicURL string
}

// Channels manages notification channels; secrets are sealed at rest and masked in output.
type Channels struct{ d ChannelsDeps }

// NewChannels returns the channel service.
func NewChannels(d ChannelsDeps) *Channels { return &Channels{d: d} }

// SetTester sets the tester (the dispatcher depends on Channels, so it is wired afterwards).
func (s *Channels) SetTester(t ChannelTester) { s.d.Tester = t }

// ChannelInput is a create/update request; secret fields equal to SecretMask keep the stored
// value on update.
type ChannelInput struct {
	Name    string
	Kind    string
	Config  json.RawMessage
	Version int
}

// List returns all channels with masked secrets.
func (s *Channels) List(ctx context.Context) ([]meta.Channel, error) {
	chs, err := s.d.Repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range chs {
		if chs[i], err = masked(chs[i]); err != nil {
			return nil, err
		}
	}
	return chs, nil
}

// Get returns one channel with masked secrets.
func (s *Channels) Get(ctx context.Context, id string) (meta.Channel, error) {
	c, err := s.d.Repo.Get(ctx, id)
	if err != nil {
		return meta.Channel{}, err
	}
	return masked(c)
}

// Resolve returns a channel with decrypted secrets (implements alerting.ChannelResolver).
func (s *Channels) Resolve(ctx context.Context, id string) (meta.Channel, error) {
	c, err := s.d.Repo.Get(ctx, id)
	if err != nil {
		return meta.Channel{}, err
	}
	cfg, err := alerting.DecodeChannelConfig(c.Config)
	if err != nil {
		return meta.Channel{}, err
	}
	if err := cfg.MapSecrets(s.d.Box.Open); err != nil {
		return meta.Channel{}, err
	}
	if c.Config, err = json.Marshal(cfg); err != nil { //nolint:gosec // decrypted for delivery only, never stored or returned by the API
		return meta.Channel{}, err
	}
	return c, nil
}

// Create validates and stores a channel.
func (s *Channels) Create(ctx context.Context, actor Actor, in ChannelInput) (meta.Channel, error) {
	cfg, err := s.validate(in, nil)
	if err != nil {
		return meta.Channel{}, err
	}
	c := meta.Channel{Name: strings.TrimSpace(in.Name), Kind: in.Kind}
	if c.Config, err = s.seal(cfg); err != nil {
		return meta.Channel{}, err
	}
	if err := s.d.Repo.Create(ctx, &c); err != nil {
		return meta.Channel{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionChannelCreate, c.ID, map[string]any{"name": c.Name, "kind": c.Kind})
	return masked(c)
}

// Update replaces a channel (optimistic concurrency on Version).
func (s *Channels) Update(ctx context.Context, actor Actor, id string, in ChannelInput) (meta.Channel, error) {
	if in.Version < 1 {
		return meta.Channel{}, invalid("version", "is required")
	}
	current, err := s.Resolve(ctx, id)
	if err != nil {
		return meta.Channel{}, err
	}
	var prev *alerting.ChannelConfig
	if current.Kind == in.Kind {
		p, err := alerting.DecodeChannelConfig(current.Config)
		if err != nil {
			return meta.Channel{}, err
		}
		prev = &p
	}
	cfg, err := s.validate(in, prev)
	if err != nil {
		return meta.Channel{}, err
	}
	c := meta.Channel{ID: id, Name: strings.TrimSpace(in.Name), Kind: in.Kind, Version: in.Version}
	if c.Config, err = s.seal(cfg); err != nil {
		return meta.Channel{}, err
	}
	if err := s.d.Repo.Update(ctx, &c); err != nil {
		return meta.Channel{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionChannelUpdate, id, map[string]any{"name": c.Name, "kind": c.Kind})
	return masked(c)
}

// Delete removes a channel unless an alert rule references it.
func (s *Channels) Delete(ctx context.Context, actor Actor, id string) error {
	rules, err := s.d.Rules.List(ctx)
	if err != nil {
		return err
	}
	for i := range rules {
		if doc, err := alerting.ParseRule(rules[i].Doc); err == nil && slices.Contains(doc.Channels, id) {
			return fmt.Errorf("%w: %s", ErrChannelInUse, rules[i].Name)
		}
	}
	if err := s.d.Repo.Delete(ctx, id); err != nil {
		return err
	}
	s.d.Audit.Record(ctx, actor, ActionChannelDelete, id, nil)
	return nil
}

// ChannelTestResult is the outcome of a test delivery.
type ChannelTestResult struct {
	OK    bool
	Error string
}

// Test sends a test notification; a failed delivery is reported in the result.
func (s *Channels) Test(ctx context.Context, actor Actor, id string) (ChannelTestResult, error) {
	c, err := s.Resolve(ctx, id)
	if err != nil {
		return ChannelTestResult{}, err
	}
	res := ChannelTestResult{OK: true}
	if sendErr := s.d.Tester.Test(ctx, c, s.d.PublicURL); sendErr != nil {
		res = ChannelTestResult{Error: sendErr.Error()}
	}
	s.d.Audit.Record(ctx, actor, ActionChannelTest, id, map[string]any{"ok": res.OK})
	return res, nil
}

// validate decodes the plaintext config, keeps masked secrets from prev and validates.
func (s *Channels) validate(in ChannelInput, prev *alerting.ChannelConfig) (alerting.ChannelConfig, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxNameLen {
		return alerting.ChannelConfig{}, invalid("name", "is required (at most 128 characters)")
	}
	cfg, err := alerting.DecodeChannelConfig(in.Config)
	if err != nil {
		return cfg, invalid("config", err.Error())
	}
	keepMasked(&cfg, prev)
	if err := alerting.ValidateChannel(in.Kind, cfg, s.d.Policy); err != nil {
		return cfg, invalid("config", err.Error())
	}
	return cfg, nil
}

// keepMasked replaces masked values with the previous plaintext (or clears them).
func keepMasked(cfg *alerting.ChannelConfig, prev *alerting.ChannelConfig) {
	if prev == nil {
		prev = &alerting.ChannelConfig{}
	}
	if cfg.URL == SecretMask {
		cfg.URL = prev.URL
	}
	if cfg.Secret == SecretMask {
		cfg.Secret = prev.Secret
	}
	for k, v := range cfg.Headers {
		if v == SecretMask {
			cfg.Headers[k] = prev.Headers[k]
		}
	}
}

func (s *Channels) seal(cfg alerting.ChannelConfig) (json.RawMessage, error) {
	if err := cfg.MapSecrets(s.d.Box.Seal); err != nil {
		return nil, err
	}
	return json.Marshal(cfg) //nolint:gosec // secrets are sealed (encrypted) by MapSecrets above
}

// masked returns c with every secret replaced by SecretMask.
func masked(c meta.Channel) (meta.Channel, error) {
	cfg, err := alerting.DecodeChannelConfig(c.Config)
	if err != nil {
		return meta.Channel{}, err
	}
	_ = cfg.MapSecrets(func(v string) (string, error) { // the mask function cannot fail
		if v == "" {
			return "", nil
		}
		return SecretMask, nil
	})
	if c.Config, err = json.Marshal(cfg); err != nil { //nolint:gosec // secrets are replaced by SecretMask above
		return meta.Channel{}, err
	}
	return c, nil
}
