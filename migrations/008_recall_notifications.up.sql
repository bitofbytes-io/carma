-- When the weekly digest first announced each recall campaign, and which
-- digest did, so a campaign is emailed once. A claimed digest marks its
-- campaigns before sending, and releasing an unsent claim clears them again.
ALTER TABLE vehicle_recalls ADD COLUMN notified_at TIMESTAMPTZ;
ALTER TABLE vehicle_recalls ADD COLUMN notified_digest_id UUID;

-- Campaigns found before recall emails existed are already on the vehicle
-- pages, so they count as announced rather than flooding the first digest.
UPDATE vehicle_recalls SET notified_at = first_seen_at;

CREATE INDEX vehicle_recalls_unnotified_idx
  ON vehicle_recalls(vehicle_id) WHERE notified_at IS NULL;
CREATE INDEX vehicle_recalls_notified_digest_idx
  ON vehicle_recalls(notified_digest_id) WHERE notified_digest_id IS NOT NULL;
