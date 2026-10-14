DELETE FROM schema_migrations WHERE version = '006_vehicle_recalls';
DROP TABLE IF EXISTS vehicle_recall_checks;
DROP TABLE IF EXISTS vehicle_recalls;
