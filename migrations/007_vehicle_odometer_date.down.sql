DELETE FROM schema_migrations WHERE version = '007_vehicle_odometer_date';
ALTER TABLE vehicles DROP COLUMN IF EXISTS current_odometer_on;
