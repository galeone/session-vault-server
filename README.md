# session-vault-server

A small, self-hosted store for Claude Code session transcripts. The companion
Claude Code plugin lives in the
[`session-vault-plugin`](../session-vault-plugin) repository: `/save` uploads
the current session here, `/fetch` lists and downloads it back on any machine.

Transcripts are treated as opaque blobs — their JSONL format is internal to
Claude Code, and clients are expected to upload them end-to-end encrypted
(age), so the server never parses or inspects them.

## Build & run

```sh
go build -o session-vault-server .

SESSION_VAULT_TOKEN=some-long-random-token ./session-vault-server
```

## Running under systemd / distro packages

The easiest way to run the server as a system service is the native distro
package, built from the repository with the [Makefile](Makefile) (in the
style of [gpu-unlock](https://github.com/Zagitta/gpu-unlock)):

```sh
make install            # detect the distro from /etc/os-release and install

# or explicitly:
make install-arch       # Arch:        makepkg + pacman -U
make install-deb        # Debian/Ubuntu: dpkg-buildpackage + dpkg -i
make install-rpm        # Fedora/RHEL:   rpmbuild + dnf install
```

`make arch` / `make deb` / `make rpm` build the package into `build/`
without installing it, and `make dist` produces the versioned source
tarball. The package version is the newest release-please tag (`vX.Y.Z`)
reachable from HEAD; override with `make VERSION=x.y.z` on an untagged
tree.

When upgrading, fetch the new tag first — a bare `git pull` does not fetch
tags that point at commits it already has, and without the tag the Makefile
silently rebuilds the previous version:

```sh
git fetch --tags
sudo make install
```

The package installs:

- `/usr/bin/session-vault-server`
- the hardened unit [misc/session-vault-server.service](misc/session-vault-server.service)
  (enabled on install) — it runs the server as the dedicated ephemeral user
  `session-vault` (`DynamicUser=`), so it never runs as root or a login user
- `/etc/session-vault/` (0700) for the service environment

Before the first start, set the tokens:

```sh
TOKEN=$(openssl rand -hex 32)                  # keep this: it goes to the client
HASH=$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)
sudo tee /etc/session-vault/session-vault.env >/dev/null <<EOF
SESSION_VAULT_TOKEN_HASHES=$HASH:default
EOF
sudo chmod 0600 /etc/session-vault/session-vault.env
sudo systemctl restart session-vault-server
```

- Data: `/var/lib/session-vault/` (owned by the dynamic user), logs there
  under `logs/` and in the journal: `journalctl -u session-vault-server -f`.
- Liveness: `curl http://localhost:8080/healthz`.
- The server is multi-user: add further tokens to
  `/etc/session-vault/session-vault.env` (`SESSION_VAULT_TOKENS` /
  `SESSION_VAULT_TOKEN_HASHES`) and restart — each user only ever sees their
  own sessions.

### Overriding the port or data directory

Both are plain env vars — the unit just ships sane defaults. The
`EnvironmentFile=` is read first, the unit's own `Environment=` lines next,
and **drop-ins last**, so an override has to live in a drop-in (or in the
unit file itself). Use `systemctl edit`, which opens a drop-in at
`/etc/systemd/system/session-vault-server.service.d/override.conf`:

Port (e.g. listen on 9090):

```sh
sudo systemctl edit session-vault-server
```

```ini
[Service]
Environment=SESSION_VAULT_ADDR=:9090
```

Data directory (e.g. `/var/lib/my-vault`):

```sh
sudo systemctl edit session-vault-server
```

```ini
[Service]
Environment=SESSION_VAULT_DATA=/var/lib/my-vault
Environment=SESSION_VAULT_LOG_DIR=/var/lib/my-vault/logs
StateDirectory=my-vault
```

The extra `StateDirectory=` line is what makes a custom path work: with
`ProtectSystem=strict` the whole filesystem is read-only for the service
except its state directories, and `StateDirectory=` is what creates the
directory and chowns it to the ephemeral `session-vault` user on every
boot. Without it the server would start and immediately fail to write.

Then `sudo systemctl restart session-vault-server` (no `daemon-reload`
needed — `systemctl edit` reloads the daemon for you). Verify with
`systemctl show session-vault-server -p Environment` and
`curl http://localhost:<port>/healthz`.

Packaging sources live in `packaging/` (`arch/PKGBUILD`, `debian/`,
`rpm/*.spec`) and are the starting point for distro repository submissions.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `SESSION_VAULT_ADDR` | `:8080` | Listen address |
| `SESSION_VAULT_DATA` | `data` | Storage directory |
| `SESSION_VAULT_TOKEN_HASHES` | — | **Preferred** multi-user auth: comma-separated `sha256hex:user` pairs — no plaintext tokens on the server |
| `SESSION_VAULT_TOKENS` | — | Convenience multi-user auth: comma-separated plaintext `token:user` pairs (hashed in memory at startup) |
| `SESSION_VAULT_TOKEN` | — | Single-user shortcut: one plaintext token mapped to user `default` |
| `SESSION_VAULT_LOG_LEVEL` | `info` | Log level: `debug` `info` `warn` `error` |
| `SESSION_VAULT_LOG_DIR` | — | Directory for the daily log files |
| `SESSION_VAULT_LOG_FILE` | — | Log to this file instead of the daily log |
| `SESSION_VAULT_DB_DSN` | — | Postgres DSN; when set, tokens and plans come from the database instead of the env vars above (hosted mode) |

At least one token must be configured. Each token maps to a user, and every
user only ever sees their own sessions. Tokens are compared by SHA-256 hash;
with `SESSION_VAULT_TOKEN_HASHES` the plaintext never touches the server:

```sh
# on your machine:
TOKEN=$(openssl rand -hex 32)
printf '%s' "$TOKEN" | sha256sum        # give the hash to the server, keep TOKEN

# on the server:
SESSION_VAULT_TOKEN_HASHES=<hash>:paolo ./session-vault-server
```

## Hosted mode: Postgres tokens and plan quotas

Setting `SESSION_VAULT_DB_DSN` switches the token source from the
environment to a Postgres database. This is how the hosted service
registers users, hands out tokens, and limits each plan: the server looks
up `sha256(token)` in `vault_tokens` and joins `vault_users` for the
namespace (`username`) and the quota (`plan`). The env token vars are
ignored in this mode, and a failed database connection is fatal at startup
— the vault would just 401 everyone.

The server assumes this schema (owned by the hosted application):

```sql
CREATE TABLE vault_users (
  id            BIGSERIAL PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  email         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  plan          TEXT NOT NULL DEFAULT 'free',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE vault_tokens (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES vault_users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  label      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX vault_tokens_user_idx ON vault_tokens (user_id);
```

`token_hash` stores the lowercase hex SHA-256 of the bearer token — the
vault never sees or stores the raw token.

### Plans and quotas

| Plan | Sessions storable |
|---|---|
| `free` | 10 |
| `plus` | 100 |
| `max` | 1000 |

A quota counts the **unique session ids** a user stores across all
projects. Re-saving an id the user already has is always allowed (updates
are free); only a *new* session is refused once the cap is hit, with HTTP
402 and a JSON body the client can relay to the user:

```json
{"error": "session limit reached", "plan": "free", "limit": 10, "used": 10}
```

Listing, downloading, and deleting are never blocked by the quota. A plan
value the server doesn't know is fail-closed (HTTP 403 + server log), so a
typo'd plan can never silently mean "unlimited". Env-token principals have
no plan and remain unlimited.

## API

All endpoints (except `/healthz`) require `Authorization: Bearer <token>`.

| Method & path | Description |
|---|---|
| `POST /api/v1/sessions` | Upload a session blob. Headers: `X-Session-Id`, `X-Project` (required, `[A-Za-z0-9_-]+`), `X-Cwd` (optional), `X-Name` (optional user-chosen label, `[A-Za-z0-9_-]{1,64}`; an unnamed re-upload keeps the existing label), `Content-Encoding: gzip` (optional transport compression, decompressed before storage), `X-Encrypted` (optional client-side encryption scheme, e.g. `age+gzip`, stored verbatim in the metadata). Body: the transcript, or its ciphertext. Returns the stored metadata. Re-uploading the same id overwrites (last write wins). |
| `GET /api/v1/sessions` | List the caller's sessions as a JSON array of metadata, newest first |
| `GET /api/v1/sessions/{id}` | Download one blob exactly as stored (`application/x-ndjson`, or `application/octet-stream` + `X-Encrypted` header when encrypted) |
| `GET /api/v1/sessions/{id}/meta` | Metadata for one session |
| `DELETE /api/v1/sessions/{id}` | Remove a session (blob and metadata). Scoped to the caller's own sessions; if the id exists under several projects, all copies are removed. Returns `{"deleted": N}` |
| `GET /healthz` | Liveness check, no auth |

Metadata shape:

```json
{
  "session_id": "557e996b-4c2a-4214-9442-9c625d9f1791",
  "name": "handbook-guidelines",
  "project": "-data-pgaleone-ue-handbook",
  "cwd": "/data/pgaleone/ue/handbook",
  "size": 123456,
  "uploaded_at": "2026-08-17T10:00:00Z",
  "encrypted": "age+gzip"
}
```

`project` is the slug Claude Code uses under `~/.claude/projects/` (the
absolute cwd with every non-alphanumeric character replaced by `-`); the
client uses it to restore downloads to the right place.

### curl examples

```sh
# upload
gzip -c session.jsonl | curl -X POST http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Encoding: gzip" \
  -H "X-Session-Id: 557e996b-4c2a-4214-9442-9c625d9f1791" \
  -H "X-Project: -data-pgaleone-ue-handbook" \
  --data-binary @-

# list
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/sessions

# download
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/api/v1/sessions/557e996b-4c2a-4214-9442-9c625d9f1791 \
  -o session.jsonl
```

## Storage layout

```
data/
└── <user>/
    └── <project>/
        ├── <session-id>.data         the blob, exactly as uploaded
        │                             (plaintext JSONL, or age ciphertext)
        └── <session-id>.meta.json    upload metadata
```

Plain files on disk — back it up like any directory.

## Logging

The server logs structured text (slog) to stdout and to a daily file named
`YYYY-MM-DD.log` in the log directory (created 0700, files 0600):
`$SESSION_VAULT_LOG_DIR`, or `$XDG_STATE_HOME/session-vault/logs`, or
`~/.local/state/session-vault/logs`. Log files older than 21 days are
deleted on startup. If the log file cannot be opened, the server logs to
stdout only and keeps running.

## Security notes

- **Encryption at rest is the client's job**: with the plugin's age key
  configured, `.data` files are ciphertext the server cannot decrypt. Without
  it, transcripts (which can contain source code, pasted secrets, and command
  output) are stored in plaintext — treat the data directory as sensitive
  either way.
- Run behind TLS (e.g. a reverse proxy such as Caddy or nginx): bearer tokens
  travel in a header.
- Prefer `SESSION_VAULT_TOKEN_HASHES` so plaintext tokens never appear in the
  server's environment, unit files, or `/proc/<pid>/environ`. Generate long
  random tokens (`openssl rand -hex 32`); rotate by swapping the hash and
  restarting.
- Uploads are capped at 256 MiB per request, both for the compressed body and for the decompressed size (defense against gzip bombs).
- Session files and their metadata are written with mode 0600 inside a 0700 directory.

## CI & releases

- **CI** (`.github/workflows/ci.yml`) runs on every push to `main` and every
  PR: `go test` and `golangci-lint` (config in `.golangci.yaml`).
- **Releases** are cut by [release-please](.github/workflows/release-please.yml)
  on push to `main`, following [Conventional Commits](https://www.conventionalcommits.org):
  - `fix: ...` → **patch** (0.1.0 → 0.1.1)
  - `feat: ...` → **minor** (0.1.0 → 0.2.0)
  - `feat!:` / `BREAKING CHANGE:` footer → **major** (0.1.0 → 1.0.0)
  - other types (`chore`, `docs`, `ci`, ...) only land in the next changelog and don't bump the version
- When a release is cut, release-please creates the `vX.Y.Z` tag, `CHANGELOG.md`
  and an empty GitHub release. That tag triggers
  [release-artifacts](.github/workflows/release-artifacts.yml), which cross-compiles
  the static binaries for linux/darwin/windows × amd64/arm64 (with the version
  injected into `--version` via `-ldflags -X main.version`) and attaches them,
  along with `checksums.txt`, to the release.
