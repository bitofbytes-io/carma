DELETE FROM schema_migrations WHERE version = '008_recall_notifications';
DROP INDEX IF EXISTS vehicle_recalls_notified_digest_idx;
DROP INDEX IF EXISTS vehicle_recalls_unnotified_idx;
ALTER TABLE vehicle_recalls DROP COLUMN IF EXISTS notified_digest_id;
ALTER TABLE vehicle_recalls DROP COLUMN IF EXISTS notified_at;
