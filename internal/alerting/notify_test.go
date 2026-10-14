// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/httpx"
	"github.com/ZeraOrg/tidemark/internal/meta"
)

type mapResolver map[string]meta.Channel

func (m mapResolver) Resolve(_ context.Context, id string) (meta.Channel, error) {
	c, ok := m[id]
	if !ok {
		return meta.Channel{}, meta.ErrNotFound
	}
	return c, nil
}

// flakyNotifier fails the first n calls with err.
type flakyNotifier struct {
	mu    sync.Mutex
	fails int
	err   error
	calls int
	got   []Notification
}

func (f *flakyNotifier) Kind() string { return KindWebhook }

func (f *flakyNotifier) Notify(_ context.Context, _ meta.Channel, n Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.fails {
		return f.err
	}
	f.got = append(f.got, n)
	return nil
}

func (f *flakyNotifier) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.got) }

func cfgJSON(c ChannelConfig) json.RawMessage { b, _ := json.Marshal(c); return b }

func newTestDispatcher(n Notifier) (*Dispatcher, *[]time.Duration) {
	var slept []time.Duration
	d := NewDispatcher(mapResolver{"c": {ID: "c", Kind: KindWebhook}, "x": {ID: "x", Kind: "sms"}}, quiet(),
		func() time.Time { return t0 }, n).
		WithSleep(func(_ context.Context, dur time.Duration) error { slept = append(slept, dur); return nil })
	return d, &slept
}

func TestDispatcherRetries(t *testing.T) {
	tests := []struct {
		name      string
		fails     int
		err       error
		wantErr   bool
		wantCalls int
		wantSleep []time.Duration
	}{
		{"ok", 0, nil, false, 1, nil},
		{"two transient", 2, errors.New("503"), false, 3, []time.Duration{time.Second, 2 * time.Second}},
		{"exhausted", 10, errors.New("503"), true, 1 + MaxRetries, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}},
		{"permanent", 10, &PermanentError{Err: errors.New("400")}, true, 1, nil},
	}
	for _, tc := range tests {
		n := &flakyNotifier{fails: tc.fails, err: tc.err}
		d, slept := newTestDispatcher(n)
		err := d.Deliver(context.Background(), "c", Notification{RuleID: "r"})
		if (err != nil) != tc.wantErr || n.calls != tc.wantCalls || len(*slept) != len(tc.wantSleep) {
			t.Errorf("%s: err %v calls %d slept %v", tc.name, err, n.calls, *slept)
			continue
		}
		for i, s := range tc.wantSleep {
			if (*slept)[i] != s {
				t.Errorf("%s: backoff %v", tc.name, *slept)
			}
		}
	}
	d, _ := newTestDispatcher(&flakyNotifier{})
	if err := d.Deliver(context.Background(), "missing", Notification{}); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing channel: %v", err)
	}
	if err := d.Deliver(context.Background(), "x", Notification{}); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("unknown kind: %v", err)
	}
	if err := d.Test(context.Background(), meta.Channel{Kind: "sms"}, ""); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("test unknown kind: %v", err)
	}
	cancelled := NewDispatcher(mapResolver{"c": {Kind: KindWebhook}}, quiet(), time.Now, &flakyNotifier{fails: 5, err: errors.New("x")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cancelled.Deliver(ctx, "c", Notification{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled backoff: %v", err)
	}
}

func TestDispatcherQueueAndDedupe(t *testing.T) {
	n := &flakyNotifier{}
	d, _ := newTestDispatcher(n)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	note := Notification{RuleID: "r", State: "firing", Group: map[string]string{"s": "a"}}
	d.Enqueue(note, []string{"c"})
	d.Enqueue(note, []string{"c"}) // duplicate within a minute
	other := note
	other.State = "ok"
	d.Enqueue(other, []string{"c"})
	deadline := time.Now().Add(2 * time.Second)
	for n.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if n.count() != 2 {
		t.Errorf("delivered %d", n.count())
	}
	for i := range dedupeSweepAfter + 1 {
		d.duplicate(Notification{RuleID: strconv.Itoa(i)})
	}
	d.now = func() time.Time { return t0.Add(2 * dedupeWindow) }
	if d.duplicate(note) {
		t.Error("window expired")
	}
	if len(d.recent) > 2 {
		t.Errorf("sweep left %d", len(d.recent))
	}
	full := NewDispatcher(mapResolver{}, quiet(), time.Now)
	for i := range queueSize + 5 {
		full.Enqueue(Notification{RuleID: strconv.Itoa(i)}, []string{"c"})
	}
	if len(full.queue) != queueSize {
		t.Errorf("queue %d", len(full.queue))
	}
}

func sampleNotification() Notification {
	return Notification{RuleID: "r1", RuleName: "Checkout <errors>", Severity: SeverityCritical, Description: "spike & more",
		State: "firing", PreviousState: "pending", Value: fptr(31), Op: ">", Threshold: 25,
		Group: map[string]string{"service": "checkout"}, At: t0, URL: "https://tm.example.com/alerts/r1"}
}

func TestWebhookNotifier(t *testing.T) {
	var got struct {
		body []byte
		hdr  http.Header
	}
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.body, _ = io.ReadAll(r.Body)
		got.hdr = r.Header.Clone()
		w.WriteHeader(status)
	}))
	defer srv.Close()
	client := httpx.SafeClient(true, SendTimeout)
	wh := Webhook{Client: client}
	ch := meta.Channel{Kind: KindWebhook, Config: cfgJSON(ChannelConfig{URL: srv.URL, Secret: "s3cret",
		Headers: map[string]string{"Authorization": "Bearer abc"}})}
	if err := wh.Notify(context.Background(), ch, sampleNotification()); err != nil {
		t.Fatal(err)
	}
	var p WebhookPayload
	if err := json.Unmarshal(got.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Rule.ID != "r1" || p.Rule.Severity != "critical" || p.State != "firing" || p.PreviousState != "pending" ||
		*p.Value != 31 || p.Threshold != 25 || p.Group["service"] != "checkout" || !p.At.Equal(t0) || p.URL == "" {
		t.Errorf("payload %+v", p)
	}
	if got.hdr.Get(SignatureHeader) != Sign("s3cret", got.body) || got.hdr.Get("Authorization") != "Bearer abc" ||
		got.hdr.Get("Content-Type") != "application/json" || got.hdr.Get("User-Agent") != UserAgent {
		t.Errorf("headers %v", got.hdr)
	}
	if !strings.HasPrefix(Sign("k", []byte("b")), "sha256=") || len(Sign("k", []byte("b"))) != 7+64 {
		t.Error("signature format")
	}
	for _, tc := range []struct {
		status    int
		permanent bool
	}{{500, false}, {429, false}, {408, false}, {404, true}, {400, true}} {
		status = tc.status
		err := wh.Notify(context.Background(), ch, sampleNotification())
		var perm *PermanentError
		if err == nil || errors.As(err, &perm) != tc.permanent {
			t.Errorf("status %d: %v", tc.status, err)
		}
	}
	status = http.StatusOK
	plain := meta.Channel{Kind: KindWebhook, Config: cfgJSON(ChannelConfig{URL: srv.URL})}
	if err := wh.Notify(context.Background(), plain, Notification{RuleID: "x"}); err != nil || got.hdr.Get(SignatureHeader) != "" ||
		!strings.Contains(string(got.body), `"group":{}`) {
		t.Errorf("unsigned: %v %v %s", err, got.hdr, got.body)
	}
	if err := wh.Notify(context.Background(), meta.Channel{Config: json.RawMessage(`{"nope":1}`)}, Notification{}); err == nil {
		t.Error("bad config accepted")
	}
}

// TestNotifiersUseSSRFSafeClient checks that loopback targets are refused by the default
// (private destinations not allowed) client at dial time.
func TestNotifiersUseSSRFSafeClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	client := httpx.SafeClient(false, SendTimeout)
	ch := meta.Channel{Config: cfgJSON(ChannelConfig{URL: srv.URL})}
	for _, n := range []Notifier{Webhook{Client: client}, Slack{Client: client}, Teams{Client: client}} {
		err := n.Notify(context.Background(), ch, sampleNotification())
		if !errors.Is(err, httpx.ErrBlockedAddress) || strings.Contains(err.Error(), srv.URL) {
			t.Errorf("%s: %v", n.Kind(), err)
		}
	}
}

func TestSlackAndTeamsPayloads(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
	}))
	defer srv.Close()
	client := httpx.SafeClient(true, SendTimeout)
	ch := meta.Channel{Config: cfgJSON(ChannelConfig{URL: srv.URL})}
	if err := (Slack{Client: client}).Notify(context.Background(), ch, sampleNotification()); err != nil {
		t.Fatal(err)
	}
	if err := (Teams{Client: client}).Notify(context.Background(), ch, sampleNotification()); err != nil {
		t.Fatal(err)
	}
	slack, teams := string(bodies[0]), string(bodies[1])
	for _, want := range []string{`"type":"header"`, `"type":"section"`, `[FIRING] Checkout \u003cerrors\u003e`, `spike \u0026amp; more`,
		`"type":"button"`, `"url":"https://tm.example.com/alerts/r1"`, `*Value*\n31`} {
		if !strings.Contains(slack, want) {
			t.Errorf("slack missing %s: %s", want, slack)
		}
	}
	for _, want := range []string{`"contentType":"application/vnd.microsoft.card.adaptive"`, `"type":"AdaptiveCard"`,
		`"version":"1.4"`, `"type":"FactSet"`, `"title":"Condition","value":"\u003e 25"`, `"type":"Action.OpenUrl"`} {
		if !strings.Contains(teams, want) {
			t.Errorf("teams missing %s: %s", want, teams)
		}
	}
	bare := Notification{RuleName: strings.Repeat("x", 200), State: "ok", PreviousState: "firing", At: t0}
	msg := SlackMessage(bare)
	if len(msg["blocks"].([]map[string]any)) != 2 {
		t.Errorf("minimal slack %v", msg)
	}
	if _, ok := TeamsMessage(bare)["attachments"].([]map[string]any)[0]["content"].(map[string]any)["actions"]; ok {
		t.Error("teams actions without url")
	}
	for _, n := range []Notifier{Slack{Client: client}, Teams{Client: client}} {
		if err := n.Notify(context.Background(), meta.Channel{Config: json.RawMessage(`x`)}, bare); err == nil {
			t.Errorf("%s: bad config accepted", n.Kind())
		}
	}
}

func TestDispatcherTestDelivery(t *testing.T) {
	n := &flakyNotifier{}
	d, _ := newTestDispatcher(n)
	if err := d.Test(context.Background(), meta.Channel{Kind: KindWebhook}, "https://tm"); err != nil {
		t.Fatal(err)
	}
	if n.got[0].State != stateTestDelivery || n.got[0].RuleID != testRuleID || n.got[0].URL != "https://tm" {
		t.Errorf("%+v", n.got[0])
	}
}

// smtpServer is a minimal SMTP server for tests; it records the transaction.
type smtpServer struct {
	ln       net.Listener
	startTLS bool
	mu       sync.Mutex
	from     string
	rcpts    []string
	data     string
	authSeen bool
}

func newSMTPServer(t *testing.T, startTLS bool) *smtpServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpServer{ln: ln, startTLS: startTLS}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *smtpServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *smtpServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *smtpServer) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	reply := func(line string) { _, _ = io.WriteString(c, line+"\r\n") }
	reply("220 test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if s.startTLS {
				reply("250-test")
				reply("250-AUTH PLAIN")
				reply("250 STARTTLS")
			} else {
				reply("250-test")
				reply("250 AUTH PLAIN")
			}
		case strings.HasPrefix(cmd, "AUTH"):
			s.mu.Lock()
			s.authSeen = true
			s.mu.Unlock()
			reply("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			s.mu.Lock()
			s.from = strings.TrimSpace(line[len("MAIL FROM:"):])
			s.mu.Unlock()
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			s.mu.Lock()
			s.rcpts = append(s.rcpts, strings.TrimSpace(line[len("RCPT TO:"):]))
			s.mu.Unlock()
			reply("250 ok")
		case cmd == "DATA":
			reply("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func TestEmailNotifier(t *testing.T) {
	srv := newSMTPServer(t, false)
	e := Email{SMTP: SMTPConfig{Host: "127.0.0.1", Port: srv.port(), From: "tidemark@example.com"},
		Now: func() time.Time { return t0 }}
	ch := meta.Channel{Kind: KindEmail, Config: cfgJSON(ChannelConfig{To: []string{"ops@example.com", "dev@example.com"}})}
	n := sampleNotification()
	n.RuleName = "Checkout\r\nBcc: evil@example.com"
	if err := e.Notify(context.Background(), ch, n); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.from != "<tidemark@example.com>" || len(srv.rcpts) != 2 {
		t.Errorf("envelope %s %v", srv.from, srv.rcpts)
	}
	for _, want := range []string{"Subject: [FIRING] Checkout  Bcc: evil@example.com", "multipart/alternative",
		"Content-Type: text/plain; charset=utf-8", "Content-Type: text/html; charset=utf-8", "Severity: critical",
		"Date: Tue, 13 Oct 2026 12:00:00 +0000", "&gt; 25", "Open in OpenTidemark"} {
		if !strings.Contains(srv.data, want) {
			t.Errorf("message missing %q:\n%s", want, srv.data)
		}
	}
	headers, _, _ := strings.Cut(srv.data, "\r\n\r\n")
	if strings.Contains(headers, "\r\nBcc:") {
		t.Errorf("header injection:\n%s", headers)
	}
}

func TestEmailNotifierErrors(t *testing.T) {
	noTLS := newSMTPServer(t, false)
	required := Email{SMTP: SMTPConfig{Host: "127.0.0.1", Port: noTLS.port(), From: "a@b.c", StartTLS: true}}
	ch := meta.Channel{Config: cfgJSON(ChannelConfig{To: []string{"ops@example.com"}})}
	var perm *PermanentError
	if err := required.Notify(context.Background(), ch, sampleNotification()); !errors.Is(err, ErrStartTLSUnavailable) || !errors.As(err, &perm) {
		t.Errorf("starttls required: %v", err)
	}
	authNoTLS := Email{SMTP: SMTPConfig{Host: "127.0.0.1", Port: noTLS.port(), From: "a@b.c", Username: "u", Password: "p"}}
	if err := authNoTLS.Notify(context.Background(), ch, sampleNotification()); err != nil {
		t.Errorf("plain auth to localhost: %v", err)
	}
	if err := (Email{}).Notify(context.Background(), ch, sampleNotification()); !errors.As(err, &perm) {
		t.Errorf("unconfigured: %v", err)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	port := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	down := Email{SMTP: SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@b.c"}}
	if err := down.Notify(context.Background(), ch, sampleNotification()); err == nil || errors.As(err, &perm) {
		t.Errorf("dial failure must be retryable: %v", err)
	}
	if err := down.Notify(context.Background(), meta.Channel{Config: json.RawMessage(`[]`)}, sampleNotification()); err == nil {
		t.Error("bad config accepted")
	}
	if !(SMTPConfig{Host: "h", From: "f"}).Configured() || (SMTPConfig{Host: "h"}).Configured() {
		t.Error("configured")
	}
}
