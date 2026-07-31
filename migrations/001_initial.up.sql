CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  oauth_provider TEXT NOT NULL,
  oauth_subject TEXT NOT NULL,
  email TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_login_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (oauth_provider, oauth_subject)
);
CREATE UNIQUE INDEX users_email_ci_idx ON users(lower(email));

CREATE TABLE user_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  user_agent TEXT NOT NULL DEFAULT '',
  ip_address TEXT NOT NULL DEFAULT ''
);
CREATE INDEX user_sessions_expires_idx ON user_sessions(expires_at);

CREATE TABLE vehicles (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  nickname TEXT NOT NULL CHECK (btrim(nickname) <> ''),
  year INTEGER CHECK (year IS NULL OR year BETWEEN 1886 AND 9999),
  make TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', vin TEXT NOT NULL DEFAULT '',
  license_plate TEXT NOT NULL DEFAULT '', photo_key TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
  archived_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE service_types (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(), name TEXT NOT NULL, is_seeded BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX service_types_name_ci_idx ON service_types(lower(name));

INSERT INTO service_types(name, is_seeded) VALUES
 ('Oil change',true),('Tire rotation',true),('Engine air filter',true),('Cabin air filter',true),
 ('Brake pads',true),('Brake fluid',true),('Coolant',true),('Transmission fluid',true),
 ('Battery',true),('Wipers',true),('Registration/inspection',true),('Repair',true),('Other',true);

CREATE TABLE records (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(), vehicle_id UUID NOT NULL REFERENCES vehicles(id),
  service_type_id UUID NOT NULL REFERENCES service_types(id), occurred_on DATE NOT NULL,
  odometer_miles BIGINT CHECK (odometer_miles IS NULL OR odometer_miles >= 0),
  cost_cents BIGINT CHECK (cost_cents IS NULL OR cost_cents >= 0), vendor TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
  created_by UUID NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX records_vehicle_order_idx ON records(vehicle_id, occurred_on DESC, created_at DESC, id DESC);

CREATE TABLE attachments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(), record_id UUID NOT NULL REFERENCES records(id) ON DELETE CASCADE,
  original_filename TEXT NOT NULL, content_type TEXT NOT NULL, byte_size BIGINT NOT NULL CHECK(byte_size >= 0),
  storage_key TEXT NOT NULL UNIQUE, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE reminders (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(), vehicle_id UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
  service_type_id UUID NOT NULL REFERENCES service_types(id), interval_months INTEGER CHECK(interval_months IS NULL OR interval_months > 0),
  interval_miles BIGINT CHECK(interval_miles IS NULL OR interval_miles > 0), enabled BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK(interval_months IS NOT NULL OR interval_miles IS NOT NULL), UNIQUE(vehicle_id, service_type_id)
);
