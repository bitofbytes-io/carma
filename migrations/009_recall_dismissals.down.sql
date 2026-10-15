DELETE FROM schema_migrations WHERE version = '009_recall_dismissals';
DROP TABLE IF EXISTS recall_dismissals;
