package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

type Memory struct {
	mu          sync.RWMutex
	users       map[uuid.UUID]model.User
	sessions    map[string]model.Session
	vehicles    map[uuid.UUID]model.Vehicle
	types       map[uuid.UUID]model.ServiceType
	records     map[uuid.UUID]model.Record
	attachments map[uuid.UUID]model.Attachment
	reminders   map[uuid.UUID]model.Reminder
}

func NewMemory() *Memory {
	m := &Memory{users: map[uuid.UUID]model.User{}, sessions: map[string]model.Session{}, vehicles: map[uuid.UUID]model.Vehicle{}, types: map[uuid.UUID]model.ServiceType{}, records: map[uuid.UUID]model.Record{}, attachments: map[uuid.UUID]model.Attachment{}, reminders: map[uuid.UUID]model.Reminder{}}
	for _, n := range []string{"Oil change", "Tire rotation", "Engine air filter", "Cabin air filter", "Brake pads", "Brake fluid", "Coolant", "Transmission fluid", "Battery", "Wipers", "Registration/inspection", "Repair", "Other"} {
		id := uuid.New()
		m.types[id] = model.ServiceType{ID: id, Name: n, Seeded: true, CreatedAt: time.Now()}
	}
	return m
}
func (m *Memory) Close() {}
func (m *Memory) FindUserByOAuth(_ context.Context, p, s string) (*model.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.OAuthProvider == p && u.OAuthSubject == s {
			x := u
			return &x, nil
		}
	}
	return nil, nil
}
func (m *Memory) FindUserByEmail(_ context.Context, e string) (*model.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if strings.EqualFold(u.Email, e) {
			x := u
			return &x, nil
		}
	}
	return nil, nil
}
func (m *Memory) UpsertUser(_ context.Context, u model.User) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var identityID, emailID uuid.UUID
	for id, x := range m.users {
		if x.OAuthProvider == u.OAuthProvider && x.OAuthSubject == u.OAuthSubject {
			if identityID != uuid.Nil && identityID != id {
				return u, ErrConflict
			}
			identityID = id
		}
		if strings.EqualFold(x.Email, u.Email) {
			if emailID != uuid.Nil && emailID != id {
				return u, ErrConflict
			}
			emailID = id
		}
	}
	if identityID != uuid.Nil && emailID != uuid.Nil && identityID != emailID {
		return u, ErrConflict
	}
	persistedID := identityID
	if persistedID == uuid.Nil {
		persistedID = emailID
	}
	if persistedID != uuid.Nil {
		existing := m.users[persistedID]
		u.ID = persistedID
		u.CreatedAt = existing.CreatedAt
		m.users[persistedID] = u
		return u, nil
	}
	m.users[u.ID] = u
	return u, nil
}
func (m *Memory) CreateSession(_ context.Context, s model.Session, h string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[h] = s
	return nil
}
func (m *Memory) FindSession(_ context.Context, h string) (*model.Session, *model.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[h]
	if !ok {
		return nil, nil, nil
	}
	u, ok := m.users[s.UserID]
	if !ok {
		return nil, nil, nil
	}
	return &s, &u, nil
}
func (m *Memory) DeleteSession(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, s := range m.sessions {
		if s.ID == id {
			delete(m.sessions, h)
		}
	}
	return nil
}
func (m *Memory) ListVehicles(_ context.Context, archived bool) ([]model.Vehicle, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []model.Vehicle{}
	for _, v := range m.vehicles {
		if (v.ArchivedAt != nil) == archived {
			x := m.enrichVehicle(v)
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Nickname) < strings.ToLower(out[j].Nickname) })
	return out, nil
}
func (m *Memory) enrichVehicle(v model.Vehicle) model.Vehicle {
	v.LatestOdometer = cloneInt64(v.CurrentOdometer)
	for _, r := range m.records {
		if r.VehicleID != v.ID {
			continue
		}
		if r.OdometerMiles != nil && (v.LatestOdometer == nil || *r.OdometerMiles > *v.LatestOdometer) {
			x := *r.OdometerMiles
			v.LatestOdometer = &x
		}
		if v.LastRecord == nil || recordLess(*v.LastRecord, r) {
			x := m.enrichRecord(r)
			v.LastRecord = &x
		}
	}
	return v
}
func recordLess(a, b model.Record) bool {
	if !a.OccurredOn.Equal(b.OccurredOn) {
		return a.OccurredOn.Before(b.OccurredOn)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID.String() < b.ID.String()
}
func (m *Memory) GetVehicle(_ context.Context, id uuid.UUID) (model.Vehicle, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vehicles[id]
	if !ok {
		return v, ErrNotFound
	}
	return m.enrichVehicle(v), nil
}
func (m *Memory) CreateVehicle(_ context.Context, v model.Vehicle) (model.Vehicle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vehicles[v.ID] = v
	return m.enrichVehicle(v), nil
}
func (m *Memory) UpdateVehicle(_ context.Context, v model.Vehicle) (model.Vehicle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.vehicles[v.ID]
	if !ok {
		return v, ErrNotFound
	}
	v.CreatedAt = old.CreatedAt
	v.ArchivedAt = old.ArchivedAt
	m.vehicles[v.ID] = v
	if v.CurrentOdometer != nil {
		effectiveOdometer := m.enrichVehicle(v).LatestOdometer
		for id, reminder := range m.reminders {
			if reminder.VehicleID == v.ID && reminder.StartingOdometerPending {
				reminder.StartingOdometer = cloneInt64(effectiveOdometer)
				reminder.StartingOdometerPending = false
				m.reminders[id] = reminder
			}
		}
	}
	return m.enrichVehicle(v), nil
}
func (m *Memory) ArchiveVehicle(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.vehicles[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now()
	v.ArchivedAt = &now
	v.UpdatedAt = now
	m.vehicles[id] = v
	return nil
}
func (m *Memory) ListServiceTypes(_ context.Context) ([]model.ServiceType, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.ServiceType, 0, len(m.types))
	for _, v := range m.types {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}
func (m *Memory) CreateServiceType(_ context.Context, t model.ServiceType) (model.ServiceType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.types {
		if strings.EqualFold(strings.TrimSpace(x.Name), strings.TrimSpace(t.Name)) {
			return t, ErrConflict
		}
	}
	m.types[t.ID] = t
	return t, nil
}
func (m *Memory) enrichRecord(r model.Record) model.Record {
	if v, ok := m.vehicles[r.VehicleID]; ok {
		r.VehicleName = v.Nickname
	}
	if t, ok := m.types[r.ServiceTypeID]; ok {
		r.ServiceTypeName = t.Name
	}
	if u, ok := m.users[r.CreatedBy]; ok {
		r.CreatedByName = u.DisplayName
		if r.CreatedByName == "" {
			r.CreatedByName = u.Email
		}
	}
	r.AttachmentCount = 0
	for _, a := range m.attachments {
		if a.RecordID == r.ID {
			r.AttachmentCount++
		}
	}
	return r
}
func (m *Memory) ListRecords(_ context.Context, q model.RecordQuery) ([]model.Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []model.Record{}
	for _, raw := range m.records {
		r := m.enrichRecord(raw)
		if q.VehicleID != nil && r.VehicleID != *q.VehicleID {
			continue
		}
		if q.ServiceTypeID != nil && r.ServiceTypeID != *q.ServiceTypeID {
			continue
		}
		if q.From != nil && r.OccurredOn.Before(*q.From) {
			continue
		}
		if q.To != nil && r.OccurredOn.After(*q.To) {
			continue
		}
		s := strings.ToLower(strings.TrimSpace(q.Search))
		if s != "" && !strings.Contains(strings.ToLower(r.ServiceTypeName+" "+r.Vendor+" "+r.Notes), s) {
			continue
		}
		out = append(out, r)
	}
	field, descending := normalizedRecordSort(q.Sort, q.Desc)
	sort.SliceStable(out, func(i, j int) bool { return compareRecords(out[i], out[j], field, descending) })
	return out, nil
}
func compareRecords(a, b model.Record, field string, desc bool) bool {
	cmp := 0
	primaryDesc := desc
	switch field {
	case "mileage":
		if a.OdometerMiles == nil || b.OdometerMiles == nil {
			if a.OdometerMiles == nil && b.OdometerMiles != nil {
				return false
			}
			if a.OdometerMiles != nil && b.OdometerMiles == nil {
				return true
			}
		} else {
			cmp = cmpInt64(*a.OdometerMiles, *b.OdometerMiles)
		}
	case "cost":
		if a.CostCents == nil || b.CostCents == nil {
			if a.CostCents == nil && b.CostCents != nil {
				return false
			}
			if a.CostCents != nil && b.CostCents == nil {
				return true
			}
		} else {
			cmp = cmpInt64(*a.CostCents, *b.CostCents)
		}
	default:
		if a.OccurredOn.Before(b.OccurredOn) {
			cmp = -1
		} else if a.OccurredOn.After(b.OccurredOn) {
			cmp = 1
		}
	}
	if cmp != 0 {
		if primaryDesc {
			return cmp > 0
		}
		return cmp < 0
	}
	if field == "mileage" || field == "cost" {
		if a.OccurredOn.Before(b.OccurredOn) {
			return false
		}
		if a.OccurredOn.After(b.OccurredOn) {
			return true
		}
	}
	if a.CreatedAt.Before(b.CreatedAt) {
		return false
	}
	if a.CreatedAt.After(b.CreatedAt) {
		return true
	}
	return strings.Compare(a.ID.String(), b.ID.String()) > 0
}
func cmpInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
func (m *Memory) GetRecord(_ context.Context, id uuid.UUID) (model.Record, []model.Attachment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.records[id]
	if !ok {
		return r, nil, ErrNotFound
	}
	as := []model.Attachment{}
	for _, a := range m.attachments {
		if a.RecordID == id {
			as = append(as, a)
		}
	}
	sort.Slice(as, func(i, j int) bool { return as[i].CreatedAt.Before(as[j].CreatedAt) })
	return m.enrichRecord(r), as, nil
}
func (m *Memory) GetAttachment(_ context.Context, id uuid.UUID) (model.Record, model.Attachment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.attachments[id]
	if !ok {
		return model.Record{}, model.Attachment{}, ErrNotFound
	}
	r, ok := m.records[a.RecordID]
	if !ok {
		return model.Record{}, model.Attachment{}, ErrNotFound
	}
	return m.enrichRecord(r), a, nil
}
func (m *Memory) CreateRecord(_ context.Context, r model.Record, as []model.Attachment) (model.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.vehicles[r.VehicleID]; !ok {
		return r, ErrNotFound
	}
	if _, ok := m.types[r.ServiceTypeID]; !ok {
		return r, ErrNotFound
	}
	m.records[r.ID] = r
	for _, a := range as {
		m.attachments[a.ID] = a
	}
	return m.enrichRecord(r), nil
}
func (m *Memory) UpdateRecord(_ context.Context, r model.Record) (model.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.records[r.ID]
	if !ok {
		return r, ErrNotFound
	}
	if _, ok := m.types[r.ServiceTypeID]; !ok {
		return r, ErrNotFound
	}
	r.VehicleID = old.VehicleID
	if _, ok := m.vehicles[r.VehicleID]; !ok {
		return r, ErrNotFound
	}
	r.CreatedAt = old.CreatedAt
	r.CreatedBy = old.CreatedBy
	m.records[r.ID] = r
	return m.enrichRecord(r), nil
}
func (m *Memory) DeleteRecord(_ context.Context, id uuid.UUID) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[id]; !ok {
		return nil, ErrNotFound
	}
	var keys []string
	for aid, a := range m.attachments {
		if a.RecordID == id {
			keys = append(keys, a.StorageKey)
			delete(m.attachments, aid)
		}
	}
	delete(m.records, id)
	return keys, nil
}
func (m *Memory) AddAttachments(_ context.Context, rid uuid.UUID, as []model.Attachment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[rid]; !ok {
		return ErrNotFound
	}
	for _, a := range as {
		m.attachments[a.ID] = a
	}
	return nil
}
func (m *Memory) DeleteAttachment(_ context.Context, id uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.attachments[id]
	if !ok {
		return "", ErrNotFound
	}
	delete(m.attachments, id)
	return a.StorageKey, nil
}
func (m *Memory) ListReminders(_ context.Context, vehicleID *uuid.UUID, includeDisabled bool) ([]model.Reminder, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []model.Reminder{}
	for _, raw := range m.reminders {
		if vehicleID != nil && raw.VehicleID != *vehicleID {
			continue
		}
		if !includeDisabled && !raw.Enabled {
			continue
		}
		v, ok := m.vehicles[raw.VehicleID]
		if !ok || (vehicleID == nil && v.ArchivedAt != nil) {
			continue
		}
		r := raw
		r.StartingOdometer = cloneInt64(raw.StartingOdometer)
		r.VehicleName = v.Nickname
		r.ServiceTypeName = m.types[r.ServiceTypeID].Name
		r.LatestOdometer = cloneInt64(v.CurrentOdometer)
		for _, rec := range m.records {
			if rec.VehicleID == r.VehicleID {
				if rec.OdometerMiles != nil && (r.LatestOdometer == nil || *rec.OdometerMiles > *r.LatestOdometer) {
					x := *rec.OdometerMiles
					r.LatestOdometer = &x
				}
				if rec.ServiceTypeID == r.ServiceTypeID && (r.Baseline == nil || recordLess(*r.Baseline, rec)) {
					x := m.enrichRecord(rec)
					r.Baseline = &x
				}
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ServiceTypeName) < strings.ToLower(out[j].ServiceTypeName)
	})
	return out, nil
}
func (m *Memory) UpsertReminder(_ context.Context, r model.Reminder) (model.Reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.vehicles[r.VehicleID]; !ok {
		return r, ErrNotFound
	}
	if _, ok := m.types[r.ServiceTypeID]; !ok {
		return r, ErrNotFound
	}
	if r.IntervalMonths == nil && r.IntervalMiles == nil {
		return r, fmt.Errorf("reminder interval is required")
	}
	for id, x := range m.reminders {
		if x.VehicleID == r.VehicleID && x.ServiceTypeID == r.ServiceTypeID {
			r.ID = id
			r.CreatedAt = x.CreatedAt
			r.StartingOdometer = cloneInt64(x.StartingOdometer)
			r.StartingOdometerPending = x.StartingOdometerPending
			stored := cloneReminderStartingOdometer(r)
			m.reminders[id] = stored
			return cloneReminderStartingOdometer(stored), nil
		}
	}
	stored := cloneReminderStartingOdometer(r)
	m.reminders[r.ID] = stored
	return cloneReminderStartingOdometer(stored), nil
}

func cloneReminderStartingOdometer(r model.Reminder) model.Reminder {
	r.StartingOdometer = cloneInt64(r.StartingOdometer)
	return r
}

func cloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func (m *Memory) DeleteReminder(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reminders[id]; !ok {
		return ErrNotFound
	}
	delete(m.reminders, id)
	return nil
}
