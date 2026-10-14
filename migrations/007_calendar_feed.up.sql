-- The household's calendar feed link. There is at most one row; only a
-- SHA-256 hash of the link's token is stored, so the link is shown once when
-- it is created and resetting it replaces the row.
CREATE TABLE calendar_feeds (
  id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  token_hash BYTEA NOT NULL CHECK (octet_length(token_hash) = 32),
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
