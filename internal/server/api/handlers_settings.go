package api

import (
	"errors"
	"net/http"
	"strings"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/notification"
	"backupmanagementcenter/internal/server/store"
)

// telegramSettingsView is the web-facing shape; the bot token is write-only:
// we never return it back to the UI.
type telegramSettingsView struct {
	Configured bool   `json:"configured"`
	ChatID     string `json:"chat_id"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// GET /settings/telegram — returns whether Telegram notifications are
// configured and the (non-secret) chat ID; the bot token is never exposed.
func (s *Server) handleGetTelegramSettings(w http.ResponseWriter, r *http.Request) {
	ts, err := s.ST.GetTelegramSettings(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, telegramSettingsView{Configured: false})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, telegramSettingsView{
		Configured: true,
		ChatID:     ts.ChatID,
		UpdatedAt:  ts.UpdatedAt.Format(timeRFC3339),
	})
}

// PUT /api/telegram/settings — save or clear the Telegram bot configuration.
// An empty bot_token+chat_id clears (disables) notifications.
func (s *Server) handlePutTelegramSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	// Disable/clear when both are empty; must set both together otherwise.
	if body.BotToken == "" && body.ChatID == "" {
		if err := s.ST.DeleteTelegramSettings(r.Context()); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, telegramSettingsView{Configured: false})
		return
	}
	if body.BotToken == "" || body.ChatID == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "bot_token and chat_id must be set together")
		return
	}
	ts := &model.TelegramSettings{BotToken: body.BotToken, ChatID: body.ChatID}
	if err := s.ST.SaveTelegramSettings(r.Context(), ts); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, telegramSettingsView{
		Configured: true,
		ChatID:     ts.ChatID,
		UpdatedAt:  ts.UpdatedAt.Format(timeRFC3339),
	})
}

// POST /settings/telegram/test — sends a verification message with the
// supplied credentials, falling back to the saved pair when a field is
// empty. The token is never echoed back.
func (s *Server) handleTestTelegramSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	n := notification.NewTelegramNotifier(s.ST, s.PublicURL)
	if err := n.SendTest(r.Context(), strings.TrimSpace(body.BotToken), strings.TrimSpace(body.ChatID)); err != nil {
		writeErr(w, http.StatusBadGateway, "telegram_test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
