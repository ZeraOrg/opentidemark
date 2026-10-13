// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ZeraOrg/tidemark/internal/httpx"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/service"
)

// Validation patterns for values that end up in source URLs or request bodies.
var (
	reIndex     = regexp.MustCompile(`^[A-Za-z0-9._*,:+-]{1,255}$`)
	reTimeField = regexp.MustCompile(`^[A-Za-z0-9_@.-]{1,255}$`)
)

// maxCACertBytes bounds an uploaded CA bundle.
const maxCACertBytes = 64 << 10

// Sealer encrypts secrets at rest (auth.SecretBox, SPEC §15.5).
type Sealer interface {
	Seal(plaintext string) (string, error)
	Open(sealed string) (string, error)
}

func invalid(field, msg string) error {
	return &service.ValidationError{Field: "config." + field, Msg: msg}
}

// decodeStrict decodes a JSON object rejecting unknown fields.
func decodeStrict(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &service.ValidationError{Field: "config", Msg: err.Error()}
	}
	return nil
}

func validateURL(raw string) error {
	if raw == "" || len(raw) > MaxURLLen {
		return invalid("url", "required, at most 2048 characters")
	}
	if _, err := httpx.ValidateURL(raw, true); err != nil {
		return invalid("url", err.Error())
	}
	return nil
}

func validateStream(s string) error {
	if s != "" && !model.ValidStreamName(s) {
		return invalid("stream", "must match ^[a-z0-9][a-z0-9._-]{0,63}$")
	}
	return nil
}

func validatePageSize(n *int, def, maxSize int) error {
	switch {
	case *n == 0:
		*n = def
	case *n < 1 || *n > maxSize:
		return invalid("pageSize", fmt.Sprintf("must be 1-%d", maxSize))
	}
	return nil
}

func (a *Auth) validate() error {
	if a.Type == "" {
		a.Type = AuthNone
	}
	switch {
	case a.Type == AuthNone:
		*a = Auth{Type: AuthNone}
	case a.Type == AuthBasic && a.Username != "" && a.Password != "":
		a.APIKey, a.Token = "", ""
	case a.Type == AuthAPIKey && a.APIKey != "":
		a.Username, a.Password, a.Token = "", "", ""
	case a.Type == AuthBearer && a.Token != "":
		a.Username, a.Password, a.APIKey = "", "", ""
	default:
		return invalid("auth", "type must be none, basic (username, password), apiKey (apiKey) or bearer (token)")
	}
	return nil
}

func (c *ElasticConfig) validate() error {
	if err := validateURL(c.URL); err != nil {
		return err
	}
	if err := c.Auth.validate(); err != nil {
		return err
	}
	if !reIndex.MatchString(c.Index) || strings.HasPrefix(c.Index, "-") || strings.HasPrefix(c.Index, "_") {
		return invalid("index", "must be an index name or pattern (letters, digits, . _ - * , : +)")
	}
	if c.TimeField == "" {
		c.TimeField = DefaultTimeField
	}
	if !reTimeField.MatchString(c.TimeField) {
		return invalid("timeField", "must be a field name")
	}
	if c.From != nil && c.To != nil && !c.From.Before(*c.To) {
		return invalid("from", "must be before to")
	}
	if err := c.validateQuery(); err != nil {
		return err
	}
	if err := validateCA(c.CACert); err != nil {
		return err
	}
	if err := validateStream(c.Stream); err != nil {
		return err
	}
	return validatePageSize(&c.PageSize, DefaultElasticPageSize, MaxElasticPageSize)
}

func (c *ElasticConfig) validateQuery() error {
	if len(bytes.TrimSpace(c.Query)) == 0 || string(bytes.TrimSpace(c.Query)) == "null" {
		c.Query = nil
		return nil
	}
	var obj map[string]json.RawMessage
	if len(c.Query) > MaxQueryBytes || json.Unmarshal(c.Query, &obj) != nil || len(obj) == 0 {
		return invalid("query", "must be a non-empty query DSL object of at most 64 KiB")
	}
	return nil
}

func validateCA(pem string) error {
	if pem == "" {
		return nil
	}
	if len(pem) > maxCACertBytes || !x509.NewCertPool().AppendCertsFromPEM([]byte(pem)) {
		return invalid("caCert", "must be a PEM certificate bundle of at most 64 KiB")
	}
	return nil
}

func (c *SeqConfig) validate() error {
	if err := validateURL(c.URL); err != nil {
		return err
	}
	if c.From != nil && c.To != nil && !c.From.Before(*c.To) {
		return invalid("from", "must be before to")
	}
	if len(c.Filter) > MaxFilterLen {
		return invalid("filter", "must be at most 8192 characters")
	}
	if !c.events() && !c.ImportSignals {
		return invalid("importEvents", "import events, signals or both")
	}
	if err := validateStream(c.Stream); err != nil {
		return err
	}
	return validatePageSize(&c.PageSize, DefaultSeqPageSize, MaxSeqPageSize)
}

func (c *CLEFConfig) validate() error {
	if len(c.Files) == 0 || len(c.Files) > MaxUploadFiles {
		return invalid("files", fmt.Sprintf("1-%d files are required", MaxUploadFiles))
	}
	return validateStream(c.Stream)
}

// sealSecret replaces *v by its sealed form (no-op for empty or already sealed values).
func sealSecret(s Sealer, v *string) error {
	if *v == "" {
		return nil
	}
	sealed, err := s.Seal(*v)
	if err != nil {
		return fmt.Errorf("seal import secret: %w", err)
	}
	*v = sealed
	return nil
}

func openSecret(s Sealer, v *string) error {
	if *v == "" {
		return nil
	}
	plain, err := s.Open(*v)
	if err != nil {
		return fmt.Errorf("open import secret: %w", err)
	}
	*v = plain
	return nil
}

func (a *Auth) seal(s Sealer) error {
	for _, v := range []*string{&a.Password, &a.APIKey, &a.Token} {
		if err := sealSecret(s, v); err != nil {
			return err
		}
	}
	return nil
}

func (a *Auth) open(s Sealer) error {
	for _, v := range []*string{&a.Password, &a.APIKey, &a.Token} {
		if err := openSecret(s, v); err != nil {
			return err
		}
	}
	return nil
}
