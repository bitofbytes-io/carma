CREATE TABLE reminder_digests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  week_start DATE NOT NULL UNIQUE,
  sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  recipients TEXT[] NOT NULL,
  message_id TEXT NOT NULL UNIQUE
);
