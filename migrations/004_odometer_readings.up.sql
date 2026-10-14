CREATE TABLE odometer_readings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  vehicle_id UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
  read_on DATE NOT NULL,
  odometer_miles BIGINT NOT NULL CHECK (odometer_miles >= 0),
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX odometer_readings_vehicle_read_on_idx
  ON odometer_readings(vehicle_id, read_on DESC);
CREATE INDEX odometer_readings_created_by_idx ON odometer_readings(created_by);
