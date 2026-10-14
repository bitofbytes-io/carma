package repository

import (
	"context"
	"errors"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/jackc/pgx/v5"
)

func (p *Postgres) GetCalendarFeed(ctx context.Context) (*model.CalendarFeed, error) {
	var feed model.CalendarFeed
	err := p.pool.QueryRow(ctx, `SELECT token_hash,created_by,created_at FROM calendar_feeds WHERE id=1`).Scan(&feed.TokenHash, &feed.CreatedBy, &feed.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &feed, nil
}

func (p *Postgres) ReplaceCalendarFeed(ctx context.Context, feed model.CalendarFeed) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO calendar_feeds(id,token_hash,created_by,created_at) VALUES(1,$1,$2,$3)
		ON CONFLICT(id) DO UPDATE SET token_hash=excluded.token_hash,created_by=excluded.created_by,created_at=excluded.created_at`,
		feed.TokenHash, feed.CreatedBy, feed.CreatedAt.UTC())
	return err
}

func (p *Postgres) DeleteCalendarFeed(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM calendar_feeds`)
	return err
}
