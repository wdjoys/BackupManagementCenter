package config

import "testing"

func setTelegramEnv(t *testing.T, token, chatID string) {
	t.Helper()
	t.Setenv("BMC_TELEGRAM_BOT_TOKEN", token)
	t.Setenv("BMC_TELEGRAM_CHAT_ID", chatID)
	// Skip TLS/master-key requirements so only the Telegram rules are under
	// test.
	t.Setenv("BMC_DEV_INSECURE", "1")
}

func TestLoadServerTelegramBothEmptyDisables(t *testing.T) {
	setTelegramEnv(t, "", "")

	c, err := LoadServer()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.TelegramBotToken != "" || c.TelegramChatID != "" {
		t.Fatalf("expected notifications disabled, got token=%q chat=%q", c.TelegramBotToken, c.TelegramChatID)
	}
}

func TestLoadServerTelegramTokenOnlyErrors(t *testing.T) {
	setTelegramEnv(t, "12345:secret", "")

	_, err := LoadServer()
	if err == nil {
		t.Fatal("expected error for token-only configuration")
	}
}

func TestLoadServerTelegramChatOnlyErrors(t *testing.T) {
	setTelegramEnv(t, "", "-10099")

	_, err := LoadServer()
	if err == nil {
		t.Fatal("expected error for chat-id-only configuration")
	}
}

func TestLoadServerTelegramBothSetEnables(t *testing.T) {
	setTelegramEnv(t, "12345:secret", "-10099")

	c, err := LoadServer()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.TelegramBotToken != "12345:secret" || c.TelegramChatID != "-10099" {
		t.Fatalf("expected both values loaded, got token=%q chat=%q", c.TelegramBotToken, c.TelegramChatID)
	}
}

func TestLoadServerTelegramRulesApplyInProductionModeToo(t *testing.T) {
	setTelegramEnv(t, "12345:secret", "")
	t.Setenv("BMC_DEV_INSECURE", "")
	t.Setenv("BMC_TLS_MODE", "none")
	t.Setenv("BMC_MASTER_KEY_FILE", t.TempDir()+"/master.key")

	// Production mode: TLS/master key satisfied, but half-configured Telegram
	// must still be a config error.
	if _, err := LoadServer(); err == nil {
		t.Fatal("expected telegram half-configuration error in production mode")
	}
}
