// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/tql"
)

const validRule = `{"description":"Checkout errors spike",
 "query":"service:checkout-api and level >= error | stats count() by service",
 "condition":{"op":">","threshold":25},"window":"5m","every":"1m","for":"2m","severity":"critical",
 "channels":["c1"],"noData":"ok","groupBy":["service"],"muteUntil":null}`

func editRule(t *testing.T, f func(m map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validRule), &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, _ := json.Marshal(m)
	return string(b)
}

func TestParseRuleValid(t *testing.T) {
	d, err := ParseRule(json.RawMessage(validRule))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(d.Window) != 5*time.Minute || time.Duration(d.For) != 2*time.Minute || d.Severity != SeverityCritical {
		t.Errorf("%+v", d)
	}
	minimal := `{"query":"* | stats count()","condition":{"op":"<","threshold":1},"window":"10m","every":"30s"}`
	d, err = ParseRule(json.RawMessage(minimal))
	if err != nil || d.Severity != SeverityWarning || d.NoData != NoDataOK || d.Channels == nil || d.GroupBy == nil {
		t.Errorf("defaults %+v %v", d, err)
	}
	for _, q := range []string{
		"source spans | kind:server | stats p95(duration)",
		"source metrics | name:cpu | stats avg(value) by host",
		"level:error | stats count() by service | where count > 5",
		"* | stats count_distinct(user) by service",
	} {
		doc := editRule(t, func(m map[string]any) {
			m["query"] = q
			if strings.Contains(q, "by host") {
				m["groupBy"] = []string{"host"}
			} else if !strings.Contains(q, "by service") {
				m["groupBy"] = []string{}
			}
		})
		if _, err := ParseRule(json.RawMessage(doc)); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"window":"10m0s"`) {
		t.Errorf("marshal %s", b)
	}
}

func TestParseRuleInvalid(t *testing.T) {
	tests := map[string]string{
		"empty":         ``,
		"syntax":        `{`,
		"trailing":      validRule + `{}`,
		"unknown field": editRule(t, func(m map[string]any) { m["extra"] = true }),
		"deep":          strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70),
		"description":   editRule(t, func(m map[string]any) { m["description"] = strings.Repeat("d", 1025) }),
		"op":            editRule(t, func(m map[string]any) { m["condition"] = map[string]any{"op": "=~", "threshold": 1} }),
		"window short":  editRule(t, func(m map[string]any) { m["window"] = "1s" }),
		"window long":   editRule(t, func(m map[string]any) { m["window"] = "200h" }),
		"window bad":    editRule(t, func(m map[string]any) { m["window"] = "soon" }),
		"window type":   editRule(t, func(m map[string]any) { m["window"] = 5 }),
		"every short":   editRule(t, func(m map[string]any) { m["every"] = "5s" }),
		"every long":    editRule(t, func(m map[string]any) { m["every"] = "25h" }),
		"for negative":  editRule(t, func(m map[string]any) { m["for"] = "-1m" }),
		"for long":      editRule(t, func(m map[string]any) { m["for"] = "25h" }),
		"severity":      editRule(t, func(m map[string]any) { m["severity"] = "page" }),
		"noData":        editRule(t, func(m map[string]any) { m["noData"] = "nodata" }),
		"dup channel":   editRule(t, func(m map[string]any) { m["channels"] = []string{"a", "a"} }),
		"empty channel": editRule(t, func(m map[string]any) { m["channels"] = []string{""} }),
		"many channels": editRule(t, func(m map[string]any) {
			var cs []string
			for i := range MaxChannels + 1 {
				cs = append(cs, string(rune('a'+i)))
			}
			m["channels"] = cs
		}),
		"no query":      editRule(t, func(m map[string]any) { m["query"] = "" }),
		"long query":    editRule(t, func(m map[string]any) { m["query"] = strings.Repeat("a", maxQueryLen+1) }),
		"no stats":      editRule(t, func(m map[string]any) { m["query"] = "level:error"; m["groupBy"] = []string{} }),
		"top":           editRule(t, func(m map[string]any) { m["query"] = "level:error | top 5 service"; m["groupBy"] = []string{} }),
		"two aggs":      editRule(t, func(m map[string]any) { m["query"] = "* | stats count(), avg(x) by service" }),
		"span":          editRule(t, func(m map[string]any) { m["query"] = "* | stats count() by service span 1m" }),
		"groupBy extra": editRule(t, func(m map[string]any) { m["groupBy"] = []string{"service", "host"} }),
		"groupBy order": editRule(t, func(m map[string]any) {
			m["query"] = "* | stats count() by host, service"
			m["groupBy"] = []string{"service", "host"}
		}),
		"groupBy none":   editRule(t, func(m map[string]any) { m["groupBy"] = []string{} }),
		"string value":   editRule(t, func(m map[string]any) { m["query"] = "* | stats first(message) by service" }),
		"bad level":      editRule(t, func(m map[string]any) { m["query"] = "level >= erorr | stats count() by service" }),
		"variable":       editRule(t, func(m map[string]any) { m["query"] = "service:$svc | stats count() by service" }),
		"threshold type": editRule(t, func(m map[string]any) { m["condition"] = map[string]any{"op": ">", "threshold": "x"} }),
	}
	for name, doc := range tests {
		_, err := ParseRule(json.RawMessage(doc))
		var qe *tql.Error
		if err == nil || (!errors.Is(err, ErrInvalidRule) && !errors.As(err, &qe)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRuleMutedAndCompileRange(t *testing.T) {
	d, _ := ParseRule(json.RawMessage(validRule))
	if d.Muted(t0) {
		t.Error("not muted")
	}
	until := t0.Add(time.Hour)
	d.MuteUntil = &until
	if !d.Muted(t0) || d.Muted(until) {
		t.Error("mute window")
	}
	c, err := d.Compile(t0, "tidemark", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "FROM tidemark.events") || c.Args[0] != t0.Add(-5*time.Minute) || c.Args[1] != t0 {
		t.Errorf("sql %s args %v", c.SQL, c.Args[:2])
	}
}

func TestValidateChannel(t *testing.T) {
	pub := ChannelPolicy{}
	priv := ChannelPolicy{AllowPrivate: true, SMTPConfigured: true}
	ok := []struct {
		kind string
		c    ChannelConfig
		p    ChannelPolicy
	}{
		{KindWebhook, ChannelConfig{URL: "https://hooks.example.com/x", Secret: "s", Headers: map[string]string{"Authorization": "Bearer t"}}, pub},
		{KindWebhook, ChannelConfig{URL: "http://10.0.0.5/hook"}, priv},
		{KindSlack, ChannelConfig{URL: "https://hooks.slack.com/services/a/b/c"}, pub},
		{KindTeams, ChannelConfig{URL: "https://example.webhook.office.com/x"}, pub},
		{KindEmail, ChannelConfig{To: []string{"ops@example.com"}}, priv},
	}
	for _, tc := range ok {
		if err := ValidateChannel(tc.kind, tc.c, tc.p); err != nil {
			t.Errorf("%s %+v: %v", tc.kind, tc.c, err)
		}
	}
	bad := []struct {
		kind string
		c    ChannelConfig
		p    ChannelPolicy
	}{
		{"sms", ChannelConfig{}, pub},
		{KindWebhook, ChannelConfig{}, pub},
		{KindWebhook, ChannelConfig{URL: "http://hooks.example.com"}, pub},
		{KindWebhook, ChannelConfig{URL: "https://127.0.0.1/x"}, pub},
		{KindWebhook, ChannelConfig{URL: "https://169.254.169.254/latest"}, pub},
		{KindWebhook, ChannelConfig{URL: "https://user:pw@example.com"}, pub},
		{KindWebhook, ChannelConfig{URL: "https://example.com/" + strings.Repeat("a", maxURLLen)}, pub},
		{KindWebhook, ChannelConfig{URL: "https://example.com", Secret: strings.Repeat("s", maxSecretLen+1)}, pub},
		{KindWebhook, ChannelConfig{URL: "https://example.com", Headers: map[string]string{"Host": "x"}}, pub},
		{KindWebhook, ChannelConfig{URL: "https://example.com", Headers: map[string]string{"x-tidemark-signature": "x"}}, pub},
		{KindWebhook, ChannelConfig{URL: "https://example.com", Headers: map[string]string{"Bad Name": "x"}}, pub},
		{KindWebhook, ChannelConfig{URL: "https://example.com", Headers: map[string]string{"X-A": "a\r\nInjected: 1"}}, pub},
		{KindWebhook, ChannelConfig{URL: "https://example.com", To: []string{"a@b.c"}}, pub},
		{KindSlack, ChannelConfig{URL: "https://example.com", Secret: "x"}, pub},
		{KindTeams, ChannelConfig{}, pub},
		{KindEmail, ChannelConfig{To: []string{"ops@example.com"}}, ChannelPolicy{}},
		{KindEmail, ChannelConfig{}, priv},
		{KindEmail, ChannelConfig{To: []string{"not an address"}}, priv},
		{KindEmail, ChannelConfig{To: []string{"Ops <ops@example.com>"}}, priv},
		{KindEmail, ChannelConfig{To: []string{"a@b.c"}, URL: "https://x"}, priv},
	}
	for _, tc := range bad {
		if err := ValidateChannel(tc.kind, tc.c, tc.p); !errors.Is(err, ErrInvalidChannel) {
			t.Errorf("%s %+v accepted: %v", tc.kind, tc.c, err)
		}
	}
	many := map[string]string{}
	for i := range maxHeaders + 1 {
		many["X-H"+string(rune('a'+i))] = "v"
	}
	if err := ValidateChannel(KindWebhook, ChannelConfig{URL: "https://example.com", Headers: many}, pub); err == nil {
		t.Error("too many headers")
	}
}

func TestChannelConfigSecrets(t *testing.T) {
	c, err := DecodeChannelConfig(json.RawMessage(`{"url":"u","secret":"s","headers":{"A":"h"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MapSecrets(func(s string) (string, error) { return "<" + s + ">", nil }); err != nil {
		t.Fatal(err)
	}
	if c.URL != "<u>" || c.Secret != "<s>" || c.Headers["A"] != "<h>" {
		t.Errorf("%+v", c)
	}
	boom := errors.New("boom")
	for i := range 3 {
		calls := 0
		err := c.MapSecrets(func(s string) (string, error) {
			calls++
			if calls > i {
				return "", boom
			}
			return s, nil
		})
		if !errors.Is(err, boom) {
			t.Errorf("error %d not propagated", i)
		}
	}
	for _, raw := range []string{``, `{"url":1}`, `{"bogus":"x"}`} {
		if _, err := DecodeChannelConfig(json.RawMessage(raw)); !errors.Is(err, ErrInvalidChannel) {
			t.Errorf("%q: %v", raw, err)
		}
	}
}
