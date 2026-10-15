-- The household's recall dismissals, kept apart from vehicle_recalls, whose
-- rows each check deletes once NHTSA stops listing a campaign. A campaign that
-- is dropped and listed again stays dismissed instead of reappearing as open
-- and being emailed. vehicle_recalls.dismissed_at mirrors this table for the
-- campaigns currently listed; restoring a campaign deletes its row here.
CREATE TABLE recall_dismissals (
  vehicle_id UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
  campaign_number TEXT NOT NULL,
  dismissed_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (vehicle_id, campaign_number)
);

INSERT INTO recall_dismissals(vehicle_id, campaign_number, dismissed_at)
SELECT vehicle_id, campaign_number, dismissed_at FROM vehicle_recalls
WHERE dismissed_at IS NOT NULL;
