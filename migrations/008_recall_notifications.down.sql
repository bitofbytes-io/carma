DELETE FROM schema_migrations WHERE version = '008_recall_notifications';
DROP TABLE IF EXISTS recall_notifications;
