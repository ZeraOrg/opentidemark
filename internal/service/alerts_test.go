// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/alerting"
	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
)

type recordingTester struct {
	err  error
	last meta.Channel
}

func (r *recordingTester) Test(_ context.Context, ch meta.Channel, _ string) error {
	r.last = ch
	return r.err
}

type stubEvaluator struct{}

func (stubEvaluator) Evaluate(_ context.Context, r meta.AlertRule, now time.Time) alerting.Outcome {
	return alerting.Outcome{State: alerting.RuleState{Groups: map[string]alerting.GroupState{"": {State: alerting.StateOK, Since: now}}},
		Samples: []alerting.Sample{{Key: r.ID}}}
}

type alertEnv struct {
	channels *Channels
	alerts   *Alerts
	tester   *recordingTester
	repo     *meta.Channels
	rules    *meta.AlertRules
	now      time.Time
}

func newAlertEnv(t *testing.T) *alertEnv {
	t.Helper()
	db := openDB(t)
	box, err := auth.NewSecretBox(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := &alertEnv{tester: &recordingTester{}, repo: meta.NewChannels(db), rules: meta.NewAlertRules(db),
		now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	audit := NewAuditor(meta.NewAuditLog(db), quiet(), time.Now)
	e.channels = NewChannels(ChannelsDeps{Repo: e.repo, Rules: e.rules, Box: box,
		Policy: alerting.ChannelPolicy{AllowPrivate: true, SMTPConfigured: true}, Audit: audit, PublicURL: "https://tm"})
	e.channels.SetTester(e.tester)
	e.alerts = NewAlerts(e.rules, e.channels, stubEvaluator{}, audit, func() time.Time { return e.now })
	return e
}

func TestChannelSecretsAtRest(t *testing.T) {
	e := newAlertEnv(t)
	ctx := context.Background()
	cfg := `{"url":"https://hooks.example.com/T/secret-path","secret":"hmac-key","headers":{"Authorization":"Bearer tok"}}`
	c, err := e.channels.Create(ctx, SystemActor, ChannelInput{Name: " Ops ", Kind: alerting.KindWebhook, Config: json.RawMessage(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Ops" || strings.Contains(string(c.Config), "secret-path") || !strings.Contains(string(c.Config), SecretMask) {
		t.Errorf("create result not masked: %s", c.Config)
	}
	stored, _ := e.repo.Get(ctx, c.ID)
	for _, plain := range []string{"secret-path", "hmac-key", "Bearer tok"} {
		if strings.Contains(string(stored.Config), plain) {
			t.Errorf("stored config leaks %q: %s", plain, stored.Config)
		}
	}
	if !strings.Contains(string(stored.Config), "enc:v1:") {
		t.Errorf("not sealed: %s", stored.Config)
	}
	resolved, err := e.channels.Resolve(ctx, c.ID)
	if err != nil || !strings.Contains(string(resolved.Config), "secret-path") || !strings.Contains(string(resolved.Config), "Bearer tok") {
		t.Errorf("resolve %s %v", resolved.Config, err)
	}
	list, _ := e.channels.List(ctx)
	got, _ := e.channels.Get(ctx, c.ID)
	if len(list) != 1 || strings.Contains(string(list[0].Config), "hmac") || strings.Contains(string(got.Config), "hmac") {
		t.Errorf("list/get not masked")
	}
	// Update keeping masked secrets but changing a header and the name.
	upd := `{"url":"••••","secret":"••••","headers":{"Authorization":"••••","X-Env":"prod"}}`
	c2, err := e.channels.Update(ctx, SystemActor, c.ID, ChannelInput{Name: "Ops 2", Kind: alerting.KindWebhook,
		Config: json.RawMessage(upd), Version: c.Version})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := e.channels.Resolve(ctx, c.ID)
	var rc alerting.ChannelConfig
	_ = json.Unmarshal(r2.Config, &rc)
	if rc.URL != "https://hooks.example.com/T/secret-path" || rc.Secret != "hmac-key" || rc.Headers["Authorization"] != "Bearer tok" ||
		rc.Headers["X-Env"] != "prod" || c2.Version != 2 {
		t.Errorf("update merged %+v v%d", rc, c2.Version)
	}
	if _, err := e.channels.Update(ctx, SystemActor, c.ID, ChannelInput{Name: "x", Kind: alerting.KindWebhook,
		Config: json.RawMessage(upd), Version: 1}); !errors.Is(err, meta.ErrConflict) {
		t.Errorf("stale version: %v", err)
	}
	// Changing the kind does not carry masked secrets over.
	if _, err := e.channels.Update(ctx, SystemActor, c.ID, ChannelInput{Name: "x", Kind: alerting.KindSlack,
		Config: json.RawMessage(`{"url":"••••"}`), Version: 2}); err == nil {
		t.Error("masked url accepted for a new kind")
	}
	if _, err := e.channels.Update(ctx, SystemActor, c.ID, ChannelInput{Name: "x", Kind: alerting.KindWebhook, Config: json.RawMessage(upd)}); err == nil {
		t.Error("missing version accepted")
	}
}

func TestChannelValidationAndTest(t *testing.T) {
	e := newAlertEnv(t)
	ctx := context.Background()
	bad := []ChannelInput{
		{Name: "", Kind: alerting.KindSlack, Config: json.RawMessage(`{"url":"https://x.example.com"}`)},
		{Name: "x", Kind: "pager", Config: json.RawMessage(`{"url":"https://x.example.com"}`)},
		{Name: "x", Kind: alerting.KindSlack, Config: json.RawMessage(`{"url":"ftp://x"}`)},
		{Name: "x", Kind: alerting.KindSlack, Config: json.RawMessage(`{"nope":1}`)},
		{Name: "x", Kind: alerting.KindEmail, Config: json.RawMessage(`{"to":[]}`)},
	}
	for i, in := range bad {
		var ve *ValidationError
		if _, err := e.channels.Create(ctx, SystemActor, in); !errors.As(err, &ve) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	c, err := e.channels.Create(ctx, SystemActor, ChannelInput{Name: "mail", Kind: alerting.KindEmail,
		Config: json.RawMessage(`{"to":["ops@example.com"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := e.channels.Test(ctx, SystemActor, c.ID); err != nil || !res.OK || e.tester.last.ID != c.ID {
		t.Errorf("test %+v %v", res, err)
	}
	e.tester.err = errors.New("smtp down")
	if res, err := e.channels.Test(ctx, SystemActor, c.ID); err != nil || res.OK || res.Error != "smtp down" {
		t.Errorf("failed test %+v %v", res, err)
	}
	if _, err := e.channels.Test(ctx, SystemActor, "missing"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

const testRuleDoc = `{"query":"level:error | stats count()","condition":{"op":">","threshold":5},"window":"5m","every":"1m"}`

func TestAlertsCRUDAndMute(t *testing.T) {
	e := newAlertEnv(t)
	ctx := context.Background()
	ch, err := e.channels.Create(ctx, SystemActor, ChannelInput{Name: "s", Kind: alerting.KindSlack,
		Config: json.RawMessage(`{"url":"https://hooks.slack.com/x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	withChannel := strings.Replace(testRuleDoc, `"every":"1m"`, `"every":"1m","channels":["`+ch.ID+`"]`, 1)
	if _, err := e.alerts.Create(ctx, SystemActor, AlertInput{Name: "x", Doc: json.RawMessage(strings.Replace(withChannel, ch.ID, "nope", 1))}); err == nil {
		t.Error("unknown channel accepted")
	}
	if _, err := e.alerts.Create(ctx, SystemActor, AlertInput{Name: " ", Doc: json.RawMessage(testRuleDoc)}); err == nil {
		t.Error("empty name accepted")
	}
	var re *alerting.RuleError
	if _, err := e.alerts.Create(ctx, SystemActor, AlertInput{Name: "x", Doc: json.RawMessage(`{"query":"x"}`)}); !errors.As(err, &re) {
		t.Errorf("invalid doc: %v", err)
	}
	r, err := e.alerts.Create(ctx, SystemActor, AlertInput{Name: "errors", Doc: json.RawMessage(withChannel)})
	if err != nil || !r.Enabled || r.State != "ok" || !strings.Contains(string(r.Doc), `"severity":"warning"`) {
		t.Fatalf("create %+v %v", r, err)
	}
	if err := e.channels.Delete(ctx, SystemActor, ch.ID); !errors.Is(err, ErrChannelInUse) {
		t.Errorf("delete used channel: %v", err)
	}
	off := false
	r, err = e.alerts.Update(ctx, SystemActor, r.ID, AlertInput{Name: "errors!", Enabled: &off, Doc: json.RawMessage(testRuleDoc), Version: 1})
	if err != nil || r.Enabled || r.Version != 2 || r.Name != "errors!" {
		t.Fatalf("update %+v %v", r, err)
	}
	if _, err := e.alerts.Update(ctx, SystemActor, r.ID, AlertInput{Name: "y", Doc: json.RawMessage(testRuleDoc), Version: 1}); !errors.Is(err, meta.ErrConflict) {
		t.Errorf("stale: %v", err)
	}
	if _, err := e.alerts.Update(ctx, SystemActor, r.ID, AlertInput{Name: "y", Doc: json.RawMessage(testRuleDoc)}); err == nil {
		t.Error("missing version")
	}
	until := e.now.Add(time.Hour)
	m, err := e.alerts.Mute(ctx, SystemActor, r.ID, &until)
	if err != nil || !strings.Contains(string(m.Doc), until.Format(time.RFC3339)) || m.Version != 3 {
		t.Fatalf("mute %s %v", m.Doc, err)
	}
	if m, err = e.alerts.Mute(ctx, SystemActor, r.ID, nil); err != nil || !strings.Contains(string(m.Doc), `"muteUntil":null`) {
		t.Errorf("unmute %s %v", m.Doc, err)
	}
	past := e.now.Add(-time.Minute)
	far := e.now.Add(400 * 24 * time.Hour)
	for _, u := range []*time.Time{&past, &far} {
		if _, err := e.alerts.Mute(ctx, SystemActor, r.ID, u); err == nil {
			t.Errorf("mute until %v accepted", u)
		}
	}
	if _, err := e.alerts.Mute(ctx, SystemActor, "missing", nil); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("mute missing: %v", err)
	}
	out, err := e.alerts.Test(ctx, r.ID)
	if err != nil || out.Samples[0].Key != r.ID {
		t.Errorf("test %+v %v", out, err)
	}
	if _, err := e.alerts.Test(ctx, "missing"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("test missing: %v", err)
	}
	if err := e.rules.AddHistory(ctx, &meta.AlertHistory{RuleID: r.ID, At: e.now, FromState: "ok", ToState: "firing"}); err != nil {
		t.Fatal(err)
	}
	if h, err := e.alerts.History(ctx, r.ID, 0); err != nil || len(h) != 1 {
		t.Errorf("history %+v %v", h, err)
	}
	if _, err := e.alerts.History(ctx, "missing", 5000); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("history missing: %v", err)
	}
	if l, _ := e.alerts.List(ctx); len(l) != 1 {
		t.Error("list")
	}
	if err := e.alerts.Delete(ctx, SystemActor, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.channels.Delete(ctx, SystemActor, ch.ID); err != nil {
		t.Errorf("delete unused channel: %v", err)
	}
	if _, err := e.alerts.Get(ctx, r.ID); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}
