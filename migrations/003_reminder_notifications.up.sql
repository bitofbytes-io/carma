CREATE TABLE reminder_notifications (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  reminder_id UUID NOT NULL REFERENCES reminders(id) ON DELETE CASCADE,
  sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  recipients TEXT[] NOT NULL,
  message_id TEXT NOT NULL UNIQUE
);

CREATE INDEX reminder_notifications_reminder_sent_idx
  ON reminder_notifications(reminder_id, sent_at DESC);
