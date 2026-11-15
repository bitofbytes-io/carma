DELETE FROM schema_migrations WHERE version = '012_vehicle_trim';
ALTER TABLE vehicle_recall_checks DROP COLUMN IF EXISTS match_version;
ALTER TABLE vehicle_recall_checks DROP COLUMN IF EXISTS vin;
ALTER TABLE vehicle_recall_checks DROP COLUMN IF EXISTS trim;
ALTER TABLE vehicles DROP COLUMN IF EXISTS trim;
