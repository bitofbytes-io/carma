DELETE FROM schema_migrations WHERE version = '001_initial';
DROP TABLE IF EXISTS reminders, attachments, records, service_types, vehicles, user_sessions, users CASCADE;
