// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// BootstrapConnectionName is the connection created from TIDEMARK_BOOTSTRAP_INGEST_KEY.
const BootstrapConnectionName = "bootstrap"

// SeedConfig drives first-start seeding (SPEC §15.2).
type SeedConfig struct {
	AdminUsername          string
	AdminPassword          string
	AdminPasswordIsDefault bool
	BootstrapIngestKey     string
}

// SeedStores are the repositories used by Seed.
type SeedStores struct {
	Users       UserStore
	Connections interface {
		Create(ctx context.Context, c *meta.Connection) error
		GetByName(ctx context.Context, name string) (meta.Connection, error)
	}
	Settings SettingsStore
}

// SeedResult reports what Seed did.
type SeedResult struct {
	CreatedAdmin       bool
	CreatedBootstrap   bool
	DefaultCredentials bool
}

// Seeder performs first-start seeding.
type Seeder struct {
	stores SeedStores
	hasher *auth.PasswordHasher
	policy *auth.PasswordPolicy
	logger *slog.Logger
}

// NewSeeder returns a seeder.
func NewSeeder(stores SeedStores, hasher *auth.PasswordHasher, policy *auth.PasswordPolicy, logger *slog.Logger) *Seeder {
	return &Seeder{stores: stores, hasher: hasher, policy: policy, logger: logger}
}

// Seed creates the admin when no users exist (admin/admin with must_change_password unless a
// password is configured, which must then satisfy the password policy), creates the optional
// bootstrap connection, and logs a warning on every start while default credentials are in use.
func (s *Seeder) Seed(ctx context.Context, cfg SeedConfig) (SeedResult, error) {
	var res SeedResult
	n, err := s.stores.Users.Count(ctx, false)
	if err != nil {
		return res, err
	}
	if n == 0 {
		if err := s.seedAdmin(ctx, cfg); err != nil {
			return res, err
		}
		res.CreatedAdmin = true
	}
	if cfg.BootstrapIngestKey != "" {
		created, err := s.seedBootstrap(ctx, cfg.BootstrapIngestKey)
		if err != nil {
			return res, err
		}
		res.CreatedBootstrap = created
	}
	if _, err := s.stores.Settings.Get(ctx, SettingDefaultAdmin); err == nil {
		res.DefaultCredentials = true
		s.logger.Warn("the admin account still uses the default password; change it now (Settings → Users or POST /api/v1/auth/password)",
			"username", cfg.AdminUsername)
	} else if !errors.Is(err, meta.ErrNotFound) {
		return res, err
	}
	return res, nil
}

func (s *Seeder) seedAdmin(ctx context.Context, cfg SeedConfig) error {
	if !cfg.AdminPasswordIsDefault {
		if err := s.policy.Validate(cfg.AdminUsername, cfg.AdminPassword); err != nil {
			return fmt.Errorf("TIDEMARK_ADMIN_PASSWORD: %w", err)
		}
	}
	hash, err := s.hasher.Hash(cfg.AdminPassword)
	if err != nil {
		return err
	}
	u := meta.User{Username: cfg.AdminUsername, DisplayName: "Administrator", PasswordHash: hash, Role: meta.RoleAdmin,
		MustChangePassword: cfg.AdminPasswordIsDefault}
	if err := s.stores.Users.Create(ctx, &u); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if cfg.AdminPasswordIsDefault {
		b, _ := json.Marshal(u.ID) // marshalling a string cannot fail
		if err := s.stores.Settings.Set(ctx, SettingDefaultAdmin, b); err != nil {
			return err
		}
	}
	s.logger.Info("seeded admin user", "username", u.Username, "defaultPassword", cfg.AdminPasswordIsDefault)
	return nil
}

func (s *Seeder) seedBootstrap(ctx context.Context, key string) (bool, error) {
	if _, err := s.stores.Connections.GetByName(ctx, BootstrapConnectionName); err == nil {
		return false, nil
	} else if !errors.Is(err, meta.ErrNotFound) {
		return false, err
	}
	c := meta.Connection{Name: BootstrapConnectionName, Description: "Created from TIDEMARK_BOOTSTRAP_INGEST_KEY",
		KeyPrefix: auth.KeyPrefix(key), KeyHash: auth.HashKey(key), Protocols: protocolNames(auth.AllProtocols()),
		DefaultStream: model.DefaultStream}
	if err := s.stores.Connections.Create(ctx, &c); err != nil {
		return false, fmt.Errorf("seed bootstrap connection: %w", err)
	}
	s.logger.Info("created bootstrap ingest connection", "key", auth.Redact(key))
	return true, nil
}
