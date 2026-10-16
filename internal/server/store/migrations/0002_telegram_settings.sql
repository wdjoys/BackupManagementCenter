-- 0002_telegram_settings.sql — single-row table for Telegram failure
-- notification settings configured from the web UI. The bot token is stored
-- sealed (AAD "telegram_settings:1:encrypted_token"); an absent row means
-- notifications are disabled.

CREATE TABLE IF NOT EXISTS telegram_settings (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    encrypted_token BLOB NOT NULL,
    chat_id         TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
