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
| Secrets | `carma_database_url`, `carma_google_client_id`, `carma_google_client_secret`, (stretch) `carma_smtp_url` |
| Bare CI repo | `/srv/git/carma-ci.git` on crystal1 |
| GitHub environment | `crystal1` (already exists; same vars/secrets as dined) |

## App repo pieces (this repo, when implemented)

- **Dockerfile** - multi-stage: `golang:<ver>-alpine` builder compiling `carma` and
  `carma-migrate` with `CGO_ENABLED=0`, then `alpine` runtime, non-root user
  `carma` (uid/gid 10001 to match NFS ACLs, like noted), copies `static/`,
  `EXPOSE 4700`, HEALTHCHECK via `wget http://localhost:4700/health`.
- **Makefile** - `run`, `run-postgres`, `test`, `lint`, `migrate*`, `docker-build`,
  `docker-buildx` with defaults `REGISTRY=registry.tail209cfc.ts.net`,
  `PLATFORMS=linux/arm64/v8`, `PORT=4700`.
- **`.github/workflows/ci.yml`** - cloned from dined's
  `CI build -> push -> trigger deploy`:
  1. On push to `main`: checkout, `TAG=$(git rev-parse --short HEAD)`, Go setup,
     tests.
  2. Join Tailscale (`tailscale/github-action`, `tag:ci`, secret
     `TS_OAUTH_CLIENT_ID`, var `TS_AUDIENCE`), ping manager.
  3. Verify the registry resolves to Tailscale CGNAT space and `/v2/` answers.
  4. `docker login` (secrets `REGISTRY_USERNAME`/`REGISTRY_PASSWORD`), buildx push
     `carma:<TAG>` and `:latest`.
  5. SSH (secret `LOCAL_REPO_DEPLOY_KEY`, `LOCAL_KNOWN_HOSTS`) and force-push
     `HEAD:refs/heads/main` to `ssh://git@crystal1.tail209cfc.ts.net/srv/git/carma-ci.git`.
  - Runner `ubuntu-24.04-arm`, environment `crystal1`. All var/secret names match
    dined's, so the existing GitHub environment works as-is (only `IMAGE_REPO=carma`
    differs).

## home_swarm additions (separate repo, `/Users/daniel/projects/home_swarm`)

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
      AUTH_GOOGLE_ALLOWED_EMAILS: ${CARMA_AUTH_GOOGLE_ALLOWED_EMAILS}
      ASSET_ROOT: /data/assets
      MAX_UPLOAD_BYTES: "26214400"
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

(When email reminders ship: add `carma_smtp_url` to secrets and
`SMTP_URL_FILE: /run/secrets/carma_smtp_url` to environment.)

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

1. Only act on `refs/heads/main`; take a flock on `/srv/git/.proxy_carma.lock`.
2. Wait (up to ~60s) for the `carma:<sha>` manifest in the registry; pin by digest.
3. Run `/app/carma-migrate` as a one-shot Swarm replicated-job with the
   `carma_database_url` secret; abort on failure.
4. `docker service update --with-registry-auth --image <digest> proxy_carma` with
   `io.bitofbytes.deploy.*` labels.
5. Verify the rollout; `docker service rollback` on failure.

Install as `/srv/git/carma-ci.git/hooks/post-receive`.

### 4. Makefile target

`make deploy-carma TAG=<sha>` → run migrate job, then
`docker stack deploy --with-registry-auth -c carma-stack.yml proxy` (mirror of
`deploy-noted`). Used for the first manual deploy and for recovery.

## One-time bootstrap checklist

Ordered; items 1-6 can happen before any code exists.

1. **DNS** - add `carma.bitofbytes.io` pointing at the home WAN IP (same as
   dined/noted) so Traefik can complete the HTTP-01 ACME challenge.
2. **Postgres (bahamut)** - create role `carma` with a generated password and
   database `carma` owned by it, on the existing instance at `:8432`. The nightly
   cluster backup job discovers new databases automatically - no backup changes
   needed.
3. **NFS share (bahamut DSM)** - create shared folder `carma-assets`, export via
   NFS to crystal1-4, permissions compatible with container uid `10001` (or the
   "map all users to admin" squash used for noted-assets). Covered by the existing
   Hyper Backup policy for shared folders.
4. **Google OAuth client** (Google Cloud console, same project as dined/noted) -
   authorized origin `https://carma.bitofbytes.io`, redirect URI
   `https://carma.bitofbytes.io/api/auth/google/callback`.
5. **Swarm secrets (crystal1)**:

   ```bash
   printf 'postgres://carma:<pw>@192.168.1.2:8432/carma?sslmode=disable' | docker secret create carma_database_url -
   printf '<client-id>'     | docker secret create carma_google_client_id -
   printf '<client-secret>' | docker secret create carma_google_client_secret -
   ```

6. **Bare CI repo (crystal1)** - `git init --bare /srv/git/carma-ci.git`, owned by
   the `git` user (already in the docker group and logged into the registry),
   install `carma/post-receive` hook, `chmod +x`.
7. **GitHub repo + environment** - create `bitofbytes-io/carma`; in the `crystal1`
   environment the existing vars/secrets (`IMAGE_REG`, `MANAGER_HOST`,
   `TS_AUDIENCE`, `TS_OAUTH_CLIENT_ID`, `REGISTRY_USERNAME`, `REGISTRY_PASSWORD`,
   `LOCAL_REPO_DEPLOY_KEY`, `LOCAL_KNOWN_HOSTS`) carry over; set repo var
   `IMAGE_REPO=carma` and `MANAGER_REPO_PATH=/srv/git/carma-ci.git`.
8. **Migrations** - first deploy's migrate job creates the schema (no manual goose
   step, unlike dined).
9. **First deploy** - the very first CI run can't `service update` a service that
   doesn't exist yet: run `make deploy-carma TAG=<sha>` once from `home_swarm` on
   crystal1 to create `proxy_carma`, then re-run the workflow. (Same
   chicken-and-egg as noted's bootstrap.)
10. **Smoke test** - `https://carma.bitofbytes.io/health` returns 200 with a valid
    LE cert; Google login works for an allowlisted account.

## Stretch: email reminders

- Create secret `carma_smtp_url` (e.g. a Gmail app password:
  `smtp://user%40gmail.com:<app-pw>@smtp.gmail.com:587`, or the household Synology
  mail relay).
- Add the secret + `SMTP_URL_FILE` env to `carma-stack.yml` and redeploy.
- No other infra changes; the scheduler activates itself when the secret is
  present.
