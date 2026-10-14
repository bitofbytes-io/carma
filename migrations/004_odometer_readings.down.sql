DELETE FROM schema_migrations WHERE version = '004_odometer_readings';
DROP TABLE IF EXISTS odometer_readings;
