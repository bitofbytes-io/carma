DELETE FROM schema_migrations WHERE version = '011_record_warranty';
ALTER TABLE records DROP COLUMN IF EXISTS warranty_until_miles;
ALTER TABLE records DROP COLUMN IF EXISTS warranty_until_date;
