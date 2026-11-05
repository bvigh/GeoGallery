# Deploying GeoGallery on Hetzner

This guide sets up a hosted GeoGallery instance with these parts:

- **One Hetzner Cloud server.** It runs PostGIS, the backend, the frontend (nginx) and Caddy, which terminates TLS.
- **Hetzner Object Storage**, which replaces MinIO for the media files.
- **Logto Cloud** for sign-in. See [auth.md](auth.md).
- **Namecheap** DNS for your domain.
- **Codeberg Forgejo Actions** for CI and deployments that you trigger.

Throughout this guide, replace `example.com` with your domain, `geo.example.com` with the app hostname and `auth.example.com` with the Logto custom domain.

```
Browser ──https──▶ Caddy :443 ──▶ frontend (nginx) ──/api──▶ backend ──▶ PostGIS
   │                                                            │
   │  presigned GET (15 min)                                    │ PutObject / DeleteObject
   └────────────────────────▶ Hetzner Object Storage ◀──────────┘
   │
   └──── login redirect ────▶ Logto (auth.example.com)
```

Browsers download media **directly from Object Storage** through presigned URLs. Media traffic therefore counts against the Object Storage egress quota, not against the server's traffic.

---

## 1. Choosing the Hetzner products

Prices are monthly, exclude VAT and follow the price list in effect since 15 June 2026. Check them on hetzner.com before you order.

| Product | Choice | Approx. price | Why |
|---|---|---|---|
| Cloud server | **CX33**: 4 vCPU (shared, x86), 8 GB RAM, 80 GB disk | €8.49 + IPv4 | ffmpeg transcoding needs the CPU. Uploads are spooled to disk 2–3 times, so the disk must hold several copies of your largest video. |
| Server backups | Enable on the server | +20% of the server price | Daily whole-disk snapshots, 7 kept. |
| Object Storage | 1 bucket, location **FSN1** (or NBG1) | €6.49 | Includes 1 TB of storage and 1 TB of egress. Further egress costs €1/TB. |
| Cloud Firewall | 1 firewall | free | |

**Server location:** put the server and the bucket in the same location, for example both in Falkenstein (FSN1). Traffic between the server and Object Storage inside `eu-central` is free.

**Why x86 and not ARM (CAX):** the `postgis/postgis:16-3.4` image is published for **amd64 only**. CAX servers would need a different PostGIS image, such as `imresamu/postgis`, and arm64 builds of the backend and frontend images.

**If CX is sold out:** hetzner.com currently marks the "Cost-Optimized" line (CX) as unavailable at times. In that case:
- Try another location in the same zone (NBG1 or HEL1).
- Or take **CAX21** (€10.49, ARM). This needs the PostGIS image swap described above.
- **CPX22/CPX32** (€19.49 / €35.49) also work, but cost 2–4 times as much since the June 2026 increase.

**Expected total:** about €17–18 per month, excluding VAT. Logto Cloud's free plan costs nothing.

---

## 2. Order of operations

Users are keyed by the OIDC issuer string (see [auth.md → Issuer stability](auth.md#issuer-stability)). Do the steps in this order:

1. Object Storage bucket and credentials (§3)
2. Server and firewall (§4)
3. DNS records (§5)
4. Logto application and **custom domain**, which must be active before step 6 (§6)
5. Server software and configuration (§7–8)
6. First start, then `server invite --admin` (§9)

---

## 3. Object Storage

1. In the Hetzner Console, go to **Object Storage → Create bucket**.
   - Location: `FSN1`.
   - Name: bucket names are unique across all of Hetzner, so `geogallery` is probably taken. Use something like `geogallery-<you>`. The bucket name is not part of `storage_key`, so it can differ from your local `geogallery` bucket.
   - Visibility: **Private**.
2. Go to **Security → S3 credentials → Generate credentials**. Copy the access key and the secret key now: the secret is shown only once. The credentials apply to the whole project.
3. **Set CORS on the bucket. This step is required.** `Panorama360.tsx` and `Video360.tsx` load media with `crossOrigin = "anonymous"` so that WebGL can use it as a texture. Without a CORS rule, 360° photos and videos fail to render, while flat photos still show.

   Save this as `cors.json`:

   ```json
   {
     "CORSRules": [
       {
         "AllowedOrigins": ["https://geo.example.com"],
         "AllowedMethods": ["GET", "HEAD"],
         "AllowedHeaders": ["*"],
         "ExposeHeaders": ["Content-Length", "Content-Range", "Accept-Ranges", "ETag"],
         "MaxAgeSeconds": 3600
       }
     ]
   }
   ```

   Apply it from your laptop with the AWS CLI:

   ```bash
   export AWS_ACCESS_KEY_ID=...  AWS_SECRET_ACCESS_KEY=...
   aws s3api put-bucket-cors --bucket geogallery-<you> \
     --cors-configuration file://cors.json \
     --endpoint-url https://fsn1.your-objectstorage.com --region fsn1
   aws s3api get-bucket-cors --bucket geogallery-<you> \
     --endpoint-url https://fsn1.your-objectstorage.com --region fsn1
   ```

4. **A second bucket `geogallery-<you>-backups`** for the database dumps in §11, with the expiry rule described there.

**The bucket is the only copy of your originals.** Hetzner does not back up Object Storage. Do one of the following:
- Keep the original files somewhere else.
- Or periodically mirror the bucket to another location, for example `rclone sync` to a Hetzner Storage Box or a disk at home.

The backend's settings for Hetzner:

| Variable | Value |
|---|---|
| `S3_ENDPOINT` | `https://fsn1.your-objectstorage.com` |
| `S3_REGION` | `fsn1` |
| `S3_BUCKET` | `geogallery-<you>` |
| `S3_ACCESS_KEY`, `S3_SECRET_KEY` | the generated credentials |

`storage.NewClient` hard-codes path-style addressing (`UsePathStyle = true`), which produces URLs like `https://fsn1.your-objectstorage.com/<bucket>/<key>`. Hetzner documents virtual-hosted style, but its Ceph-based endpoint also accepts path style. If presigned URLs or uploads fail, see [§14](#14-known-gaps-to-fix-in-the-repo).

---

## 4. Server and firewall

1. Upload your SSH public key: **Security → SSH keys**.
2. Create a **Firewall** (**Firewalls → Create firewall**) with these inbound rules:

   | Protocol | Port | Source |
   |---|---|---|
   | TCP | 22 | your home IP, if it is static; otherwise any |
   | TCP | 80 | any (needed for ACME HTTP challenges and the redirect to https) |
   | TCP | 443 | any |
   | UDP | 443 | any (HTTP/3) |

   Docker writes its own iptables rules and bypasses `ufw`. The Hetzner Cloud Firewall runs outside the VM, so it is the dependable layer. In addition, the production compose file publishes no ports except Caddy's.
3. Create the **server**:
   - Location: FSN1, the same as the bucket.
   - Image: **Ubuntu 24.04**.
   - Type: **CX33**.
   - Networking: public IPv4 and IPv6.
   - SSH key: yours.
   - Firewall: the one from step 2.
   - **Backups: on.**
   - Name: `geogallery`.
4. Note the IPv4 and IPv6 addresses.

---

## 5. DNS at Namecheap

Keep Namecheap as the DNS host; moving DNS to Hetzner is not necessary. Go to **Domain List → Manage → Advanced DNS → Host records** and add:

| Type | Host | Value | TTL |
|---|---|---|---|
| A | `geo` | server IPv4 | Automatic |
| AAAA | `geo` | server IPv6 | Automatic |
| CNAME | `auth` | `domains.logto.app` | Automatic |

- To serve the app on the bare domain instead, use host `@` and set `PUBLIC_URL=https://example.com`.
- Remove Namecheap's default parking records (`CNAME www → parkingpage…`, the URL redirect record) if they conflict.
- The `auth` record is a CNAME, not an IP. Enter only the subdomain as Host, not `auth.example.com`. Namecheap adds the trailing dot to the value itself. `domains.logto.app` is the usual target; if Logto's **Settings → Domains** table shows a different CNAME value, use that one.
- Create the `auth` record **before** adding the domain in Logto (§6). Until it resolves, Logto rejects the domain with "custom hostname does not CNAME to this zone".
- Advanced DNS only applies while the domain uses **Namecheap BasicDNS** (Manage → Nameservers). If it uses other nameservers, add the records there instead. On Cloudflare, set the `auth` record to **DNS only**, not proxied.
- If the domain has CAA records, they must allow Logto's certificate authority, or SSL activation stalls after DNS verifies.

Check propagation with `dig +short geo.example.com` and `dig +short auth.example.com CNAME`. The second must print `domains.logto.app.`

---

## 6. Logto

Follow [auth.md → Logto Cloud](auth.md#logto-cloud-step-by-step). It covers:

- The application type: **Traditional web**.
- The redirect URI: `https://geo.example.com/api/auth/callback`.
- The post sign-out redirect URI: `https://geo.example.com/`.
- **The custom domain:** once the `auth` CNAME from §5 resolves, add `auth.example.com` under **Settings → Domains**. Wait until both DNS and SSL show as active. Logto re-checks automatically; this usually takes minutes but can take up to 24 hours.
- Social connectors, account linking and turning off registration.

Then verify the issuer:

```bash
curl -s https://auth.example.com/oidc/.well-known/openid-configuration | jq -r .issuer
# → https://auth.example.com/oidc
```

Check that Logto's free plan includes a custom domain. If it does not, you can start on `https://<tenant>.logto.app/oidc`. Switching later then requires `server rebind-issuer`, which works because Logto keeps the same `sub` values.

---

## 7. Preparing the server

Log in as root once and set up a non-root user, the firewall basics and Docker:

```bash
ssh root@<ipv4>

apt update && apt -y full-upgrade
apt -y install unattended-upgrades fail2ban git jq rclone
dpkg-reconfigure -plow unattended-upgrades

# Docker Engine and the compose plugin from Docker's apt repository
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
  https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
  > /etc/apt/sources.list.d/docker.list
apt update && apt -y install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Container logs go to journald, which deletes them after 14 days (see "Log retention" below)
mkdir -p /etc/systemd/journald.conf.d
cat > /etc/systemd/journald.conf.d/10-retention.conf <<'EOF'
[Journal]
MaxFileSec=1day
MaxRetentionSec=13day
SystemMaxUse=2G
ForwardToSyslog=no
EOF
systemctl restart systemd-journald
echo '{ "log-driver": "journald" }' > /etc/docker/daemon.json
systemctl restart docker

# 4 GB swap: protects against OOM when ffmpeg and an image build run at the same time
fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab

# Deploy user. Membership in the docker group is root-equivalent: protect this account.
adduser --disabled-password --gecos "" deploy
usermod -aG docker deploy
mkdir -p /home/deploy/.ssh && cp ~/.ssh/authorized_keys /home/deploy/.ssh/
chown -R deploy:deploy /home/deploy/.ssh && chmod 700 /home/deploy/.ssh

mkdir -p /srv/geogallery && chown deploy:deploy /srv/geogallery
```

Harden SSH with `/etc/ssh/sshd_config.d/10-hardening.conf`:

```
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
```

Run `systemctl reload ssh`. **Before you close the root session, test `ssh deploy@<ipv4>` in a second terminal.**

**Log retention.** The privacy policy (`frontend/public/privacy.html`) promises that server logs are deleted after 14 days at the latest. Caddy writes no access log, but nginx logs each request with the client IP (from Caddy's `X-Forwarded-For`), and Docker's default `json-file` driver never deletes anything. Docker can only rotate its logs by size, not by age, so the setup above sends container logs to journald instead:
- `MaxFileSec=1day` starts a new journal file every day. journald deletes a whole file once its **oldest** entry is older than `MaxRetentionSec`, so 13 days keeps every entry for at most about 14.
- `ForwardToSyslog=no` stops rsyslog from copying the entries into `/var/log/syslog`, which logrotate keeps for 4 weeks.
- `SystemMaxUse=2G` caps disk use; on a busy instance the oldest entries go even sooner.

`docker compose logs` works the same with journald. The log driver only applies to containers created after the change: on a server that already runs the stack, run `docker compose up -d --force-recreate`. Check with `docker inspect -f '{{.HostConfig.LogConfig.Type}}' $(docker compose ps -q frontend)` (prints `journald`) and `journalctl --disk-usage`.

Clone the repository as `deploy`:

```bash
ssh deploy@<ipv4>
git clone https://codeberg.org/bvigh/GeoGallery.git /srv/geogallery
```

---

## 8. Production configuration

The development `docker-compose.yml` does not fit production:
- It hard-codes `S3_ENDPOINT: http://minio:9000`.
- The backend waits for MinIO.
- It publishes the database and backend ports.

Production therefore uses a separate compose file. Add these files to the repository so that CI deploys them too.

### `docker-compose.prod.yml`

```yaml
services:
  db:
    image: postgis/postgis:16-3.4
    restart: unless-stopped
    environment:
      POSTGRES_DB: ${POSTGRES_DB}
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - db_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER}"]
      interval: 5s
      timeout: 5s
      retries: 10

  backend:
    build: ./backend
    restart: unless-stopped
    depends_on:
      db:
        condition: service_healthy
    environment:
      DATABASE_URL: postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@db:5432/${POSTGRES_DB}?sslmode=disable
      S3_ENDPOINT: ${S3_ENDPOINT}
      S3_ACCESS_KEY: ${S3_ACCESS_KEY}
      S3_SECRET_KEY: ${S3_SECRET_KEY}
      S3_BUCKET: ${S3_BUCKET}
      S3_REGION: ${S3_REGION}
      INGEST_WATCH_DIR: /ingest
      LOG_FORMAT: json
      PUBLIC_URL: ${PUBLIC_URL}
      OIDC_ISSUER_URL: ${OIDC_ISSUER_URL}
      OIDC_DISCOVERY_URL: ${OIDC_ISSUER_URL}
      OIDC_CLIENT_ID: ${OIDC_CLIENT_ID}
      OIDC_CLIENT_SECRET: ${OIDC_CLIENT_SECRET}
      SESSION_TTL_HOURS: ${SESSION_TTL_HOURS:-720}
      CAMERA_TIMEZONE: ${CAMERA_TIMEZONE}
    volumes:
      - ./ingest:/ingest
    # no ports: only nginx reaches the backend, over the compose network

  frontend:
    build: ./frontend
    restart: unless-stopped
    depends_on:
      - backend

  caddy:
    image: caddy:2
    restart: unless-stopped
    depends_on:
      - frontend
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    environment:
      SITE_DOMAIN: ${SITE_DOMAIN}
    volumes:
      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config

volumes:
  db_data:
  caddy_data:
  caddy_config:
```

### `deploy/Caddyfile`

```
{$SITE_DOMAIN} {
	encode zstd gzip
	reverse_proxy frontend:80
	# No `log` directive on purpose: Caddy then writes no access log, which keeps invite
	# tokens and OIDC codes in query strings out of the logs (as nginx does for /api/auth/).
}
```

Caddy obtains and renews the Let's Encrypt certificate itself once DNS points at the server. It sets no request body limit, so nginx's `client_max_body_size 4g` stays the effective upload limit.

### `/srv/geogallery/.env` (on the server only, never committed)

```bash
COMPOSE_FILE=docker-compose.prod.yml    # plain `docker compose ...` then uses the prod file
COMPOSE_PROJECT_NAME=geogallery

SITE_DOMAIN=geo.example.com
PUBLIC_URL=https://geo.example.com

POSTGRES_DB=geogallery
POSTGRES_USER=geogallery
POSTGRES_PASSWORD=<openssl rand -hex 24>

S3_ENDPOINT=https://fsn1.your-objectstorage.com
S3_REGION=fsn1
S3_BUCKET=geogallery-<you>
S3_ACCESS_KEY=<hetzner access key>
S3_SECRET_KEY=<hetzner secret key>

OIDC_ISSUER_URL=https://auth.example.com/oidc
OIDC_CLIENT_ID=<Logto App ID>
OIDC_CLIENT_SECRET=<Logto App secret>

# The zone camera clocks show. Required: over https the backend refuses to start without it.
# Without it, every photo time without a usable GPS timestamp would be read in the container's UTC.
CAMERA_TIMEZONE=Europe/Warsaw
```

Run `chmod 600 .env`. Because `PUBLIC_URL` is https, the backend refuses to start with the dev OIDC secret or `minioadmin` credentials, so a forgotten value fails loudly.

---

## 9. First start

```bash
cd /srv/geogallery
docker compose up -d --build
docker compose logs -f backend      # wait for migrations, OIDC discovery and "listening"
curl -fsS https://geo.example.com/api/health
```

Create the admin account. Only do this once the Logto custom domain is final:

```bash
docker compose exec backend ./server invite --admin
```

Open the printed link and sign in. The Logto user who signs in becomes the admin and owns any existing data. Invite other people from the Admin page.

**Smoke test:**
1. Upload a flat photo and check that it appears on the map.
2. Upload a 360° photo and check that it renders. This verifies CORS.
3. Upload a short video and check that it plays.
4. Log out and check that you return to `/`.

---

## 10. Moving existing local data (optional)

Do this **instead of** the first start in §9, before the backend has run against the new database.

```bash
# On your laptop: dump the local database and copy it to the server
docker compose exec -T db pg_dump -U geogallery -Fc geogallery > geogallery.dump
scp geogallery.dump deploy@geo.example.com:/srv/geogallery/

# On your laptop: copy the objects from local MinIO to Hetzner (rclone config: two s3 remotes)
#   [local]   provider = Minio, endpoint = http://localhost:9000, access_key_id = minioadmin, ...
#   [hetzner] provider = Other, endpoint = https://fsn1.your-objectstorage.com, region = fsn1, ...
rclone sync local:geogallery hetzner:geogallery-<you> --progress
rclone check local:geogallery hetzner:geogallery-<you>

# On the server: start only the database, restore the dump, then start everything
docker compose up -d db
docker compose exec -T db pg_restore -U geogallery -d geogallery --no-owner < geogallery.dump
docker compose up -d --build
```

`storage_key` values are bucket-relative, so the different bucket name does not matter.

**Caveat:** if you already claimed the Owner account locally through the dev Keycloak, the dump contains a Keycloak-bound user. `rebind-issuer` cannot move it to Logto because the `sub` values differ. Use a dump taken before you claimed the account, or add a command that re-opens the Owner claim.

---

## 11. Backups

There are three layers:

1. **Hetzner server backups** (enabled in §4). Daily, 7 kept. They are crash-consistent, so treat them as the "restore the whole box" option.
2. **Nightly `pg_dump` to Object Storage.** This is the backup to rely on for the database.
3. **Media.** See the note in §3: keep the originals elsewhere, or mirror the bucket.

`/srv/geogallery/backup.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd /srv/geogallery
mkdir -p backups
f="backups/geogallery-$(date -u +%Y%m%dT%H%M%SZ).dump"
docker compose exec -T db pg_dump -U geogallery -Fc geogallery > "$f"
rclone copy "$f" hetzner:geogallery-<you>-backups/db/
find backups -name '*.dump' -mtime +14 -delete
```

Configure the `hetzner` remote with `rclone config` as the `deploy` user. Then add the job with `crontab -e`:

```
30 3 * * * /srv/geogallery/backup.sh >> /srv/geogallery/backups/backup.log 2>&1
```

**Expire old dumps after 30 days.** The privacy policy promises that deleted data leaves the backups within 30 days. The script deletes local dumps after 14 days, and Hetzner keeps its server backups for 7. The copies in the bucket need a lifecycle rule. Save this as `lifecycle.json`:

```json
{
  "Rules": [
    {
      "ID": "expire-db-dumps",
      "Status": "Enabled",
      "Filter": { "Prefix": "db/" },
      "Expiration": { "Days": 28 }
    }
  ]
}
```

S3 expiry counts days from the upload, rounds up to the next midnight UTC and then deletes in a background job, so 28 days keeps every dump below 30. Apply the rule from your laptop, as for CORS in §3:

```bash
aws s3api put-bucket-lifecycle-configuration --bucket geogallery-<you>-backups \
  --lifecycle-configuration file://lifecycle.json \
  --endpoint-url https://fsn1.your-objectstorage.com --region fsn1
aws s3api get-bucket-lifecycle-configuration --bucket geogallery-<you>-backups \
  --endpoint-url https://fsn1.your-objectstorage.com --region fsn1
```

Leave versioning off on this bucket: with versioning on, an expired dump only becomes a noncurrent version and stays stored. If you mirror the media bucket (§3), use `rclone sync`, which deletes files from the mirror that were deleted in GeoGallery. `rclone copy` keeps them forever.

**Test a restore once.** Load a dump into a scratch database: `docker compose exec db createdb -U geogallery restore_test`, then `pg_restore` into it.

---

## 12. Manual updates

```bash
cd /srv/geogallery
./backup.sh                          # migrations run on startup, so back up first
git pull
docker compose up -d --build
docker image prune -f
```

Bulk ingest on the server:

```bash
rsync -av --progress ~/Pictures/trip/ deploy@geo.example.com:/srv/geogallery/ingest/
ssh deploy@geo.example.com 'cd /srv/geogallery && docker compose exec backend ./server scan'
```

---

## 13. CI and deployments (Codeberg Forgejo Actions)

The plan is in three stages, each more automated than the last:

| Stage | What runs | Trigger |
|---|---|---|
| A | Tests, lint and build | every push and PR (automatic) |
| B | Deploy: SSH to the server, which pulls, builds and restarts | pushing a `v*` tag, or the manual **Run workflow** button |
| C (later) | Build images in CI, push to the Codeberg registry, the server only pulls | when on-server builds become too slow |

**Codeberg-hosted runners:**

| Label | CPU | RAM | Max runtime |
|---|---|---|---|
| `codeberg-tiny` | 1 | 2 GB | 2 min |
| `codeberg-small` | 2 | 4 GB | 5 min |
| `codeberg-medium` | 4 | 8 GB | 10 min |

- Each label also has a `-lazy` variant.
- Hosted runners require a **public repository under a free licence**, and use is subject to fair use.
- Running a Docker daemon inside the job is not officially supported.

First enable Actions under **Repository settings → Units → Actions**.

### A. `.forgejo/workflows/ci.yml`

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:

jobs:
  backend:
    runs-on: codeberg-medium
    services:
      db:
        image: postgis/postgis:16-3.4
        env:
          POSTGRES_DB: geogallery_test
          POSTGRES_USER: geogallery
          POSTGRES_PASSWORD: geogallery
        options: >-
          --health-cmd "pg_isready -U geogallery" --health-interval 5s --health-retries 20
    env:
      TEST_DATABASE_URL: postgres://geogallery:geogallery@db:5432/geogallery_test?sslmode=disable
    steps:
      - uses: https://code.forgejo.org/actions/checkout@v4
      - uses: https://code.forgejo.org/actions/setup-go@v5
        with:
          go-version-file: backend/go.mod
      - run: sudo apt-get update && sudo apt-get install -y ffmpeg
      - run: cd backend && go vet ./... && go test ./...

  frontend:
    runs-on: codeberg-small
    steps:
      - uses: https://code.forgejo.org/actions/checkout@v4
      - uses: https://code.forgejo.org/actions/setup-node@v4
        with:
          node-version: 22
          cache: npm
          cache-dependency-path: frontend/package-lock.json
      - run: cd frontend && npm ci && npm run lint && npm run test -- --run && npm run build
```

The handler tests read the database from `TEST_DATABASE_URL`. If `services:` turns out to be unreliable on the hosted runners, start with the frontend job and the pure-Go tests, and move the database tests to a self-hosted runner (below).

### B. On-demand deployment over SSH

The CI key may run exactly one command on the server: a deploy script that accepts only `main` or a version tag. Nothing else is possible with that key.

**1. Create a dedicated key** on your laptop:

```bash
ssh-keygen -t ed25519 -N "" -C ci-deploy -f ci-deploy
```

**2. On the server, `/srv/geogallery/deploy.sh`** (`chmod 750`):

```bash
#!/usr/bin/env bash
set -euo pipefail
ref="${SSH_ORIGINAL_COMMAND:-main}"
[[ "$ref" =~ ^(main|v[0-9]+\.[0-9]+\.[0-9]+)$ ]] || { echo "refused ref: $ref" >&2; exit 2; }

cd /srv/geogallery
exec 9>/tmp/geogallery-deploy.lock
flock -n 9 || { echo "another deploy is running" >&2; exit 3; }

log=/srv/geogallery/backups/deploy.log
{
  echo "=== $(date -u +%FT%TZ) deploying $ref"
  ./backup.sh
  git fetch --tags --prune origin
  if [[ "$ref" == main ]]; then git checkout -q main && git reset -q --hard origin/main
  else git checkout -q --detach "refs/tags/$ref"; fi
  docker compose up -d --build --remove-orphans
  docker image prune -f
} >>"$log" 2>&1

curl -fsS --retry 20 --retry-delay 3 --retry-all-errors "https://$(grep ^SITE_DOMAIN= .env | cut -d= -f2)/api/health"
echo " deployed $ref ($(git rev-parse --short HEAD))"
```

The script writes all build output to a log file, not to the SSH session. If the CI job hits its time limit and the connection drops, the deploy therefore still finishes instead of dying halfway.

**3. Pin the key to the script** by adding one line to `/home/deploy/.ssh/authorized_keys`:

```
command="/srv/geogallery/deploy.sh",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty ssh-ed25519 AAAA... ci-deploy
```

**4. In Codeberg**, go to **Repository settings → Actions**:
- Secret `DEPLOY_SSH_KEY`: the contents of the private key file `ci-deploy`.
- Variable `DEPLOY_HOST`: `geo.example.com`.
- Variable `DEPLOY_KNOWN_HOSTS`: the output of `ssh-keyscan geo.example.com`. Compare its fingerprints with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server.

**5. Add `.forgejo/workflows/deploy.yml`:**

```yaml
name: deploy
on:
  push:
    tags: ['v*']
  workflow_dispatch:

jobs:
  deploy:
    runs-on: codeberg-small
    steps:
      - name: Deploy over SSH
        env:
          KEY: ${{ secrets.DEPLOY_SSH_KEY }}
          KNOWN_HOSTS: ${{ vars.DEPLOY_KNOWN_HOSTS }}
          HOST: ${{ vars.DEPLOY_HOST }}
          REF: ${{ github.ref_type == 'tag' && github.ref_name || 'main' }}
        run: |
          install -m 700 -d ~/.ssh
          printf '%s\n' "$KEY" > ~/.ssh/id_ed25519 && chmod 600 ~/.ssh/id_ed25519
          printf '%s\n' "$KNOWN_HOSTS" > ~/.ssh/known_hosts
          ssh -o BatchMode=yes deploy@"$HOST" "$REF"
```

**Releasing:** run `git tag v0.1.0 && git push origin v0.1.0`. Alternatively, use **Actions → deploy → Run workflow**, which deploys `main`. For fully automatic deploys later, add `branches: [main]` under `push`, and make the job wait for `ci` (`needs:` only works within one workflow, so merge the two files).

**Rollback:** run the workflow again for the previous tag, or run `ssh deploy@geo.example.com v0.0.9` from your laptop with the CI key. Migrations do not run backwards on their own. If a release with a new migration breaks, restore the dump that `deploy.sh` took just before it.

### Self-hosted runner, if the hosted limits get in the way

- Run `forgejo-runner` on a **separate** machine: a home server, or a CAX11 for about €6.
- **Do not run it on the production server.** CI jobs that can reach the Docker socket there would be root on production.
- Register it at the repository level under **Settings → Actions → Runners**.
- Use its label in `runs-on:`.

### C. Building images in CI (later)

When on-server `--build` takes too long or uses too much CPU:
1. Build `backend` and `frontend` in CI with buildah or kaniko, since the hosted runners provide no Docker daemon.
2. Push them to the Codeberg container registry, for example `codeberg.org/bvigh/geogallery-backend:<tag>`.
3. In `docker-compose.prod.yml`, give each service an `image: codeberg.org/bvigh/geogallery-<svc>:${TAG}`.
4. In `deploy.sh`, replace `up --build` with `TAG=$ref docker compose pull && docker compose up -d`.

The server then only pulls images, so deploys take seconds and rollbacks only change the tag.

---

## 14. Known gaps to fix in the repo

These are not blockers for a first deployment, but they are worth fixing:

1. **`docker-compose.yml` is development-only.** It hard-codes `S3_ENDPOINT` and depends on MinIO. `docker-compose.prod.yml` (§8) works around both. Commit it, together with `deploy/Caddyfile`.
2. **nginx timeouts for large uploads.**
   - By default nginx buffers the whole request body to disk before it proxies.
   - It waits only 60 s (`proxy_read_timeout`) for the backend's response.
   - The upload handler probes and possibly transcodes the file before it answers.

   If big video uploads end in **504** although the media appears later, add these lines to the `/api` location in `frontend/nginx.conf`: `proxy_read_timeout 900s; proxy_send_timeout 900s; proxy_request_buffering off;`.
3. **Real client IPs in logs.** nginx sends `X-Real-IP $remote_addr`, which is now Caddy's container IP. Use `proxy_set_header X-Real-IP $http_x_real_ip;` and have Caddy set the header with `header_up X-Real-IP {remote_host}`. The other option is to accept Caddy's IP.
4. **S3 compatibility switches.**
   - `UsePathStyle` is hard-coded to `true`.
   - aws-sdk-go-v2 (s3 v1.103) sends CRC32 checksums with every upload by default.
   - If Hetzner rejects uploads with checksum or `XAmzContentSHA256Mismatch` errors, set `RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired` and `ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired`.
   - If it rejects path-style URLs, make `UsePathStyle` configurable, for example through `S3_PATH_STYLE`, with a default of `true` for MinIO.
5. **No backend healthcheck in compose.** Add one for `/api/health` so that `depends_on` and `docker compose ps` reflect the real state.
