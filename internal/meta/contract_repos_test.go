// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func contractUsers(t *testing.T, open opener) {
	ctx := context.Background()
	r := NewUsers(open(t))
	u := &User{Username: "Admin", PasswordHash: "h", Role: RoleAdmin, MustChangePassword: true}
	if err := r.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, &User{Username: "admin", PasswordHash: "h", Role: RoleViewer}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate username (case-insensitive): %v", err)
	}
	got, err := r.GetByUsername(ctx, "ADMIN")
	if err != nil || got.ID != u.ID || !got.MustChangePassword || got.Role != RoleAdmin || !got.CreatedAt.Equal(t0) {
		t.Fatalf("get: %+v %v", got, err)
	}
	lock := t0.Add(time.Hour)
	if err := r.RecordLoginFailure(ctx, u.ID, 5, &lock); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(ctx, u.ID)
	if got.FailedLogins != 5 || got.LockedUntil == nil || !got.LockedUntil.Equal(lock) {
		t.Errorf("failure: %+v", got)
	}
	if err := r.RecordLoginSuccess(ctx, u.ID, t0); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(ctx, u.ID)
	if got.FailedLogins != 0 || got.LockedUntil != nil || got.LastLoginAt == nil {
		t.Errorf("success: %+v", got)
	}
	if err := r.SetPassword(ctx, u.ID, "h2", false); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(ctx, u.ID)
	got.DisplayName, got.Disabled = "The Admin", true
	if err := r.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(ctx, u.ID)
	if got.PasswordHash != "h2" || got.MustChangePassword || got.DisplayName != "The Admin" || !got.Disabled {
		t.Errorf("update: %+v", got)
	}
	if n, _ := r.Count(ctx, true); n != 0 {
		t.Errorf("enabled count %d", n)
	}
	if n, _ := r.Count(ctx, false); n != 1 {
		t.Errorf("count %d", n)
	}
	if n, _ := r.CountAdmins(ctx); n != 0 {
		t.Errorf("admins %d", n)
	}
	list, err := r.List(ctx)
	if err != nil || len(list) != 1 {
		t.Errorf("list %v %v", list, err)
	}
	if err := r.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted: %v", err)
	}
	if err := r.Delete(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
	if err := r.Update(ctx, &User{ID: "nope", Role: RoleViewer}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
}

func contractSessions(t *testing.T, open opener) {
	ctx := context.Background()
	db := open(t)
	users, r := NewUsers(db), NewSessions(db)
	u := &User{Username: "u", PasswordHash: "h", Role: RoleViewer}
	if err := users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		s := Session{ID: id, UserID: u.ID, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour), IP: "1.2.3.4", UserAgent: "ua"}
		if err := r.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	s, err := r.Get(ctx, "s1")
	if err != nil || s.UserID != u.ID || s.IP != "1.2.3.4" || !s.ExpiresAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("get %+v %v", s, err)
	}
	if err := r.Touch(ctx, "s1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := r.DeleteForUser(ctx, u.ID, "s1"); err != nil || n != 2 {
		t.Errorf("delete for user: %d %v", n, err)
	}
	if n, err := r.DeleteExpired(ctx, t0.Add(2*time.Hour), time.Hour); err != nil || n != 1 {
		t.Errorf("expired: %d %v", n, err)
	}
	if err := r.Delete(ctx, "s1"); err != nil {
		t.Error(err)
	}
	if _, err := r.Get(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func contractConnections(t *testing.T, open opener) {
	ctx := context.Background()
	r := NewConnections(open(t))
	c := &Connection{Name: "checkout", KeyPrefix: "tmk_abcd", KeyHash: "hash1", Protocols: []string{"seq", "otlp"},
		DefaultStream: "app", AllowedCIDRs: []string{"10.0.0.0/8"}, MinimumLevel: "Warning",
		AppliedProperties: map[string]any{"team": "a"}, RateLimitEPS: 100, AllowedStreams: []string{"app", "web"}}
	if err := r.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, &Connection{Name: "x", KeyHash: "hash1"}); !errors.Is(err, ErrConflict) {
		t.Errorf("dup hash: %v", err)
	}
	got, err := r.GetByKeyHash(ctx, "hash1", t0)
	if err != nil || got.Name != "checkout" || len(got.Protocols) != 2 || got.AppliedProperties["team"] != "a" ||
		got.AllowedCIDRs[0] != "10.0.0.0/8" || got.RateLimitEPS != 100 || got.MinimumLevel != "Warning" ||
		!slices.Equal(got.AllowedStreams, []string{"app", "web"}) {
		t.Fatalf("get: %+v %v", got, err)
	}
	grace := t0.Add(10 * time.Minute)
	if err := r.RotateKey(ctx, c.ID, "tmk_efgh", "hash2", &grace); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetByKeyHash(ctx, "hash1", t0.Add(5*time.Minute)); err != nil {
		t.Errorf("old key in grace: %v", err)
	}
	if _, err := r.GetByKeyHash(ctx, "hash1", t0.Add(11*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("old key after grace: %v", err)
	}
	if err := r.RotateKey(ctx, c.ID, "tmk_ijkl", "hash3", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetByKeyHash(ctx, "hash2", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("rotate without grace kept old key: %v", err)
	}
	got, _ = r.Get(ctx, c.ID)
	got.Description, got.Protocols, got.AppliedProperties, got.AllowedCIDRs, got.AllowedStreams = "d", nil, nil, nil, nil
	if err := r.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if err := r.AddUsage(ctx, c.ID, t0, "9.9.9.9", 7); err != nil {
		t.Fatal(err)
	}
	if err := r.AddUsage(ctx, c.ID, t0, "9.9.9.9", 3); err != nil {
		t.Fatal(err)
	}
	got, _ = r.GetByName(ctx, "checkout")
	if got.EventsReceived != 10 || got.LastUsedIP != "9.9.9.9" || got.Description != "d" || got.KeyPrefix != "tmk_ijkl" ||
		got.Protocols == nil || len(got.Protocols) != 0 || got.AllowedStreams == nil || len(got.AllowedStreams) != 0 {
		t.Errorf("after update: %+v", got)
	}
	if err := r.Revoke(ctx, c.ID, t0); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.Count(ctx); n != 1 {
		t.Errorf("count %d", n)
	}
	list, _ := r.List(ctx)
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Errorf("list %+v", list)
	}
	if err := r.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
}

func contractAPITokens(t *testing.T, open opener) {
	ctx := context.Background()
	db := open(t)
	users, r := NewUsers(db), NewAPITokens(db)
	u := &User{Username: "u", PasswordHash: "h", Role: RoleEditor}
	_ = users.Create(ctx, u)
	tok := &APIToken{Name: "ci", UserID: u.ID, KeyPrefix: "tmt_abcd", KeyHash: "th", Scope: ScopeRead}
	if err := r.Create(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, &APIToken{Name: "bad", UserID: u.ID, KeyHash: "x", Scope: "root"}); err == nil {
		t.Error("invalid scope accepted")
	}
	got, err := r.GetByHash(ctx, "th")
	if err != nil || got.Scope != ScopeRead {
		t.Fatalf("%+v %v", got, err)
	}
	_ = r.Touch(ctx, tok.ID, t0)
	if err := r.Revoke(ctx, tok.ID, t0); err != nil {
		t.Fatal(err)
	}
	_ = r.RevokeForUser(ctx, u.ID, t0)
	got, _ = r.Get(ctx, tok.ID)
	if got.RevokedAt == nil || got.LastUsedAt == nil {
		t.Errorf("%+v", got)
	}
	list, _ := r.ListForUser(ctx, u.ID)
	if len(list) != 1 {
		t.Errorf("list %d", len(list))
	}
	if err := r.Delete(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
}

func contractSignalsDashboardsChannels(t *testing.T, open opener) {
	ctx := context.Background()
	db := open(t)
	sig := NewSignals(db)
	s := &Signal{Name: "errors", TQL: "level:error", OwnerID: "u1", Source: "logs"}
	if err := sig.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	_ = sig.Create(ctx, &Signal{Name: "other", TQL: "*", OwnerID: "u2", Shared: false, Source: "logs"})
	vis, _ := sig.ListVisible(ctx, "u1")
	if len(vis) != 1 {
		t.Errorf("visible %d", len(vis))
	}
	s.Shared = true
	if err := sig.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := sig.Get(ctx, s.ID); !got.Shared {
		t.Error("shared not saved")
	}
	if err := sig.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}

	dash := NewDashboards(db)
	d := &Dashboard{Name: "d", OwnerID: "u1", Doc: json.RawMessage(`{"version":1}`)}
	if err := dash.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	stale := *d
	d.Name = "d2"
	if err := dash.Update(ctx, d); err != nil || d.Version != 2 {
		t.Fatalf("update %v v%d", err, d.Version)
	}
	if err := dash.Update(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Errorf("stale update: %v", err)
	}
	if err := dash.Update(ctx, &Dashboard{ID: "missing", Version: 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing update: %v", err)
	}
	if list, _ := dash.ListVisible(ctx, "u1"); len(list) != 1 || string(list[0].Doc) != `{"version":1}` {
		t.Errorf("list %+v", list)
	}
	_ = dash.Delete(ctx, d.ID)

	ch := NewChannels(db)
	c := &Channel{Name: "hook", Kind: "webhook", Config: json.RawMessage(`{"url":"enc"}`)}
	if err := ch.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := ch.Create(ctx, &Channel{Name: "bad", Kind: "pager"}); err == nil {
		t.Error("bad kind accepted")
	}
	if err := ch.Update(ctx, c); err != nil || c.Version != 2 {
		t.Errorf("update %v", err)
	}
	if list, _ := ch.List(ctx); len(list) != 1 {
		t.Error("list")
	}
	if err := ch.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
}

func contractAlertRules(t *testing.T, open opener) {
	ctx := context.Background()
	r := NewAlertRules(open(t))
	a := &AlertRule{Name: "spike", Enabled: true, Doc: json.RawMessage(`{"query":"*"}`)}
	if err := r.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	v := 42.0
	if string(a.EvalState) != "" {
		t.Errorf("eval state %q", a.EvalState)
	}
	a.State, a.LastValue, a.StateSince, a.LastEvalAt = "firing", &v, &t0, &t0
	a.EvalState = json.RawMessage(`{"groups":{}}`)
	if err := r.SaveState(ctx, *a); err != nil {
		t.Fatal(err)
	}
	if err := r.AddHistory(ctx, &AlertHistory{RuleID: a.ID, At: t0, FromState: "ok", ToState: "firing", Value: &v,
		GroupKey: `{"service":"api"}`}); err != nil {
		t.Fatal(err)
	}
	h, err := r.History(ctx, a.ID, 10)
	if err != nil || len(h) != 1 || *h[0].Value != 42 || h[0].GroupKey != `{"service":"api"}` {
		t.Fatalf("history %+v %v", h, err)
	}
	got, _ := r.Get(ctx, a.ID)
	if got.State != "firing" || *got.LastValue != 42 || string(got.EvalState) != `{"groups":{}}` || got.Streams != nil {
		t.Errorf("%+v", got)
	}
	got.Streams = []string{"api", "web"}
	if err := r.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.List(ctx); len(list) != 1 || !slices.Equal(list[0].Streams, []string{"api", "web"}) {
		t.Errorf("list %+v", list)
	}
	scoped := &AlertRule{Name: "scoped", Doc: json.RawMessage(`{}`), Streams: []string{}}
	if err := r.Create(ctx, scoped); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(ctx, scoped.ID); g.Streams == nil || len(g.Streams) != 0 {
		t.Errorf("an empty stream list must stay distinct from all streams: %#v", g.Streams)
	}
	if err := r.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
}

func contractStreamsIndexedPurge(t *testing.T, open opener) {
	ctx := context.Background()
	db := open(t)
	st := NewStreams(db)
	days := 90
	s := &Stream{Name: "api", RetentionDays: &days, LevelRules: []LevelRule{{Levels: []string{"Debug"}, Days: 3}}}
	if err := st.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &Stream{Name: "api"}); !errors.Is(err, ErrConflict) {
		t.Errorf("dup: %v", err)
	}
	got, err := st.GetByName(ctx, "api")
	if err != nil || *got.RetentionDays != 90 || got.LevelRules[0].Days != 3 || got.SpansRetentionDays != nil {
		t.Fatalf("%+v %v", got, err)
	}
	got.Paused, got.LevelRules = true, nil
	if err := st.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	list, _ := st.List(ctx)
	if len(list) != 1 || !list[0].Paused || list[0].LevelRules == nil {
		t.Errorf("%+v", list)
	}
	if err := st.Delete(ctx, "api"); err != nil {
		t.Fatal(err)
	}

	ix := NewIndexedFields(db)
	f := &IndexedField{Source: "logs", Key: "user.id", ColumnName: "f_user_id"}
	if err := ix.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := ix.Create(ctx, &IndexedField{Source: "logs", Key: "user.id", ColumnName: "f_other"}); !errors.Is(err, ErrConflict) {
		t.Errorf("dup: %v", err)
	}
	if g, err := ix.Get(ctx, f.ID); err != nil || g.ColumnName != "f_user_id" {
		t.Errorf("%+v %v", g, err)
	}
	if l, _ := ix.List(ctx); len(l) != 1 {
		t.Error("list")
	}
	_ = ix.Delete(ctx, f.ID)

	pj := NewPurgeJobs(db)
	j := &PurgeJob{Source: "logs", From: t0, To: t0.Add(time.Hour), Status: PurgePending, RequestedBy: "u1"}
	if err := pj.Create(ctx, j); err != nil {
		t.Fatal(err)
	}
	if l, err := pj.ListByStatus(ctx, PurgePending); err != nil || len(l) != 1 || l[0].ID != j.ID {
		t.Errorf("pending: %+v %v", l, err)
	}
	if err := pj.SetStatus(ctx, j.ID, PurgeDone, "mut_1"); err != nil {
		t.Fatal(err)
	}
	if g, err := pj.Get(ctx, j.ID); err != nil || g.Status != PurgeDone || g.MutationID != "mut_1" || !g.To.Equal(t0.Add(time.Hour)) {
		t.Errorf("%+v %v", g, err)
	}
}

func contractAuditAndSettings(t *testing.T, open opener) {
	ctx := context.Background()
	db := open(t)
	a := NewAuditLog(db)
	for i, action := range []string{"login", "logout", "login"} {
		e := &AuditEntry{At: t0.Add(time.Duration(i) * time.Minute), Actor: "u1", Action: action, Details: json.RawMessage(`{"i":1}`)}
		if err := a.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.List(ctx, AuditFilter{Action: "login", From: t0, To: t0.Add(time.Hour), Actor: "u1"})
	if err != nil || len(got) != 2 || !got[0].At.After(got[1].At) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE audit_log SET actor = 'x'`); err == nil {
		t.Error("audit log update allowed")
	}
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM audit_log`); err == nil {
		t.Error("audit log delete allowed")
	}

	s := NewSettings(db)
	if _, err := s.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := s.Set(ctx, "k", json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "k", json.RawMessage(`2`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "bad", json.RawMessage(`{`)); err == nil {
		t.Error("invalid json accepted")
	}
	if v, _ := s.Get(ctx, "k"); string(v) != "2" {
		t.Errorf("value %s", v)
	}
	all, _ := s.All(ctx)
	if len(all) != 1 {
		t.Errorf("all %v", all)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
}
