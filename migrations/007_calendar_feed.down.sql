DELETE FROM schema_migrations WHERE version = '007_calendar_feed';
DROP TABLE IF EXISTS calendar_feeds;
