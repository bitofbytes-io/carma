# Carma - Product Requirements

Carma is a self-hosted vehicle maintenance tracker for the household, hosted at
`https://carma.bitofbytes.io`. It keeps a complete service history for every car the
family owns: what was done, when, at what mileage, what it cost, and the receipt that
proves it. Reminders make sure routine maintenance (oil changes, tire rotations,
filters, inspections) doesn't get forgotten.

## Goals

- One place for the full maintenance and repair history of every household vehicle.
- Paper-free receipts: scan or photograph a receipt and attach it to the record.
- Never miss routine service: per-vehicle reminders based on time and/or mileage.
- Easy hand-off: export a vehicle's history to CSV/XLSX for a buyer, mechanic, or
  spreadsheet analysis.
- Shared household access: everyone in the family sees and maintains the same garage.

## Non-goals

- Not a fleet-management product: no fuel logs, trip tracking, driver assignment,
  or cost-per-mile analytics (odometer and cost live on records, so basic analysis is
  possible via export).
- No public signup. Access is limited to an allowlist of Google accounts.
- No mobile app. The web UI must work well on phones, but it is server-rendered HTML.
- No payment/subscription features.

## Users and permissions

- **Authentication:** Google OAuth (OIDC) only, same as dined. A verified Google email
  must appear in the `AUTH_GOOGLE_ALLOWED_EMAILS` allowlist (or an allowed domain) to
  sign in. No passwords, no self-service registration.
- **Authorization:** shared garage. Every signed-in user can view and edit every
  vehicle, record, receipt, and reminder. There are no roles.
- **Attribution:** records store `created_by` so the history shows who logged what.
- **Sessions:** opaque session token in an `HttpOnly` cookie (`carma_session`),
  SHA-256-hashed in Postgres, ~90-day TTL, `Secure` + `SameSite=Lax` in production.
- Unauthenticated requests are redirected to the login page. Unlike dined there are
  no public read-only pages; the entire app is behind login.

## Features

### MVP

#### Vehicles

- Add a vehicle with: nickname (required, e.g. "Dan's Outback"), year, make, model,
  VIN, license plate, optional current odometer, optional photo, notes.
- Edit any field later.
- Archive (soft-delete) a vehicle when sold; archived vehicles are hidden from the
  dashboard and reminders but their history remains viewable and exportable.
- No practical limit on vehicle count.

#### Maintenance / repair records

- A record belongs to one vehicle and captures:
  - date performed (required)
  - service type (required; picked from a list, see below)
  - odometer reading in miles (optional but encouraged - drives mileage reminders)
  - cost (optional, stored in cents)
  - vendor / shop (optional free text; "DIY" is just text)
  - notes (optional free text, e.g. part numbers, warranty info)
- Records can be edited and deleted.
- Vehicle detail page lists records newest-first with:
  - text search across service type, vendor, and notes
  - sort by date, mileage, or cost
  - filter by service type and date range
- **Service types** are a seeded shared list (oil change, tire rotation, engine air
  filter, cabin air filter, brake pads, brake fluid, coolant, transmission fluid,
  battery, wipers, registration/inspection, repair, other). Users can add custom
  types; types are shared across the garage.

#### Receipts (attachments)

- Attach one or more files (PDF, JPEG, PNG, WebP, HEIC) to a record, at creation or
  later.
- Server validates content by magic bytes, caps each file at 25 MiB, and stores it
  under an opaque key (`ab/<uuid>.<ext>`) on the NFS-backed asset volume - the same
  pattern as noted. Original filename is preserved for display/download.
- Receipts render as thumbnails (images) or a document icon (PDF) on the record
  detail page; clicking opens/downloads the file. Attachments can be deleted.

#### Reminders

- A reminder is defined per vehicle + service type with an interval in **months**,
  **miles**, or both (whichever comes first). Example: oil change every 6 months or
  5,000 miles.
- Due computation:
  - *First cycle:* when no matching service record exists, time starts at the
    reminder's creation date and mileage starts at a snapshot of the vehicle's
    effective odometer when the reminder is created. An unknown starting mileage
    leaves only the mileage dimension unevaluable; a configured time interval still
    evaluates normally.
  - *Later cycles:* the most recent record on that vehicle with that service type
    replaces both first-cycle baselines.
  - *Time:* due when `baseline date + interval_months <= today`.
  - *Mileage:* due when `latest known odometer on the vehicle >= baseline odometer +
    interval_miles`. Latest known odometer is the greater of the manually entered
    current odometer and the max reading across the vehicle's records.
  - "Due soon" threshold: within 30 days or 500 miles of due.
- Dashboard shows an Overdue / Due soon panel across all vehicles; each vehicle page
  shows its own reminders with status.
- Logging a new record of the matching service type automatically resets the
  reminder - no manual "mark done" needed.
- Reminders can be disabled without deleting them.

#### Export

- Export records to **CSV** from any records view; the export respects the current
  search/filter, so "2019 Outback, oil changes only, 2023-2025" is one click.
- Columns: vehicle, date, service type, odometer, cost, vendor, notes, receipt count,
  logged by.
- **XLSX** export (via `excelize`) as a nice-to-have follow-up; CSV ships first.

### Stretch: email reminders

- A daily scheduler (goroutine with a ticker, guarded so only one replica sends -
  e.g. a Postgres advisory lock) evaluates all enabled reminders.
- When a reminder is overdue, send an email; keep sending every 30 days until a
  matching record is logged.
- `reminder_notifications` table logs each send (reminder id, sent_at, recipient) to
  enforce the 30-day cadence and provide an audit trail.
- Recipients: all allowlisted users by default, plus optional extra addresses stored
  per reminder (e.g. a family member who doesn't use the app).
- SMTP configuration via Docker secret `carma_smtp_url`
  (`smtp://user:pass@host:port`). If unset, the scheduler is disabled and the app
  runs exactly as the MVP - email is strictly additive.

## Data model

All timestamps `timestamptz`, money in integer cents, soft deletes via `archived_at`.

| Table | Key columns |
|---|---|
| `users` | id, oauth_provider, oauth_subject, email, display_name, created_at |
| `user_sessions` | id, user_id, token_hash, expires_at, created_at |
| `vehicles` | id, nickname, year, make, model, vin, license_plate, current_odometer_miles, photo_key, notes, archived_at, created_at |
| `service_types` | id, name (unique), is_seeded, created_at |
| `records` | id, vehicle_id FK, service_type_id FK, occurred_on (date), odometer_miles, cost_cents, vendor, notes, created_by FK users, created_at, updated_at |
| `attachments` | id, record_id FK, original_filename, content_type, byte_size, storage_key (unique), created_at |
| `reminders` | id, vehicle_id FK, service_type_id FK, interval_months, interval_miles, starting_odometer_miles, enabled, extra_emails (stretch), created_at; unique (vehicle_id, service_type_id) |
| `reminder_notifications` (stretch) | id, reminder_id FK, sent_at, recipients |

Notes:

- `users`/`user_sessions` are copied from dined's migration `004_add_oauth_sessions.sql`.
- At least one of `interval_months` / `interval_miles` must be set on a reminder
  (check constraint).
- Deleting a record cascades to its attachments (and the files are removed from the
  asset store).
- Vehicle photo reuses the same asset store as receipts (`photo_key`).

## Acceptance criteria (MVP)

1. An allowlisted user can sign in with Google; a non-allowlisted Google account is
   rejected with a friendly message.
2. Two different users see the same vehicles and records.
3. A record with a PDF receipt can be created in one form submission; the receipt is
   viewable afterwards from the record page.
4. With an oil-change reminder of 6 months / 5,000 miles and a last oil change 7
   months ago, the dashboard shows it overdue; logging a new oil change clears it.
5. CSV export of a filtered records view opens correctly in Excel/Numbers and
   matches the on-screen rows.
6. The app works acceptably on a phone-width viewport (log a record, snap-and-attach
   a receipt photo from the phone camera via `<input type="file">`).
