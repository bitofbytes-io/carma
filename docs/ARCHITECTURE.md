# Carma - Architecture

Carma follows the **dined pattern**: a single Go binary serving server-rendered HTML
enhanced with HTMX, deployed as one Docker image to the crystal1 Swarm. It borrows
two pieces from noted: the NFS-backed asset store for receipt files and the
automatic migration job in the deploy hook.

## Stack

| Layer | Choice | Precedent |
|---|---|---|
| Language | Go (latest stable, currently 1.26.x) | dined |
| HTTP router | chi (`github.com/go-chi/chi/v5`) | dined, noted |
| Frontend | Server-rendered HTML + HTMX (`static/htmx.min.js`), hand-authored CSS | dined |
| Database | PostgreSQL on bahamut (`192.168.1.2:8432`), database `carma` | both |
| Data access | Raw SQL via pgx/v5 (`pgxpool`), no ORM | both |
| Migrations | Numbered SQL files, embedded, applied by a `carma-migrate` binary at deploy time | noted |
| Auth | Google OAuth/OIDC + email allowlist + hashed cookie sessions | dined |
| File storage | Opaque-key filesystem store on NFS volume (`/data/assets`) | noted |
| Export | `encoding/csv` (stdlib); XLSX later via `github.com/xuri/excelize/v2` | new |
| Email | stdlib SMTP over implicit TLS, password via `carma_smtp_password` | new |

### Why a monolith + HTMX and not an SPA

The UI is forms, sortable tables, file uploads, and status lists - all of which
dined already does well with HTMX. A single image means one Dockerfile, one CI
build, one Swarm service, and no CORS/API-versioning concerns. Noted needed Angular
for its interactive PDF reader; carma has no equivalent complexity.

## Runtime topology

```mermaid
flowchart LR
  Browser -->|"HTTPS carma.bitofbytes.io"| Traefik["Traefik v3 on crystal1 (ACME LE)"]
  Traefik -->|"http://carma:4700"| App["proxy_carma x3 (Go + HTMX)"]
  App --> PG[("Postgres @ bahamut:8432, db carma")]
  App --> NFS["NFS volume carma_assets -> bahamut /volume1/carma-assets"]
  App --> SMTP["SMTP relay (overdue reminders)"]
```

- **Port:** 4700 (dined uses 4600, noted 8080; each app gets its own).
- **Replicas:** 3, spread across nodes, start-first rolling updates with rollback on
  failure (same `deploy:` block as dined/noted).
- **Traefik:** file-provider router (no Swarm labels), `Host(carma.bitofbytes.io)`,
  `websecure` entrypoint, `le` cert resolver, `secure-headers` +
  `private-app-limit` middlewares. See [INFRA.md](INFRA.md).
- **Health:** `GET /health` returns 200 with no auth; used by the Dockerfile
  HEALTHCHECK and the deploy hook.

## Repository layout (planned)

```
carma/
├── cmd/
│   ├── carma/            # main server binary
│   └── carma-migrate/    # migration runner (built into the same image)
├── internal/
│   ├── auth/             # Google OIDC, sessions, allowlist (ported from dined)
│   ├── config/           # env + /run/secrets *_FILE loading (getEnvOrFile pattern)
│   ├── handler/          # HTTP handlers (pages, records, vehicles, reminders, export)
│   ├── middleware/       # auth, logging, same-origin
│   ├── model/            # domain types + validation
│   ├── repository/       # pgx SQL for vehicles/records/attachments/reminders
│   ├── assets/           # filesystem asset store (ported from noted)
│   ├── reminder/         # due-status computation
│   ├── reminderemail/    # scheduler, runner, and message rendering
│   ├── mailer/           # implicit-TLS SMTP client
│   ├── export/           # CSV (later XLSX) writers
│   └── ui/               # html/template views, HTMX partials
├── migrations/           # 001_..., embedded via embed.go
├── static/               # styles.css, htmx.min.js, favicons
├── docs/                 # this documentation
├── Dockerfile
├── Makefile
├── local.mk.example
└── .github/workflows/ci.yml
```

## Request handling

- Full page loads render complete templates; HTMX requests (`HX-Request` header)
  render partials (e.g. the records table body after a filter change, the reminder
  panel after logging a record).
- Mutations are form POSTs. Same-origin middleware (as in dined) protects against
  CSRF for cookie-authenticated form posts.
- Records list querying (search/sort/filter) is one parameterized SQL query; the
  same query feeds both the HTML table and the CSV export, so exports always match
  the on-screen view.

## File storage (receipts, vehicle photos)

Ported from noted's `internal/assets`:

- `Store` interface with a `LocalStore` implementation rooted at `ASSET_ROOT`
  (`/data/assets` in prod, `.local/carma-assets` locally).
- Keys are opaque: `ab/<uuid>.<ext>` (two-hex-char shard directory). The DB maps
  keys to original filenames and content types.
- Upload path: multipart form → magic-byte sniff (`%PDF-`, JPEG/PNG/WebP/HEIC
  signatures) → size check (25 MiB cap via `MAX_UPLOAD_BYTES`) → write to a
  `temporary/` staging dir → fsync + rename into `objects/` → insert `attachments`
  row in the same request.
- Download path: `http.ServeContent` for byte-range support (large PDFs on iOS).
- Deletes remove the DB row first, then best-effort remove the file.
- Durability: the volume is an NFS export on bahamut covered by the existing
  Hyper Backup job, same as noted's `/volume1/noted-assets`.

## Reminder engine

Due status is **computed, never stored**, so it can't drift:

```mermaid
flowchart TD
  R[Reminder: vehicle + service type + interval] --> B{Any record of this type?}
  B -->|no| F[Use immutable created date and starting odometer snapshot]
  B -->|yes| L[Use latest matching record date and odometer]
  F --> T{"baseline date + months <= today?"}
  F --> M{"effective odometer >= baseline odometer + miles?"}
  L --> T
  L --> M
  T -->|yes| DUE2[Overdue]
  M -->|yes| DUE2
  T -->|"within 30 days"| SOON[Due soon]
  M -->|"within 500 miles or projected within 30 days"| SOON
  T -->|no| OK[OK]
  M -->|no| OK
```

One SQL query joins reminders to the latest matching record and the vehicle's
effective odometer (the greater of its manually maintained current odometer and all
record readings); the handler classifies each row as overdue / due soon / ok. A
reminder snapshots that effective odometer when first created. Edits preserve its
creation date and snapshot, while logging a matching record naturally replaces both
first-cycle baselines.

Mileage intervals are also projected to a date. A second query loads the last 12
months of each vehicle's `odometer_readings` and record mileages, and
`reminder.EstimateMilesPerDay` fits a least-squares line through them (at least two
points spanning 14 days, with increasing mileage). The projected due date is
`today + miles left / miles per day`; a projection within 30 days makes the reminder
due soon, but only an actual reading makes it overdue, so email is unaffected.

## Email reminders

- `internal/reminderemail.Runner` runs at startup and daily (via `internal/schedule.Every`); it takes a Postgres session advisory lock
  (`pg_try_advisory_lock`) so only one of the 3 replicas sends.
- For each enabled, overdue reminder: skip if a `reminder_notifications` row exists
  within the last 30 days; otherwise send and log.
- `internal/mailer`: authenticated SMTP over certificate-verified implicit TLS.
  Separate configuration fields keep the password in a password-only `*_FILE` secret.
  `REMINDER_EMAIL_ENABLED=false` leaves the reminder and digest schedulers off.
- Email content: vehicle, service type, baseline (last done date/mileage), what
  triggered it (time or mileage), and a link to the vehicle page.
- `internal/reminderemail.DigestRunner` (on whenever reminder email is, same SMTP
  settings) checks hourly, under its own advisory lock, whether this week's digest
  is due: from 08:00 household time on Monday, one email per week lists every overdue and
  due-soon reminder (including projected mileage dates) grouped by vehicle, overdue
  first. It leads with new NHTSA recalls: open, undismissed `vehicle_recalls` with no
  `recall_notifications` row, each linking to NHTSA's VIN lookup (`nhtsa.gov/recalls?vymm=<VIN>`,
  only for a valid VIN) and to `PUBLIC_URL/vehicles/{id}#recalls` to dismiss it.
  A `reminder_digests` row per `week_start` (the local Monday) dedupes sends
  across replicas; reserving it also records its recalls in `recall_notifications`
  (with the digest's id) in the same transaction, and releasing an unsent claim
  deletes them, so a recall is announced at most once. That table is kept apart from
  `vehicle_recalls`, whose rows each check replaces, so a campaign NHTSA drops and
  lists again is not announced twice. A week with nothing to report is not
  recorded, so a later check that week still sends once something comes due or a
  recall is found.

## NHTSA recalls and VIN decode

Always on; both NHTSA APIs are public and keyless, so the containers only need
outbound HTTPS to `vpic.nhtsa.dot.gov` and `api.nhtsa.gov`.
- `internal/nhtsa`: VIN decode via vPIC (`DecodeVinValues`) and recall campaigns via
  `api.nhtsa.gov/recalls/recallsByVehicle` (make, model, model year).
- `POST /vehicles/vin-decode` (htmx) re-renders the form's identity fields filled in
  from vPIC, keeping what was typed when the VIN can't be decoded.
- `internal/recallcheck.Runner` runs at startup and every 6 hours under its own
  advisory lock. It checks vehicles never checked, last checked over 7 days ago, or
  whose year, make, or model changed since (`vehicle_recall_checks`), and replaces
  their `vehicle_recalls` rows, keeping dismissals and notifications of campaigns
  still listed. A failed fetch is retried on the next run. A vehicle's first check
  for a year, make, and model is its baseline: those campaigns are recorded as
  already notified (they show on the vehicle page right away), so the digest emails
  only campaigns NHTSA adds later. Migration 008 does the same for campaigns stored
  before recall emails existed. `carma-reminders --check-recalls` runs one check by
  hand.
- Campaigns cover a model year rather than a VIN, so the vehicle page lets the
  household dismiss (and restore) each one. Undismissed campaigns show in the
  dashboard's "Needs attention" as one row per vehicle.

## Configuration

Same `getEnvOrFile` convention as dined/noted - every secret readable from env or
`/run/secrets/<name>` via `*_FILE`:

| Variable | Prod source | Purpose |
|---|---|---|
| `APP_ENV` | stack env (`production`) | prod behaviors (secure cookies, no development auth) |
| `PORT` | stack env (`4700`) | listen port |
| `DATABASE_URL` | secret `carma_database_url` | Postgres DSN |
| `AUTH_GOOGLE_CLIENT_ID` / `_SECRET` | secrets `carma_google_client_id` / `_secret` | OIDC |
| `AUTH_GOOGLE_REDIRECT_URL` | stack env | `https://carma.bitofbytes.io/api/auth/google/callback`; an `https` URL also makes cookies `Secure` |
| `AUTH_GOOGLE_ALLOWED_EMAILS` | stack env | comma-separated allowlist |
| `SESSION_TTL` | default `2160h` | session lifetime |
| `APP_TIMEZONE` | default `America/New_York` | IANA zone that decides "today" for due dates, reminder emails, and new-record defaults; tzdata is embedded in the binary |
| `ASSET_ROOT` | stack env (`/data/assets`) | receipt/photo storage root |
| `MAX_UPLOAD_BYTES` | stack env (`26214400`) | 25 MiB upload cap |
| `MAX_MULTIPART_BYTES` | stack env (default `134217728`) | 128 MiB total multipart request cap; supports four maximum-size receipts plus overhead |
| `REMINDER_EMAIL_ENABLED` | stack env (`true`), default `false` | overdue reminder emails and the weekly digest (due-soon reminders and new recalls) |
| `SMTP_PASSWORD` | secret `carma_smtp_password` via `SMTP_PASSWORD_FILE` | email reminders |

## CI/CD pipeline

Cloned from dined's single workflow (`ci.yml`), which is the simpler of the two
precedents and sufficient for one image:

```mermaid
sequenceDiagram
  participant GH as GitHub Actions (ubuntu-24.04-arm)
  participant TS as Tailscale (tag:ci)
  participant REG as registry.tail209cfc.ts.net
  participant CR as crystal1 post-receive hook
  participant SW as Swarm proxy_carma

  GH->>GH: test, vet, build, govulncheck
  GH->>TS: join tailnet, ping manager
  GH->>REG: buildx push carma:<shortsha> (linux/arm64/v8)
  GH->>CR: ssh git push -o image-digest=<built digest> -> /srv/git/carma-ci.git
  CR->>REG: wait for the manifest of that digest
  CR->>SW: run carma-migrate as one-shot Swarm job
  CR->>SW: docker service update --with-registry-auth proxy_carma
  CR->>SW: verify health, rollback on failure
```

Validation also runs for pull requests, manual dispatches, and at 09:17 UTC every
Monday. Image publication and deployment remain restricted to pushes on `main`, so
manual and scheduled validation runs cannot deploy.

The post-receive hook is noted's variant (migration job + rollback) adapted to a
single service. Stack file, Traefik routes, and the hook live in `home_swarm`, not
this repo - see [INFRA.md](INFRA.md).

## Local development

- `make db-up` - local Postgres via a small `compose.local.yml` (host port 5435 to
  avoid colliding with noted's 5434). `make run` needs it.
- `make migrate` - apply migrations locally.
- `make run` - the app against local Postgres, in dev auth mode with a seeded local
  user (noted's `AUTH_MODE=development` idea) so Google OAuth isn't required to hack
  on the UI.
- `make test-integration` - start local Postgres and run every test against it.
  `make test` skips the Postgres-backed tests unless `CARMA_TEST_DATABASE_URL` is
  set; CI sets it and fails those tests if it is missing.
- Assets land in `.local/carma-assets` (gitignored).
- `local.mk` (gitignored) for developer-specific overrides, per dined convention.
