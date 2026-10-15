-- The date the vehicle's current mileage was true, so it can feed the mileage
-- projection as a dated point like an odometer reading. Existing mileage is
-- dated by the reading or record that set it, else by the vehicle's last edit
-- in the household time zone, which carma-migrate sets from APP_TIMEZONE.
ALTER TABLE vehicles ADD COLUMN current_odometer_on DATE;

UPDATE vehicles v
SET current_odometer_on = COALESCE(
  (SELECT max(o.read_on) FROM odometer_readings o
    WHERE o.vehicle_id = v.id AND o.odometer_miles = v.current_odometer_miles),
  (SELECT max(r.occurred_on) FROM records r
    WHERE r.vehicle_id = v.id AND r.odometer_miles = v.current_odometer_miles),
  (v.updated_at AT TIME ZONE current_setting('carma.household_timezone'))::date
)
WHERE v.current_odometer_miles IS NOT NULL;
