# Carma - Infrastructure and Bootstrap

Carma reuses the established household infrastructure verbatim: GitHub Actions
builds ARM64 images over Tailscale, pushes them to the private registry, and
triggers deployment by SSH-pushing to a bare git repo on crystal1 whose
post-receive hook runs migrations and rolls the Swarm service. Traefik terminates
TLS with Let's Encrypt and routes by hostname from file-based config.

Everything below follows the naming conventions already used by dined and noted.

## Names at a glance

| Item | Value |
|---|---|
| Public hostname | `carma.bitofbytes.io` |
| GitHub repo | `bitofbytes-io/carma` |
| Image | `registry.tail209cfc.ts.net/carma:<7-char-sha>` (platform `linux/arm64/v8`) |
| Swarm stack / service | stack `proxy` → service `proxy_carma` |
| App port | `4700` |
| Postgres | bahamut `192.168.1.2:8432`, role + db `carma` |
| NFS share | bahamut `/volume1/carma-assets` → volume `carma_assets` → `/data/assets` |
| Secrets | `carma_database_url`, `carma_google_client_id`, `carma_google_client_secret`, `carma_smtp_password` |
| Bare CI repo | `/srv/git/carma-ci.git` on crystal1 |
| GitHub environment | Carma repository-specific `crystal1` environment |

## App repo pieces

- **Dockerfile** - multi-stage: `golang:<ver>-alpine` builder compiling `carma` and
  `carma-migrate` with `CGO_ENABLED=0`, then `alpine` runtime, non-root user
  `carma` (uid/gid 10001 to match NFS ACLs, like noted), copies `static/`,
  `EXPOSE 4700`, HEALTHCHECK via `wget http://localhost:4700/health`.
- **Makefile** - `run`, `run-postgres`, `test`, `lint`, `migrate*`, `docker-build`,
  `docker-buildx` with defaults `REGISTRY=registry.tail209cfc.ts.net`,
  `PLATFORMS=linux/arm64/v8`, `PORT=4700`.
- **`.github/workflows/ci.yml`** - follows the hardened Noted request/status
  deployment protocol, reduced to one image and service:
  1. On pull requests and pushes to `main`, run only `make test lint build`.
     Main deploy jobs are serialized and derive an explicit seven-character tag.
  2. Join Tailscale (`tailscale/github-action`, `tag:ci`, secret
     `TS_OAUTH_CLIENT_ID`, var `TS_AUDIENCE`), ping manager.
  3. Resolve and validate one registry Tailscale IP, then pin that same address for
     the probe, registry login, and BuildKit push.
  4. `docker login` (secrets `REGISTRY_USERNAME`/`REGISTRY_PASSWORD`), buildx push
     `carma:<TAG>` and `:latest`.
  5. SSH using hardened key material and atomically fast-forward `main` together
     with a unique `refs/deploy/requests/<run>-<attempt>` ref. Fetch
     `refs/deploy/status` and require the exact full revision, succeeded state, and
     all three `proxy_carma` replicas before cleaning up the request ref.
  - Runner `ubuntu-24.04-arm`, environment `crystal1`. Create this environment in
    the Carma repository; GitHub environments and their values do not inherit from
    dined. Set repository-scoped `IMAGE_REPO=carma`, then populate every other named
    variable and secret securely for this repository.

## home_swarm deployment files (separate repo, `/home/daniel/projects/home_swarm` on crystal1)

### 1. `carma-stack.yml`

Modeled on `dined-stack.yml` plus noted's NFS volume block:

```yaml
networks:
  proxy:
    external: true
    name: proxy

services:
  carma:
    image: ${REGISTRY:-registry.tail209cfc.ts.net}/carma:${TAG?Set TAG to the image tag}
    environment:
      APP_ENV: production
      DATA_STORE: postgres
      PORT: "4700"
      DATABASE_URL_FILE: /run/secrets/carma_database_url
      AUTH_GOOGLE_CLIENT_ID_FILE: /run/secrets/carma_google_client_id
      AUTH_GOOGLE_CLIENT_SECRET_FILE: /run/secrets/carma_google_client_secret
      AUTH_GOOGLE_REDIRECT_URL: https://carma.bitofbytes.io/api/auth/google/callback
      AUTH_GOOGLE_ALLOWED_EMAILS: ${CARMA_AUTH_GOOGLE_ALLOWED_EMAILS:?required}
      ASSET_ROOT: /data/assets
      MAX_UPLOAD_BYTES: "26214400"
      MAX_MULTIPART_BYTES: "134217728"
    deploy:
      replicas: 3
      placement:
        preferences:
          - spread: node.hostname
      update_config:
        parallelism: 1
        delay: 10s
        failure_action: rollback
        order: start-first
    networks:
      - proxy
    secrets:
      - carma_database_url
      - carma_google_client_id
      - carma_google_client_secret
    volumes:
      - carma_assets:/data/assets

volumes:
  carma_assets:
    driver: local
    driver_opts:
      type: nfs
      o: addr=192.168.1.2,nfsvers=4.1,rw,hard,timeo=600,retrans=2
      device: :/volume1/carma-assets

secrets:
  carma_database_url:
    external: true
  carma_google_client_id:
    external: true
  carma_google_client_secret:
    external: true
```

Email reminders use separate SMTP settings and mount the password-only external
secret `carma_smtp_password` at `/run/secrets/carma_smtp_password`.

### 2. Traefik router (`traefik/dynamic/dynamic.routers-services.yml`)

```yaml
# routers
carma:
  rule: Host(`carma.bitofbytes.io`)
  entryPoints: [websecure]
  tls:
    certResolver: le
  service: carma-svc
  middlewares:
    - secure-headers
    - private-app-limit

# services
carma-svc:
  loadBalancer:
    servers:
      - url: "http://carma:4700"
```

No Swarm labels; file provider only, matching every other app.

### 3. `carma/post-receive` hook

Noted's hook adapted to a single service:

1. Act on `refs/heads/main` and `refs/deploy/requests/*`, take a flock on
   `/srv/git/.proxy_carma.lock`, then re-read current `main` so queued pushes
   coalesce safely.
2. Wait for the `carma:<7-char-sha>` manifest in the registry and require its
   immutable digest.
3. Run `/app/carma-migrate` as a one-shot Swarm replicated-job with the
   `carma_database_url` secret; abort on failure.
4. `docker service update --with-registry-auth --image <digest> proxy_carma` with
   `io.bitofbytes.deploy.*` labels.
5. Verify the exact full revision and three running replicas. On failure, restore
   the captured full service spec and publish a failed JSON status commit at
   `refs/deploy/status`; publish succeeded status only after convergence.

Install as `/srv/git/carma-ci.git/hooks/post-receive`.

### 4. Makefile target

`CARMA_AUTH_GOOGLE_ALLOWED_EMAILS='<approved-emails>' make deploy-carma TAG=<sha>`
checks Traefik and all three external secrets, then runs the migrate job followed by
`docker stack deploy --with-registry-auth -c carma-stack.yml proxy` (mirror of
`deploy-noted`). Used for the first manual deploy and for recovery.

## One-time bootstrap checklist

Ordered; items 1-6 can happen before any code exists.

1. **DNS** - add `carma.bitofbytes.io` in Namecheap pointing at the home WAN IP
   (same record shape as dined/noted) so Traefik can complete the HTTP-01 ACME
   challenge, and add a local UniFi override to `192.168.10.2`.
2. **Postgres (bahamut)** - create role `carma` with a generated password and
   database `carma` owned by it, on the existing instance at `:8432`. Explicitly
   confirm the nightly cluster backup includes the new database.
3. **NFS share (bahamut DSM)** - create shared folder `carma-assets`, export via
   NFS to crystal1-4, permissions compatible with container uid `10001` (or the
   "map all users to admin" squash used for noted-assets). Restrict NFS TCP 2049
   to crystal1-4 and explicitly confirm Hyper Backup includes the new share.
4. **Google OAuth client** (Google Cloud console, same project as dined/noted) -
   authorized origin `https://carma.bitofbytes.io`, redirect URI
   `https://carma.bitofbytes.io/api/auth/google/callback`.
5. **Swarm secrets (crystal1)** - use hidden prompts and stdin so values never
   appear in command arguments or shell history. The guard refuses to overwrite an
   existing secret:

   ```bash
   set -euo pipefail

   for secret_name in carma_database_url carma_google_client_id carma_google_client_secret; do
     if docker secret inspect "$secret_name" >/dev/null 2>&1; then
       printf 'Refusing to overwrite existing secret: %s\n' "$secret_name" >&2
       exit 1
     fi
   done

   clear_carma_secret_inputs() {
     unset carma_db_password carma_db_password_encoded carma_database_url
     unset carma_google_client_id carma_google_client_secret carma_urlencoded
   }
   trap clear_carma_secret_inputs EXIT

   carma_urlencode() {
     local LC_ALL=C input="$1" output="" character encoded index
     for ((index = 0; index < ${#input}; index++)); do
       character="${input:index:1}"
       case "$character" in
         [a-zA-Z0-9.~_-]) output+="$character" ;;
         *) printf -v encoded '%%%02X' "'$character"; output+="$encoded" ;;
       esac
     done
     carma_urlencoded="$output"
   }

   read -rsp 'Carma database password: ' carma_db_password
   printf '\n'
   test -n "$carma_db_password"
   carma_urlencode "$carma_db_password"
   carma_db_password_encoded="$carma_urlencoded"
   carma_database_url="postgres://carma:${carma_db_password_encoded}@192.168.1.2:8432/carma?sslmode=disable"
   printf '%s' "$carma_database_url" | docker secret create carma_database_url - >/dev/null
   unset carma_db_password carma_db_password_encoded carma_database_url carma_urlencoded

   read -rsp 'Google OAuth client ID: ' carma_google_client_id
   printf '\n'
   test -n "$carma_google_client_id"
   printf '%s' "$carma_google_client_id" | docker secret create carma_google_client_id - >/dev/null
   unset carma_google_client_id

   read -rsp 'Google OAuth client secret: ' carma_google_client_secret
   printf '\n'
   test -n "$carma_google_client_secret"
   printf '%s' "$carma_google_client_secret" | docker secret create carma_google_client_secret - >/dev/null
   unset carma_google_client_secret

   trap - EXIT
   clear_carma_secret_inputs
   ```

   If only part of the set is created before an error, inspect and resolve that state
   deliberately; rerunning this block stops instead of replacing existing secrets.

6. **Bare CI repo (crystal1)** - `git init --bare /srv/git/carma-ci.git`, owned by
   the `git` user (already in the docker group and logged into the registry),
   install `carma/post-receive` hook, `chmod +x`.
7. **GitHub repo + environment** - create a Carma repository-specific `crystal1`
   environment; it does not inherit dined's configuration. Set the repository-scoped
   variable `IMAGE_REPO=carma`. Populate `IMAGE_REG`, `MANAGER_HOST`,
   `MANAGER_REPO_PATH=/srv/git/carma-ci.git`, and `TS_AUDIENCE`, plus the secrets
   `TS_OAUTH_CLIENT_ID`, `REGISTRY_USERNAME`, `REGISTRY_PASSWORD`,
   `LOCAL_REPO_DEPLOY_KEY`, and `LOCAL_KNOWN_HOSTS`, securely in the Carma repository.
8. **Migrations** - first deploy's migrate job creates the schema (no manual goose
   step, unlike dined).
9. **First deploy** - the very first CI run can't `service update` a service that
   doesn't exist yet: after that run publishes the image, run
   `CARMA_AUTH_GOOGLE_ALLOWED_EMAILS='<approved-emails>' make deploy-carma TAG=<7-char-sha>`
   once from `home_swarm` on crystal1 to create `proxy_carma`, then re-run the same
   workflow. Its unique request ref triggers the hook even though bare-repo `main`
   already points at that commit.
10. **Smoke test** - `https://carma.bitofbytes.io/health` returns 200 with a valid
    LE cert; Google login works for an allowlisted account.
11. **Monitoring** - add an Uptime Kuma HTTP monitor for
    `https://carma.bitofbytes.io/health` using the existing notification channel.

## Email reminders

- Use the dedicated non-admin MailPlus account `carma@bitofbytes.io` and store its
  unique password outside the repository.
- Submit to `mail.bitofbytes.io:465` with implicit TLS. Certificate chain and
  hostname verification must remain enabled.
- Never put the password in a URL, environment variable, command argument, or log.
- Use `/app/carma-reminders --dry-run --reminder-id <uuid>` for a read-only
  eligibility check. Remove `--dry-run` for the controlled send, confirm inbox
  delivery plus one audit row, then rerun to prove 30-day suppression.
- Rotate through a temporary versioned Swarm secret targeted to the same container
  path, recreate the fixed-name secret, switch back, and remove the temporary secret
  only after every replica and a targeted proof are healthy.
