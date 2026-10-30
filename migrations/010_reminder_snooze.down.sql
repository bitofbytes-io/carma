DELETE FROM schema_migrations WHERE version = '010_reminder_snooze';
ALTER TABLE reminders DROP COLUMN IF EXISTS snoozed_until_miles;
ALTER TABLE reminders DROP COLUMN IF EXISTS snoozed_until_on;
