package notification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/store"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeStore embeds the nil Store interface: only the methods the notifier
// actually calls are implemented; anything else panics loudly in tests.
type fakeStore struct {
	store.Store
	run      *model.Run
	plan     *model.Plan
	settings *model.TelegramSettings
	err      error // when set, GetTelegramSettings returns it verbatim
}

func (f *fakeStore) GetRun(_ context.Context, id string) (*model.Run, error) {
	if f.run == nil || f.run.ID != id {
		return nil, store.ErrNotFound
	}
	r := *f.run
	return &r, nil
}

func (f *fakeStore) GetPlan(_ context.Context, id string) (*model.Plan, error) {
	if f.plan == nil || f.plan.ID != id {
		return nil, store.ErrNotFound
	}
	p := *f.plan
	return &p, nil
}

func (f *fakeStore) GetTelegramSettings(context.Context) (*model.TelegramSettings, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.settings == nil {
		return nil, store.ErrNotFound
	}
	s := *f.settings
	return &s, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type capturedRequest struct {
	req  *http.Request
	body sendMessageRequest
}

func newTestNotifier(st store.Store, rt roundTripFunc, publicURL string) *TelegramNotifier {
	t := NewTelegramNotifier(st, publicURL)
	t.client = &http.Client{Transport: rt}
	return t
}

// configuredStore returns a fake pre-loaded with a failed run, its plan, and
// valid Telegram settings.
func configuredStore() *fakeStore {
	return &fakeStore{
		run:  failedRun(),
		plan: failedPlan(),
		settings: &model.TelegramSettings{
			BotToken: "12345:SECRET-TOKEN",
			ChatID:   "-10099",
		},
	}
}

func failedRun() *model.Run {
	fin := time.Date(2026, 8, 22, 10, 1, 30, 0, time.UTC)
	sched := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	return &model.Run{
		ID:           "run-1",
		PlanID:       "plan-1",
		AgentID:      "agent-1",
		Operation:    model.OpBackup,
		Status:       model.RunFailed,
		QueuedAt:     sched,
		FinishedAt:   &fin,
		ErrorCode:    "restic_backup_failed",
		ErrorMessage: `source <gone> & "broken" > path`,
		ScheduledAt:  &sched,
	}
}

func failedPlan() *model.Plan {
	return &model.Plan{ID: "plan-1", Name: "夜间<备份>"}
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

func TestNotifyPlanFailureSendsSingleEscapedMessage(t *testing.T) {
	st := configuredStore()
	var captured []capturedRequest
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body sendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		captured = append(captured, capturedRequest{req: r, body: body})
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Header:     make(http.Header),
		}, nil
	})

	err := newTestNotifier(st, rt, "https://bmc.example.com/").NotifyPlanFailure(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected exactly 1 POST, got %d", len(captured))
	}

	req := captured[0].req
	if req.Method != http.MethodPost {
		t.Fatalf("method=%s, want POST", req.Method)
	}
	if req.URL.Host != "api.telegram.org" {
		t.Fatalf("host=%s, want api.telegram.org", req.URL.Host)
	}
	if req.URL.Path != "/bot12345:SECRET-TOKEN/sendMessage" {
		t.Fatalf("path=%s, want /bot<token>/sendMessage", req.URL.Path)
	}

	body := captured[0].body
	if body.ChatID != "-10099" {
		t.Fatalf("chat_id=%q, want -10099", body.ChatID)
	}
	if body.ParseMode != "HTML" {
		t.Fatalf("parse_mode=%q, want HTML", body.ParseMode)
	}
	if !body.DisableWebPagePreview {
		t.Fatal("disable_web_page_preview not set")
	}

	want := strings.Join([]string{
		"<b>备份计划执行失败</b>",
		"计划：夜间&lt;备份&gt;",
		"运行：run-1",
		"触发：定时",
		"Agent：agent-1",
		"错误：restic_backup_failed",
		"详情：source &lt;gone&gt; &amp; &#34;broken&#34; &gt; path",
		"完成时间：2026-08-22T10:01:30Z",
		"查看：https://bmc.example.com/runs/run-1",
	}, "\n")
	if body.Text != want {
		t.Fatalf("text mismatch:\ngot:  %q\nwant: %q", body.Text, want)
	}
}

func TestNotifyPlanFailureManualRunAndDefaults(t *testing.T) {
	run := failedRun()
	run.ScheduledAt = nil
	run.ErrorCode = ""
	run.ErrorMessage = ""
	run.FinishedAt = nil
	st0 := configuredStore()
	st0.run = run
	st := st0
	var texts []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body sendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		texts = append(texts, body.Text)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})

	if err := newTestNotifier(st, rt, "").NotifyPlanFailure(context.Background(), "run-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(texts) != 1 {
		t.Fatalf("expected 1 send, got %d", len(texts))
	}
	for _, want := range []string{"触发：手动", "错误：unknown", "详情：无详细信息", "完成时间：unknown"} {
		if !strings.Contains(texts[0], want) {
			t.Fatalf("text missing %q:\n%s", want, texts[0])
		}
	}
	if strings.Contains(texts[0], "查看：") {
		t.Fatalf("empty PublicURL must omit the detail link:\n%s", texts[0])
	}
}

// ---------------------------------------------------------------------------
// Filtering: only persisted plan-bound failed runs notify.
// ---------------------------------------------------------------------------

func TestNotifyPlanFailureSkipsNonQualifyingRuns(t *testing.T) {
	cases := []struct {
		name string
		run  *model.Run
	}{
		{"succeeded", func() *model.Run { r := failedRun(); r.Status = model.RunSucceeded; return r }()},
		{"cancelled", func() *model.Run { r := failedRun(); r.Status = model.RunCancelled; return r }()},
		{"system-run", func() *model.Run { r := failedRun(); r.PlanID = ""; return r }()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st0 := configuredStore()
			st0.run = c.run
			st := st0
			calls := 0
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			})
			if err := newTestNotifier(st, rt, "https://bmc.example.com").NotifyPlanFailure(context.Background(), "run-1"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if calls != 0 {
				t.Fatalf("expected 0 HTTP requests, got %d", calls)
			}
		})
	}
}

// Unconfigured (row missing) or half-cleared settings disable sending even
// for a qualifying failed run.
func TestNotifyPlanFailureSkipsWhenUnconfigured(t *testing.T) {
	cases := []struct {
		name     string
		settings *model.TelegramSettings
		err      error
	}{
		{name: "row-missing", settings: nil},
		{name: "empty-pair", settings: &model.TelegramSettings{}},
		{name: "settings-error", err: errors.New("db boom")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := configuredStore()
			st.settings = c.settings
			st.err = c.err
			calls := 0
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			})
			err := newTestNotifier(st, rt, "").NotifyPlanFailure(context.Background(), "run-1")
			if c.name == "settings-error" {
				if err == nil {
					t.Fatal("expected settings load error to surface")
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if calls != 0 {
				t.Fatalf("expected 0 HTTP requests, got %d", calls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fault tolerance: Telegram errors surface but never touch the stored run.
// ---------------------------------------------------------------------------

func TestNotifyPlanFailureTelegramErrorsDoNotMutateRun(t *testing.T) {
	st := configuredStore()

	t.Run("network error", func(t *testing.T) {
		rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp: connection refused")
		})
		err := newTestNotifier(st, rt, "").NotifyPlanFailure(context.Background(), "run-1")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("http 500 with token in transport error", func(t *testing.T) {
		rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: "https://api.telegram.org/bot12345:SECRET-TOKEN/sendMessage", Err: errors.New("boom")}
		})
		err := newTestNotifier(st, rt, "").NotifyPlanFailure(context.Background(), "run-1")
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "SECRET-TOKEN") {
			t.Fatalf("error leaks bot token: %v", err)
		}
	})

	t.Run("non-2xx body", func(t *testing.T) {
		rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 400,
				Body:       io.NopCloser(strings.NewReader(`{"ok":false,"description":"Bad Request: chat not found"}`)),
				Header:     make(http.Header),
			}, nil
		})
		err := newTestNotifier(st, rt, "").NotifyPlanFailure(context.Background(), "run-1")
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "chat not found") {
			t.Fatalf("error should include API description: %v", err)
		}
		if strings.Contains(err.Error(), "SECRET-TOKEN") {
			t.Fatalf("error leaks bot token: %v", err)
		}
	})

	// The stored run must be untouched by every failed send above.
	got, err := st.GetRun(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.RunFailed || got.ErrorCode != "restic_backup_failed" ||
		got.ErrorMessage != `source <gone> & "broken" > path` {
		t.Fatalf("stored run mutated: %+v", got)
	}
}

func TestNopNotifierIsNoop(t *testing.T) {
	if err := (NopNotifier{}).NotifyPlanFailure(context.Background(), "run-1"); err != nil {
		t.Fatalf("nop notifier returned error: %v", err)
	}
}
