DELETE FROM schema_migrations WHERE version = '003_reminder_notifications';
DROP TABLE IF EXISTS reminder_notifications;
