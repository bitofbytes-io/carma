package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(ctx context.Context, url string) (*Postgres, error) {
	p, e := pgxpool.New(ctx, url)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return &Postgres{pool: p}, nil
}
func (p *Postgres) Close() { p.pool.Close() }

const userCols = `id, oauth_provider, oauth_subject, email, display_name, avatar_url, created_at, updated_at, last_login_at`
const userUpsertAdvisoryLockID int64 = 0x4341524d41555352 // "CARMAUSR"

func scanUser(row pgx.Row) (*model.User, error) {
	var u model.User
	e := row.Scan(&u.ID, &u.OAuthProvider, &u.OAuthSubject, &u.Email, &u.DisplayName, &u.AvatarURL, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	return &u, e
}
func (p *Postgres) FindUserByOAuth(c context.Context, provider, sub string) (*model.User, error) {
	return scanUser(p.pool.QueryRow(c, `SELECT `+userCols+` FROM users WHERE oauth_provider=$1 AND oauth_subject=$2`, provider, sub))
}
func (p *Postgres) FindUserByEmail(c context.Context, email string) (*model.User, error) {
	return scanUser(p.pool.QueryRow(c, `SELECT `+userCols+` FROM users WHERE lower(email)=lower($1)`, email))
}

// UpsertUser serializes the short identity-resolution transaction because the
// same logical user can conflict on either OAuth identity or normalized email.
func (p *Postgres) UpsertUser(c context.Context, u model.User) (model.User, error) {
	tx, e := p.pool.Begin(c)
	if e != nil {
		return u, e
	}
	defer func() { _ = tx.Rollback(c) }()
	if _, e = tx.Exec(c, `SELECT pg_advisory_xact_lock($1)`, userUpsertAdvisoryLockID); e != nil {
		return u, e
	}

	var persistedID uuid.UUID
	e = tx.QueryRow(c, `SELECT id FROM users
		WHERE (oauth_provider=$1 AND oauth_subject=$2) OR lower(email)=lower($3)
		ORDER BY (oauth_provider=$1 AND oauth_subject=$2) DESC
		LIMIT 1 FOR UPDATE`, u.OAuthProvider, u.OAuthSubject, u.Email).Scan(&persistedID)
	switch {
	case e == nil:
		e = tx.QueryRow(c, `UPDATE users SET
			oauth_provider=$2,
			oauth_subject=$3,
			email=$4,
			display_name=$5,
			avatar_url=$6,
			updated_at=$7,
			last_login_at=$8
			WHERE id=$1 RETURNING `+userCols,
			persistedID, u.OAuthProvider, u.OAuthSubject, u.Email, u.DisplayName, u.AvatarURL, u.UpdatedAt, u.LastLoginAt,
		).Scan(&u.ID, &u.OAuthProvider, &u.OAuthSubject, &u.Email, &u.DisplayName, &u.AvatarURL, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	case errors.Is(e, pgx.ErrNoRows):
		e = tx.QueryRow(c, `INSERT INTO users(id,oauth_provider,oauth_subject,email,display_name,avatar_url,created_at,updated_at,last_login_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+userCols,
			u.ID, u.OAuthProvider, u.OAuthSubject, u.Email, u.DisplayName, u.AvatarURL, u.CreatedAt, u.UpdatedAt, u.LastLoginAt,
		).Scan(&u.ID, &u.OAuthProvider, &u.OAuthSubject, &u.Email, &u.DisplayName, &u.AvatarURL, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	default:
		return u, e
	}
	if e != nil {
		return u, e
	}
	if e = tx.Commit(c); e != nil {
		return u, e
	}
	return u, nil
}
func (p *Postgres) CreateSession(c context.Context, s model.Session, h string) error {
	_, e := p.pool.Exec(c, `INSERT INTO user_sessions(id,user_id,token_hash,expires_at,created_at,user_agent,ip_address) VALUES($1,$2,$3,$4,$5,$6,$7)`, s.ID, s.UserID, h, s.ExpiresAt, s.CreatedAt, s.UserAgent, s.IPAddress)
	return e
}
func (p *Postgres) FindSession(c context.Context, h string) (*model.Session, *model.User, error) {
	var s model.Session
	var u model.User
	e := p.pool.QueryRow(c, `SELECT s.id,s.user_id,s.expires_at,s.created_at,s.user_agent,s.ip_address,u.`+strings.ReplaceAll(userCols, ", ", ",u.")+` FROM user_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1`, h).Scan(&s.ID, &s.UserID, &s.ExpiresAt, &s.CreatedAt, &s.UserAgent, &s.IPAddress, &u.ID, &u.OAuthProvider, &u.OAuthSubject, &u.Email, &u.DisplayName, &u.AvatarURL, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if e != nil {
		return nil, nil, e
	}
	return &s, &u, nil
}
func (p *Postgres) DeleteSession(c context.Context, id uuid.UUID) error {
	_, e := p.pool.Exec(c, `DELETE FROM user_sessions WHERE id=$1`, id)
	return e
}

const vehicleCols = `v.id,v.nickname,v.year,v.make,v.model,v.vin,v.license_plate,v.photo_key,v.notes,v.archived_at,v.created_at,v.updated_at,v.current_odometer_miles`
const initializeReminderOdometerSQL = `UPDATE reminders SET starting_odometer_miles=GREATEST($2::bigint,(SELECT max(odometer_miles) FROM records WHERE vehicle_id=$1)) WHERE vehicle_id=$1 AND starting_odometer_miles IS NULL`

func scanVehicle(row pgx.Row) (model.Vehicle, error) {
	var v model.Vehicle
	e := row.Scan(&v.ID, &v.Nickname, &v.Year, &v.Make, &v.Model, &v.VIN, &v.LicensePlate, &v.PhotoKey, &v.Notes, &v.ArchivedAt, &v.CreatedAt, &v.UpdatedAt, &v.CurrentOdometer, &v.LatestOdometer)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return v, e
}
func (p *Postgres) ListVehicles(c context.Context, archived bool) ([]model.Vehicle, error) {
	rows, e := p.pool.Query(c, `SELECT `+vehicleCols+`,
		GREATEST(v.current_odometer_miles,(SELECT max(odometer_miles) FROM records WHERE vehicle_id=v.id)),
		lr.id,lr.vehicle_id,lr.service_type_id,lr.created_by,lr.occurred_on,lr.odometer_miles,lr.cost_cents,lr.vendor,lr.notes,lr.created_at,lr.updated_at,lr.service_type_name
		FROM vehicles v
		LEFT JOIN LATERAL (
			SELECT r.id,r.vehicle_id,r.service_type_id,r.created_by,r.occurred_on,r.odometer_miles,r.cost_cents,r.vendor,r.notes,r.created_at,r.updated_at,st.name AS service_type_name
			FROM records r JOIN service_types st ON st.id=r.service_type_id
			WHERE r.vehicle_id=v.id
			ORDER BY r.occurred_on DESC,r.created_at DESC,r.id DESC LIMIT 1
		) lr ON true
		WHERE (v.archived_at IS NOT NULL)=$1 ORDER BY lower(v.nickname)`, archived)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []model.Vehicle
	for rows.Next() {
		var v model.Vehicle
		var rid, rvid, typeID, createdBy *uuid.UUID
		var occurred, created, updated *time.Time
		var odometer, cost *int64
		var vendor, notes, typeName *string
		e = rows.Scan(&v.ID, &v.Nickname, &v.Year, &v.Make, &v.Model, &v.VIN, &v.LicensePlate, &v.PhotoKey, &v.Notes, &v.ArchivedAt, &v.CreatedAt, &v.UpdatedAt, &v.CurrentOdometer, &v.LatestOdometer, &rid, &rvid, &typeID, &createdBy, &occurred, &odometer, &cost, &vendor, &notes, &created, &updated, &typeName)
		if e != nil {
			return nil, e
		}
		if rid != nil {
			v.LastRecord = &model.Record{ID: *rid, VehicleID: *rvid, ServiceTypeID: *typeID, CreatedBy: *createdBy, OccurredOn: *occurred, OdometerMiles: odometer, CostCents: cost, Vendor: *vendor, Notes: *notes, CreatedAt: *created, UpdatedAt: *updated, ServiceTypeName: *typeName}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (p *Postgres) GetVehicle(c context.Context, id uuid.UUID) (model.Vehicle, error) {
	return scanVehicle(p.pool.QueryRow(c, `SELECT `+vehicleCols+`,GREATEST(v.current_odometer_miles,(SELECT max(odometer_miles) FROM records WHERE vehicle_id=v.id)) FROM vehicles v WHERE v.id=$1`, id))
}
func (p *Postgres) CreateVehicle(c context.Context, v model.Vehicle) (model.Vehicle, error) {
	e := p.pool.QueryRow(c, `INSERT INTO vehicles(id,nickname,year,make,model,vin,license_plate,photo_key,notes,current_odometer_miles,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id,nickname,year,make,model,vin,license_plate,photo_key,notes,archived_at,created_at,updated_at,current_odometer_miles`, v.ID, v.Nickname, v.Year, v.Make, v.Model, v.VIN, v.LicensePlate, v.PhotoKey, v.Notes, v.CurrentOdometer, v.CreatedAt, v.UpdatedAt).Scan(&v.ID, &v.Nickname, &v.Year, &v.Make, &v.Model, &v.VIN, &v.LicensePlate, &v.PhotoKey, &v.Notes, &v.ArchivedAt, &v.CreatedAt, &v.UpdatedAt, &v.CurrentOdometer)
	v.LatestOdometer = cloneInt64(v.CurrentOdometer)
	return v, e
}
func (p *Postgres) UpdateVehicle(c context.Context, v model.Vehicle) (model.Vehicle, error) {
	tx, e := p.pool.Begin(c)
	if e != nil {
		return v, e
	}
	defer func() { _ = tx.Rollback(c) }()
	tag, e := tx.Exec(c, `UPDATE vehicles SET nickname=$2,year=$3,make=$4,model=$5,vin=$6,license_plate=$7,photo_key=$8,notes=$9,current_odometer_miles=$10,updated_at=$11 WHERE id=$1`, v.ID, v.Nickname, v.Year, v.Make, v.Model, v.VIN, v.LicensePlate, v.PhotoKey, v.Notes, v.CurrentOdometer, v.UpdatedAt)
	if e != nil {
		return v, e
	}
	if tag.RowsAffected() == 0 {
		return v, ErrNotFound
	}
	if v.CurrentOdometer != nil {
		if _, e = tx.Exec(c, initializeReminderOdometerSQL, v.ID, v.CurrentOdometer); e != nil {
			return v, e
		}
	}
	if e = tx.Commit(c); e != nil {
		return v, e
	}
	return p.GetVehicle(c, v.ID)
}
func (p *Postgres) ArchiveVehicle(c context.Context, id uuid.UUID) error {
	tag, e := p.pool.Exec(c, `UPDATE vehicles SET archived_at=now(),updated_at=now() WHERE id=$1`, id)
	if e == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return e
}
func (p *Postgres) ListServiceTypes(c context.Context) ([]model.ServiceType, error) {
	rows, e := p.pool.Query(c, `SELECT id,name,is_seeded,created_at FROM service_types ORDER BY lower(name)`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []model.ServiceType
	for rows.Next() {
		var t model.ServiceType
		if e := rows.Scan(&t.ID, &t.Name, &t.Seeded, &t.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (p *Postgres) CreateServiceType(c context.Context, t model.ServiceType) (model.ServiceType, error) {
	e := p.pool.QueryRow(c, `INSERT INTO service_types(id,name,is_seeded,created_at) VALUES($1,$2,false,$3) RETURNING id,name,is_seeded,created_at`, t.ID, t.Name, t.CreatedAt).Scan(&t.ID, &t.Name, &t.Seeded, &t.CreatedAt)
	if e != nil && strings.Contains(strings.ToLower(e.Error()), "unique") {
		e = ErrConflict
	}
	return t, e
}

const recordSelect = `SELECT r.id,r.vehicle_id,r.service_type_id,r.created_by,r.occurred_on,r.odometer_miles,r.cost_cents,r.vendor,r.notes,r.created_at,r.updated_at,v.nickname,st.name,COALESCE(NULLIF(u.display_name,''),u.email),(SELECT count(*) FROM attachments a WHERE a.record_id=r.id) FROM records r JOIN vehicles v ON v.id=r.vehicle_id JOIN service_types st ON st.id=r.service_type_id JOIN users u ON u.id=r.created_by`
const recordFilter = ` WHERE ($1::uuid IS NULL OR r.vehicle_id=$1) AND ($2::uuid IS NULL OR r.service_type_id=$2) AND ($3::date IS NULL OR r.occurred_on >= $3) AND ($4::date IS NULL OR r.occurred_on <= $4) AND ($5='' OR st.name ILIKE '%'||$5||'%' ESCAPE E'\\' OR r.vendor ILIKE '%'||$5||'%' ESCAPE E'\\' OR r.notes ILIKE '%'||$5||'%' ESCAPE E'\\')`

func scanRecord(row pgx.Row) (model.Record, error) {
	var r model.Record
	e := row.Scan(&r.ID, &r.VehicleID, &r.ServiceTypeID, &r.CreatedBy, &r.OccurredOn, &r.OdometerMiles, &r.CostCents, &r.Vendor, &r.Notes, &r.CreatedAt, &r.UpdatedAt, &r.VehicleName, &r.ServiceTypeName, &r.CreatedByName, &r.AttachmentCount)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return r, e
}
func (p *Postgres) ListRecords(c context.Context, q model.RecordQuery) ([]model.Record, error) {
	order := recordOrder(q)
	sql := recordSelect + recordFilter + ` ORDER BY ` + order
	rows, e := p.pool.Query(c, sql, q.VehicleID, q.ServiceTypeID, q.From, q.To, escapeLikePattern(q.Search))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []model.Record
	for rows.Next() {
		r, e := scanRecord(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func recordOrder(q model.RecordQuery) string {
	q.Sort, q.Desc = normalizedRecordSort(q.Sort, q.Desc)
	order := "r.occurred_on DESC,r.created_at DESC,r.id DESC"
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	switch q.Sort {
	case "mileage":
		order = "r.odometer_miles " + dir + " NULLS LAST,r.occurred_on DESC,r.created_at DESC,r.id DESC"
	case "cost":
		order = "r.cost_cents " + dir + " NULLS LAST,r.occurred_on DESC,r.created_at DESC,r.id DESC"
	case "date":
		order = "r.occurred_on " + dir + ",r.created_at DESC,r.id DESC"
	}
	return order
}
func (p *Postgres) GetRecord(c context.Context, id uuid.UUID) (model.Record, []model.Attachment, error) {
	r, e := scanRecord(p.pool.QueryRow(c, recordSelect+` WHERE r.id=$1`, id))
	if e != nil {
		return r, nil, e
	}
	rows, e := p.pool.Query(c, `SELECT id,record_id,original_filename,content_type,byte_size,storage_key,created_at FROM attachments WHERE record_id=$1 ORDER BY created_at,id`, id)
	if e != nil {
		return r, nil, e
	}
	defer rows.Close()
	var out []model.Attachment
	for rows.Next() {
		var a model.Attachment
		if e := rows.Scan(&a.ID, &a.RecordID, &a.OriginalFilename, &a.ContentType, &a.ByteSize, &a.StorageKey, &a.CreatedAt); e != nil {
			return r, nil, e
		}
		out = append(out, a)
	}
	return r, out, rows.Err()
}
func (p *Postgres) GetAttachment(c context.Context, id uuid.UUID) (model.Record, model.Attachment, error) {
	row := p.pool.QueryRow(c, `SELECT r.id,r.vehicle_id,r.service_type_id,r.created_by,r.occurred_on,r.odometer_miles,r.cost_cents,r.vendor,r.notes,r.created_at,r.updated_at,v.nickname,st.name,COALESCE(NULLIF(u.display_name,''),u.email),(SELECT count(*) FROM attachments x WHERE x.record_id=r.id),a.id,a.record_id,a.original_filename,a.content_type,a.byte_size,a.storage_key,a.created_at FROM attachments a JOIN records r ON r.id=a.record_id JOIN vehicles v ON v.id=r.vehicle_id JOIN service_types st ON st.id=r.service_type_id JOIN users u ON u.id=r.created_by WHERE a.id=$1`, id)
	var r model.Record
	var a model.Attachment
	e := row.Scan(&r.ID, &r.VehicleID, &r.ServiceTypeID, &r.CreatedBy, &r.OccurredOn, &r.OdometerMiles, &r.CostCents, &r.Vendor, &r.Notes, &r.CreatedAt, &r.UpdatedAt, &r.VehicleName, &r.ServiceTypeName, &r.CreatedByName, &r.AttachmentCount, &a.ID, &a.RecordID, &a.OriginalFilename, &a.ContentType, &a.ByteSize, &a.StorageKey, &a.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return model.Record{}, model.Attachment{}, ErrNotFound
	}
	return r, a, e
}
func (p *Postgres) CreateRecord(c context.Context, r model.Record, as []model.Attachment) (model.Record, error) {
	tx, e := p.pool.Begin(c)
	if e != nil {
		return r, e
	}
	defer func() { _ = tx.Rollback(c) }()
	_, e = tx.Exec(c, `INSERT INTO records(id,vehicle_id,service_type_id,occurred_on,odometer_miles,cost_cents,vendor,notes,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, r.ID, r.VehicleID, r.ServiceTypeID, r.OccurredOn, r.OdometerMiles, r.CostCents, r.Vendor, r.Notes, r.CreatedBy, r.CreatedAt, r.UpdatedAt)
	if e != nil {
		return r, e
	}
	for _, a := range as {
		if _, e = tx.Exec(c, `INSERT INTO attachments(id,record_id,original_filename,content_type,byte_size,storage_key,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.ID, a.RecordID, a.OriginalFilename, a.ContentType, a.ByteSize, a.StorageKey, a.CreatedAt); e != nil {
			return r, e
		}
	}
	if e = tx.Commit(c); e != nil {
		return r, e
	}
	return r, nil
}
func (p *Postgres) UpdateRecord(c context.Context, r model.Record) (model.Record, error) {
	tag, e := p.pool.Exec(c, `UPDATE records SET service_type_id=$2,occurred_on=$3,odometer_miles=$4,cost_cents=$5,vendor=$6,notes=$7,updated_at=$8 WHERE id=$1`, r.ID, r.ServiceTypeID, r.OccurredOn, r.OdometerMiles, r.CostCents, r.Vendor, r.Notes, r.UpdatedAt)
	if e == nil && tag.RowsAffected() == 0 {
		e = ErrNotFound
	}
	return r, e
}
func (p *Postgres) DeleteRecord(c context.Context, id uuid.UUID) ([]string, error) {
	tx, e := p.pool.Begin(c)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(c) }()
	rows, e := tx.Query(c, `SELECT storage_key FROM attachments WHERE record_id=$1`, id)
	if e != nil {
		return nil, e
	}
	var keys []string
	for rows.Next() {
		var k string
		if e = rows.Scan(&k); e != nil {
			rows.Close()
			return nil, e
		}
		keys = append(keys, k)
	}
	rows.Close()
	tag, e := tx.Exec(c, `DELETE FROM records WHERE id=$1`, id)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	if e = tx.Commit(c); e != nil {
		return nil, e
	}
	return keys, nil
}
func (p *Postgres) AddAttachments(c context.Context, rid uuid.UUID, as []model.Attachment) error {
	tx, e := p.pool.Begin(c)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(c) }()
	for _, a := range as {
		if _, e = tx.Exec(c, `INSERT INTO attachments(id,record_id,original_filename,content_type,byte_size,storage_key,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.ID, rid, a.OriginalFilename, a.ContentType, a.ByteSize, a.StorageKey, a.CreatedAt); e != nil {
			return e
		}
	}
	return tx.Commit(c)
}
func (p *Postgres) DeleteAttachment(c context.Context, id uuid.UUID) (string, error) {
	var k string
	e := p.pool.QueryRow(c, `DELETE FROM attachments WHERE id=$1 RETURNING storage_key`, id).Scan(&k)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return k, e
}

func (p *Postgres) ListReminders(c context.Context, vid *uuid.UUID, disabled bool) ([]model.Reminder, error) {
	rows, e := p.pool.Query(c, `SELECT rm.id,rm.vehicle_id,rm.service_type_id,v.nickname,st.name,rm.interval_months,rm.interval_miles,rm.starting_odometer_miles,rm.enabled,rm.created_at,rm.updated_at,b.id,b.occurred_on,b.odometer_miles,b.created_at,GREATEST(v.current_odometer_miles,(SELECT max(odometer_miles) FROM records x WHERE x.vehicle_id=rm.vehicle_id)) FROM reminders rm JOIN vehicles v ON v.id=rm.vehicle_id JOIN service_types st ON st.id=rm.service_type_id LEFT JOIN LATERAL(SELECT r.id,r.occurred_on,r.odometer_miles,r.created_at FROM records r WHERE r.vehicle_id=rm.vehicle_id AND r.service_type_id=rm.service_type_id ORDER BY r.occurred_on DESC,r.created_at DESC,r.id DESC LIMIT 1)b ON true WHERE ($1::uuid IS NULL OR rm.vehicle_id=$1) AND ($2 OR rm.enabled) AND ($1::uuid IS NOT NULL OR v.archived_at IS NULL) ORDER BY lower(v.nickname),lower(st.name)`, vid, disabled)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []model.Reminder
	for rows.Next() {
		var r model.Reminder
		var bid *uuid.UUID
		var date, created *time.Time
		var odo *int64
		if e := rows.Scan(&r.ID, &r.VehicleID, &r.ServiceTypeID, &r.VehicleName, &r.ServiceTypeName, &r.IntervalMonths, &r.IntervalMiles, &r.StartingOdometer, &r.Enabled, &r.CreatedAt, &r.UpdatedAt, &bid, &date, &odo, &created, &r.LatestOdometer); e != nil {
			return nil, e
		}
		if bid != nil {
			r.Baseline = &model.Record{ID: *bid, VehicleID: r.VehicleID, ServiceTypeID: r.ServiceTypeID, OccurredOn: *date, OdometerMiles: odo, CreatedAt: *created}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (p *Postgres) UpsertReminder(c context.Context, r model.Reminder) (model.Reminder, error) {
	e := p.pool.QueryRow(c, `INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months,interval_miles,starting_odometer_miles,enabled,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(vehicle_id,service_type_id) DO UPDATE SET interval_months=excluded.interval_months,interval_miles=excluded.interval_miles,enabled=excluded.enabled,updated_at=excluded.updated_at RETURNING id,created_at,starting_odometer_miles`, r.ID, r.VehicleID, r.ServiceTypeID, r.IntervalMonths, r.IntervalMiles, r.StartingOdometer, r.Enabled, r.CreatedAt, r.UpdatedAt).Scan(&r.ID, &r.CreatedAt, &r.StartingOdometer)
	return r, e
}
func (p *Postgres) DeleteReminder(c context.Context, id uuid.UUID) error {
	tag, e := p.pool.Exec(c, `DELETE FROM reminders WHERE id=$1`, id)
	if e == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return e
}
func ptrString[T fmt.Stringer](v *T) string {
	if v == nil {
		return ""
	}
	return (*v).String()
}
