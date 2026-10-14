-- One row per week claims that week's digest before it is sent, so a crash or
-- failed write after delivery can never cause a second send. sent_at and
-- message_id are filled in once the relay accepts the message.
CREATE TABLE reminder_digests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  week_start DATE NOT NULL UNIQUE,
  reserved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  recipients TEXT[] NOT NULL,
  sent_at TIMESTAMPTZ,
  message_id TEXT UNIQUE
);
