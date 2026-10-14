DELETE FROM schema_migrations WHERE version = '005_reminder_digests';
DROP TABLE IF EXISTS reminder_digests;
