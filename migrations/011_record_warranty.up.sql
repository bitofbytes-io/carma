-- Optional warranty limits on a service record. Either limit may be set; the
-- warranty ends at whichever set limit passes first.
ALTER TABLE records
  ADD COLUMN warranty_until_date DATE,
  ADD COLUMN warranty_until_miles BIGINT
    CONSTRAINT records_warranty_until_miles_nonnegative
    CHECK (warranty_until_miles IS NULL OR warranty_until_miles >= 0);
