# Carma infrastructure boundary

Carma builds one Go image containing the web server, migration runner, and
operator-only reminder runner. Production deploys it as `proxy_carma` behind
Traefik at `https://carma.bitofbytes.io`, with PostgreSQL and NFS-backed assets.

## Authoritative production operations

The `home_swarm` repository is the source of truth for production infrastructure.
Do not copy its stack, secret bootstrap, deployment, recovery, SMTP rotation, or
delivery-proof commands into this repository; duplicated operational instructions
drift from the deployed service.

Use these files in the current `home_swarm` default branch:

- `carma-stack.yml` for runtime environment, external secrets, replicas, volumes,
  health checks, and update policy.
- `README.md`, section “Carma CI and deployment,” for NAS/MailPlus and 1Password
  bootstrap, four-secret creation, first deployment, recipient-count-guarded email
  proof, credential rotation, and recovery.
- `carma/post-receive` for migration and immutable-image rollout behavior.
- `Makefile` target `deploy-carma` for manual bootstrap and recovery deployment.
- `traefik/dynamic/dynamic.routers-services.yml` for public routing.

Operational changes must be made and reviewed in `home_swarm`; this document only
describes the interface the application exposes to that deployment.

## Application deployment interface

The image provides:

- `/app/carma` — HTTP application on port 4700.
- `/app/carma-migrate` — embedded PostgreSQL migration runner.
- `/app/carma-reminders` — one-shot reminder evaluation/delivery runner supporting
  `--dry-run`, `--reminder-id <uuid>`, and the targeted proof guard
  `--require-recipient-count <n>`. The guard accepts a count only; it never accepts
  recipient address overrides.

Required secret-file interfaces are `DATABASE_URL_FILE`,
`AUTH_GOOGLE_CLIENT_ID_FILE`, `AUTH_GOOGLE_CLIENT_SECRET_FILE`, and—when reminder
email is enabled—`SMTP_PASSWORD_FILE`. SMTP passwords cannot be supplied directly
through `SMTP_PASSWORD`.

Reminder email is optional locally and requires PostgreSQL when enabled. Its
non-secret contract is `REMINDER_EMAIL_ENABLED`, `SMTP_HOST`, `SMTP_USERNAME`,
`SMTP_FROM_ADDRESS`, `SMTP_FROM_NAME`, `SMTP_TLS_MODE=implicit`, and `PUBLIC_URL`.
Incomplete enabled configuration fails startup. TLS certificate and hostname
verification must remain enabled.

## Validation boundary

Application changes run `make test`, `make lint`, and `make build`. Run
`make test-integration` with Docker available to apply migrations in an isolated
PostgreSQL schema, exercise notification suppression, roll migration 003 down and
back up, and verify advisory-lock contention and reacquisition.

Infrastructure changes render and validate the authoritative stack in
`home_swarm`; production deployment and live email proof are separate operator
actions and are never performed by application tests.
