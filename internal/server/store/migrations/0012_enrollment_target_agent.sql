ALTER TABLE enrollment_tokens ADD COLUMN target_agent_id TEXT REFERENCES agents(id);
CREATE INDEX IF NOT EXISTS idx_enrollment_tokens_target ON enrollment_tokens(target_agent_id);
