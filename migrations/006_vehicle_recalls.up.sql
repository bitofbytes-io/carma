-- NHTSA safety recall campaigns for each vehicle's model year, make, and model,
-- refreshed by the weekly recall check. A campaign covers a whole model year,
-- so dismissed_at lets the household hide one that is fixed or does not apply.
CREATE TABLE vehicle_recalls (
  vehicle_id UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
  campaign_number TEXT NOT NULL,
  component TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  consequence TEXT NOT NULL DEFAULT '',
  remedy TEXT NOT NULL DEFAULT '',
  report_received_on DATE,
  park_it BOOLEAN NOT NULL DEFAULT false,
  park_outside BOOLEAN NOT NULL DEFAULT false,
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  dismissed_at TIMESTAMPTZ,
  PRIMARY KEY (vehicle_id, campaign_number)
);

-- The last successful check per vehicle and the model year, make, and model it
-- covered, so editing any of them triggers a fresh check.
CREATE TABLE vehicle_recall_checks (
  vehicle_id UUID PRIMARY KEY REFERENCES vehicles(id) ON DELETE CASCADE,
  checked_at TIMESTAMPTZ NOT NULL,
  model_year INTEGER NOT NULL,
  make TEXT NOT NULL,
  model TEXT NOT NULL
);
