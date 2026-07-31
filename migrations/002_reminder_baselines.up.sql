ALTER TABLE vehicles
  ADD COLUMN current_odometer_miles BIGINT
  CONSTRAINT vehicles_current_odometer_nonnegative
  CHECK (current_odometer_miles IS NULL OR current_odometer_miles >= 0);

UPDATE vehicles v
SET current_odometer_miles = (
  SELECT max(r.odometer_miles)
  FROM records r
  WHERE r.vehicle_id = v.id
);

ALTER TABLE reminders
  ADD COLUMN starting_odometer_miles BIGINT
  CONSTRAINT reminders_starting_odometer_nonnegative
  CHECK (starting_odometer_miles IS NULL OR starting_odometer_miles >= 0);

UPDATE reminders rm
SET starting_odometer_miles = v.current_odometer_miles
FROM vehicles v
WHERE v.id = rm.vehicle_id;
