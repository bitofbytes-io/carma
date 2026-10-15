-- One row per recall campaign the weekly digest has announced for a vehicle,
-- so a campaign is emailed once. It is kept apart from vehicle_recalls, whose
-- rows are replaced on every check, so a campaign NHTSA drops and lists again
-- is not announced twice. A claimed digest records its campaigns before
-- sending (digest_id), and releasing an unsent claim deletes them again.
-- Baseline campaigns, found on a vehicle's first check, have no digest_id.
CREATE TABLE recall_notifications (
  vehicle_id UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
  campaign_number TEXT NOT NULL,
  notified_at TIMESTAMPTZ NOT NULL,
  digest_id UUID,
  PRIMARY KEY (vehicle_id, campaign_number)
);

CREATE INDEX recall_notifications_digest_id_idx
  ON recall_notifications(digest_id) WHERE digest_id IS NOT NULL;

-- Campaigns found before recall emails existed are already on the vehicle
-- pages, so they count as announced rather than flooding the first digest.
INSERT INTO recall_notifications(vehicle_id, campaign_number, notified_at)
SELECT vehicle_id, campaign_number, first_seen_at FROM vehicle_recalls;
