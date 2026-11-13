-- The trim vPIC decodes from the VIN, such as "E450 4MATIC". NHTSA files some
-- recalls under trim names rather than the model family, so the recall check
-- queries it too.
ALTER TABLE vehicles ADD COLUMN trim TEXT NOT NULL DEFAULT '';

-- The trim and matching version a vehicle's last recall check used. Editing the
-- trim, or improving how models are matched (a higher version), triggers a
-- fresh check; existing rows start at version 0 so they are rechecked once.
ALTER TABLE vehicle_recall_checks
  ADD COLUMN trim TEXT NOT NULL DEFAULT '',
  ADD COLUMN match_version INTEGER NOT NULL DEFAULT 0;
