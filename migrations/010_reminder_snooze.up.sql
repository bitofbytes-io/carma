-- Defer a due reminder without disabling it. Either threshold may be set.
-- The reminder stays quiet until every set threshold has passed, and logging
-- a matching service record clears both.
ALTER TABLE reminders
  ADD COLUMN snoozed_until_on DATE,
  ADD COLUMN snoozed_until_miles BIGINT
    CONSTRAINT reminders_snoozed_until_miles_nonnegative
    CHECK (snoozed_until_miles IS NULL OR snoozed_until_miles >= 0);
