DELETE FROM schema_migrations WHERE version = '002_reminder_baselines';
ALTER TABLE reminders DROP COLUMN IF EXISTS starting_odometer_miles;
ALTER TABLE vehicles DROP COLUMN IF EXISTS current_odometer_miles;
