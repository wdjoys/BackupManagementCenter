// Package notification sends plan-failure notifications to external
// channels. Currently Telegram is the only implementation; the narrow
// FailureNotifier interface keeps producers (agent results, scheduler,
// dispatcher watchdog, startup recovery) decoupled from the transport.
//
// Callers MUST invoke NotifyPlanFailure only after the failed terminal state
// has been persisted, and MUST treat a returned error as best-effort: the
// run's stored failure state never changes because a notification failed.
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/store"
)

// FailureNotifier reports an already-persisted failed run. The argument is
// the run ID; implementations re-read the run and apply their own filtering
// (plan-bound failures only) so every call site shares one rule.
type FailureNotifier interface {
	NotifyPlanFailure(ctx context.Context, runID string) error
}

// NopNotifier is used when notifications are not configured.
type NopNotifier struct{}

func (NopNotifier) NotifyPlanFailure(context.Context, string) error { return nil }

const telegramTimeout = 10 * time.Second

// TelegramNotifier sends failure messages via the official Telegram Bot API.
// Credentials are read from the store on every call, so web-UI changes take
// effect immediately and an unconfigured (or cleared) target silently
// disables sending. Safe for concurrent use.
type TelegramNotifier struct {
	st        store.Store
	publicURL string
	client    *http.Client
}

var _ FailureNotifier = (*TelegramNotifier)(nil)

// NewTelegramNotifier returns a notifier posting to api.telegram.org with a
// fixed 10s HTTP timeout. Whether anything is sent is decided per call from
// the stored Telegram settings.
func NewTelegramNotifier(st store.Store, publicURL string) *TelegramNotifier {
	return &TelegramNotifier{
		st:        st,
		publicURL: publicURL,
		client:    &http.Client{Timeout: telegramTimeout},
	}
}

// NotifyPlanFailure sends one message when runID refers to a persisted
// plan-bound failed run and Telegram is configured in the store. Non-plan
// (system) runs and runs that are not in the failed state are silently
// skipped. Errors never mutate the run.
func (t *TelegramNotifier) NotifyPlanFailure(ctx context.Context, runID string) error {
	run, err := t.st.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("telegram notify: get run %s: %w", runID, err)
	}
	if run.Status != model.RunFailed || run.PlanID == "" {
		return nil
	}
	plan, err := t.st.GetPlan(ctx, run.PlanID)
	if err != nil {
		return fmt.Errorf("telegram notify: get plan %s for run %s: %w", run.PlanID, runID, err)
	}
	settings, err := t.st.GetTelegramSettings(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil // not configured — notifications disabled
	}
	if err != nil {
		return fmt.Errorf("telegram notify: load settings for run %s: %w", runID, err)
	}
	if settings.BotToken == "" || settings.ChatID == "" {
		return nil // defensively treat an empty pair as disabled
	}
	return t.sendMessage(ctx, settings.BotToken, settings.ChatID, renderFailure(*run, *plan, t.publicURL))
}

type sendMessageRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

// sendMessage posts the payload with per-call credentials.
func (t *TelegramNotifier) sendMessage(ctx context.Context, botToken, chatID, text string) error {
	payload := sendMessageRequest{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram notify: encode request: %w", err)
	}
	endpoint := "https://api.telegram.org/bot" + botToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return scrub(botToken, fmt.Errorf("telegram notify: build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// url.Error embeds the full URL including the bot token — scrub it.
		return scrub(botToken, fmt.Errorf("telegram notify: send: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return scrub(botToken, fmt.Errorf("telegram notify: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet))))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

// scrub removes the bot token from an error string so callers may log it.
func scrub(botToken string, err error) error {
	if err == nil || botToken == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), botToken, "[redacted]")
	return errors.New(msg)
}

// renderFailure builds the fixed HTML message body. Every dynamic value is
// HTML-escaped; parse errors cannot occur because parse_mode is plain HTML
// with no nested entities.
func renderFailure(run model.Run, plan model.Plan, publicURL string) string {
	trigger := "手动"
	if run.ScheduledAt != nil {
		trigger = "定时"
	}
	errorCode := run.ErrorCode
	if errorCode == "" {
		errorCode = "unknown"
	}
	detail := run.ErrorMessage
	if detail == "" {
		detail = "无详细信息"
	}
	finished := "unknown"
	if run.FinishedAt != nil {
		finished = run.FinishedAt.UTC().Format(time.RFC3339)
	}

	lines := []string{
		"<b>备份计划执行失败</b>",
		"计划：" + html.EscapeString(plan.Name),
		"运行：" + html.EscapeString(run.ID),
		"触发：" + trigger,
		"Agent：" + html.EscapeString(run.AgentID),
		"错误：" + html.EscapeString(errorCode),
		"详情：" + html.EscapeString(detail),
		"完成时间：" + finished,
	}
	if publicURL != "" {
		lines = append(lines, "查看："+strings.TrimRight(publicURL, "/")+"/runs/"+url.PathEscape(run.ID))
	}
	return strings.Join(lines, "\n")
}

// LogFailure records a best-effort notification error without the token;
// shared by all call sites to keep log formatting identical.
func LogFailure(runID string, err error) {
	log.Printf("[ERROR] plan failure notification run=%s: %v", runID, err)
}
