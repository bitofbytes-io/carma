package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const userCols = `id, oauth_provider, oauth_subject, email, display_name, avatar_url, created_at, updated_at, last_login_at`

const userUpsertAdvisoryLockID int64 = 0x4341524d41555352 // "CARMAUSR"

func scanUser(row pgx.Row) (*model.User, error) {
	var user model.User
	err := row.Scan(
		&user.ID, &user.OAuthProvider, &user.OAuthSubject,
		&user.Email, &user.DisplayName, &user.AvatarURL,
		&user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &user, err
}

func (p *Postgres) FindUserByOAuth(ctx context.Context, provider, subject string) (*model.User, error) {
	return scanUser(p.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE oauth_provider=$1 AND oauth_subject=$2`, provider, subject))
}

func (p *Postgres) FindUserByEmail(ctx context.Context, email string) (*model.User, error) {
	return scanUser(p.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(email)=lower($1)`, email))
}

// UpsertUser serializes the short identity-resolution transaction because the
// same logical user can conflict on either OAuth identity or normalized email.
func (p *Postgres) UpsertUser(ctx context.Context, user model.User) (model.User, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return user, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, userUpsertAdvisoryLockID); err != nil {
		return user, err
	}

	rows, err := tx.Query(
		ctx, `SELECT id, oauth_provider=$1 AND oauth_subject=$2, lower(email)=lower($3)
		FROM users
		WHERE (oauth_provider=$1 AND oauth_subject=$2) OR lower(email)=lower($3)
		FOR UPDATE`, user.OAuthProvider,
		user.OAuthSubject, user.Email,
	)
	if err != nil {
		return user, err
	}
	var identityID, emailID uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var identityMatch, emailMatch bool
		if err = rows.Scan(&id, &identityMatch, &emailMatch); err != nil {
			rows.Close()
			return user, err
		}
		if identityMatch {
			identityID = id
		}
		if emailMatch {
			emailID = id
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return user, err
	}
	// Like the memory store, refuse to move an identity onto an email address
	// that already belongs to a different user.
	if identityID != uuid.Nil && emailID != uuid.Nil && identityID != emailID {
		return user, ErrConflict
	}
	persistedID := identityID
	if persistedID == uuid.Nil {
		persistedID = emailID
	}
	switch {
	case persistedID != uuid.Nil:
		err = tx.QueryRow(
			ctx, `UPDATE users SET
			oauth_provider=$2,
			oauth_subject=$3,
			email=$4,
			display_name=$5,
			avatar_url=$6,
			updated_at=$7,
			last_login_at=$8
		WHERE id=$1
		RETURNING `+userCols,
			persistedID,
			user.OAuthProvider, user.OAuthSubject, user.Email,
			user.DisplayName, user.AvatarURL, user.UpdatedAt,
			user.LastLoginAt,
		).Scan(
			&user.ID, &user.OAuthProvider, &user.OAuthSubject,
			&user.Email, &user.DisplayName, &user.AvatarURL,
			&user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt,
		)
	default:
		err = tx.QueryRow(
			ctx, `INSERT INTO users(id,oauth_provider,oauth_subject,email,display_name,avatar_url,created_at,updated_at,
			last_login_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING `+userCols,
			user.ID,
			user.OAuthProvider, user.OAuthSubject, user.Email,
			user.DisplayName, user.AvatarURL, user.CreatedAt,
			user.UpdatedAt, user.LastLoginAt,
		).Scan(
			&user.ID, &user.OAuthProvider, &user.OAuthSubject,
			&user.Email, &user.DisplayName, &user.AvatarURL,
			&user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt,
		)
	}
	if err != nil {
		return user, err
	}
	if err = tx.Commit(ctx); err != nil {
		return user, err
	}
	return user, nil
}

func (p *Postgres) CreateSession(ctx context.Context, session model.Session, tokenHash string) error {
	_, err := p.pool.Exec(
		ctx, `INSERT INTO user_sessions(id,user_id,token_hash,expires_at,created_at,user_agent,ip_address)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, session.ID,
		session.UserID, tokenHash, session.ExpiresAt,
		session.CreatedAt, session.UserAgent, session.IPAddress,
	)
	return err
}

func (p *Postgres) FindSession(ctx context.Context, tokenHash string) (*model.Session, *model.User, error) {
	var session model.Session
	var user model.User
	err := p.pool.QueryRow(ctx, `SELECT s.id,s.user_id,s.expires_at,s.created_at,s.user_agent,s.ip_address,u.`+strings.ReplaceAll(userCols, ", ", ",u.")+` FROM user_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1`, tokenHash).Scan(
		&session.ID, &session.UserID, &session.ExpiresAt,
		&session.CreatedAt, &session.UserAgent, &session.IPAddress,
		&user.ID, &user.OAuthProvider, &user.OAuthSubject,
		&user.Email, &user.DisplayName, &user.AvatarURL,
		&user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &session, &user, nil
}

func (p *Postgres) DeleteSession(ctx context.Context, id uuid.UUID) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM user_sessions WHERE id=$1`, id)
	return err
}

func (p *Postgres) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM user_sessions WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
