# session-vault-server

A small, self-hosted store for Claude Code session transcripts. The companion
Claude Code plugin lives in the
[`session-vault-plugin`](https://github.com/galeone/session-vault-plugin)
repository: `/session-vault:save` uploads the current session here,
`/session-vault:fetch` lists and downloads it back on any machine.

Transcripts are treated as opaque blobs — their JSONL format is internal to
Claude Code, and clients must upload them end-to-end encrypted (age;
plaintext uploads are refused), so the server never parses, inspects or
can read them.

If you'd rather not run a server, there's a hosted version at
[session-vault.dev](https://session-vault.dev).

## Quick start

1. **Get the binary.** Download the one for your platform from the
   [latest release](https://github.com/galeone/session-vault-server/releases/latest)
   (static builds for linux, darwin and windows, amd64 and arm64) and check
   it against `checksums.txt`:

   ```sh
   curl -LO https://github.com/galeone/session-vault-server/releases/latest/download/session-vault-server_linux_amd64
   curl -LO https://github.com/galeone/session-vault-server/releases/latest/download/checksums.txt
   sha256sum -c --ignore-missing checksums.txt
   chmod +x session-vault-server_linux_amd64
   ```

   Or [build it from source](#building-from-source), or install it as a
   [systemd service](#running-under-systemd--distro-packages).

2. **Create a token and start the server.** Only the token's hash goes to
   the server; the token itself goes to the plugin:

   ```sh
   TOKEN=$(openssl rand -hex 32)                    # keep this for step 4
   HASH=$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)
   SESSION_VAULT_TOKEN_HASHES=$HASH:me SESSION_VAULT_DATA=$HOME/session-vault \
     ./session-vault-server_linux_amd64           # listens on :8080
   ```

3. **Put it behind TLS.** The bearer token travels in a header, so expose
   the server only through a reverse proxy with HTTPS. See
   [Reverse proxy](#reverse-proxy) for Caddy and nginx examples (nginx
   needs a larger body limit).

4. **Point the plugin at it.** Install the plugin in Claude Code:

   ```
   /plugin marketplace add galeone/session-vault-plugin
   /plugin install session-vault@galeone-plugins
   ```

   then create `~/.config/session-vault/config` on each of your machines:

   ```sh
   SESSION_VAULT_URL=https://vault.example.com
   SESSION_VAULT_TOKEN=<the TOKEN from step 2>
   ```

   Run `/session-vault:save`. The first save creates your age key in
   `~/.config/session-vault/age.key`: copy it to every machine that will
   fetch these sessions, and keep a backup. Without it nobody can decrypt
   them, you included.

## Reverse proxy

Uploads can be large (a long transcript is several MB; the server accepts
up to 128 MiB per request), so make sure the proxy doesn't cap the body
size below that.

Caddy (automatic HTTPS, no body limit by default):

```
vault.example.com {
	reverse_proxy localhost:8080
}
```

nginx (its default `client_max_body_size` is 1 MB, which makes most
uploads fail with 413):

```nginx
server {
    listen 443 ssl;
    server_name vault.example.com;
    # ssl_certificate / ssl_certificate_key ...

    client_max_body_size 128m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## Building from source

Requires Go (the version in [go.mod](go.mod)):

```sh
go build -o session-vault-server .
```

## Running under systemd / distro packages

The easiest way to run the server as a system service is the native distro
package, built from the repository with the [Makefile](Makefile) (in the
style of [gpu-unlock](https://github.com/Zagitta/gpu-unlock)). It needs a
git clone of this repository, Go, your distro's packaging tools
(`makepkg`, `dpkg-buildpackage` or `rpmbuild`) and sudo:

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
reachable from HEAD, or `X.Y.Z.rN.gHASH` when HEAD is N commits past it
(unreleased commits, or a release whose tag is not fetched yet); it still
sorts below the next release. Override with `make VERSION=x.y.z`.

When upgrading, fetch the new tag first: a bare `git pull` does not fetch
tags that point at commits it already has, and right after a release PR is
merged the tag may not exist yet. Without it the build is labelled as the
previous version plus `.rN.gHASH`:

```sh
git fetch --tags
sudo make install
```

The package installs:

- `/usr/bin/session-vault-server`
- the hardened unit [misc/session-vault-server.service](misc/session-vault-server.service)
  — it runs the server as the dedicated ephemeral user `session-vault`
  (`DynamicUser=`), so it never runs as root or a login user
- `/etc/session-vault/` (0700) for the service environment

The service is not enabled or started at install, because it needs a
token first. Set the tokens, then enable and start it:

```sh
TOKEN=$(openssl rand -hex 32)                  # keep this: it goes to the client
HASH=$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)
sudo tee /etc/session-vault/session-vault.env >/dev/null <<EOF
SESSION_VAULT_TOKEN_HASHES=$HASH:default
EOF
sudo chmod 0600 /etc/session-vault/session-vault.env
sudo systemctl enable --now session-vault-server
```

- Data: `/var/lib/session-vault/` (owned by the dynamic user). Logs:
  `/var/log/session-vault/`, outside the data directory so no user name
  can collide with it, and the journal: `journalctl -u session-vault-server -f`.
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

## API

All endpoints (except `/healthz`) require `Authorization: Bearer <token>`.

| Method & path | Description |
|---|---|
| `POST /api/v1/sessions` | Upload a session blob. Headers: `X-Session-Id`, `X-Project` (required, `[A-Za-z0-9_-]+`), `X-Cwd` (optional), `X-Name` (optional user-chosen label, `[A-Za-z0-9_-]{1,64}`; an unnamed re-upload keeps the existing label), `X-Encrypted: age+gzip` (required: plaintext uploads are refused with 400, so the server only ever stores ciphertext). Body: the `age(gzip(transcript))` ciphertext. No `Content-Encoding` (415): the body is stored byte for byte. Returns the stored metadata. Re-uploading the same id overwrites (last write wins). |
| `GET /api/v1/sessions` | List the caller's sessions as a JSON array of metadata, newest first |
| `GET /api/v1/sessions/{id}` | Download one blob exactly as stored (`application/octet-stream`, with `X-Encrypted: age+gzip`) |
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
# upload (age-encrypted with your key's public recipient, like the client)
gzip -c session.jsonl | age -r "$(age-keygen -y ~/.config/session-vault/age.key)" \
  | curl -X POST http://localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Encrypted: age+gzip" \
  -H "X-Session-Id: 557e996b-4c2a-4214-9442-9c625d9f1791" \
  -H "X-Project: -data-pgaleone-ue-handbook" \
  --data-binary @-

# list
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/sessions

# download
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/api/v1/sessions/557e996b-4c2a-4214-9442-9c625d9f1791 \
  | age -d -i ~/.config/session-vault/age.key | gunzip > session.jsonl
```

## Storage layout

```
data/
└── <user>/
    └── <project>/
        ├── <session-id>.data         the blob, exactly as uploaded
        │                             (age ciphertext)
        └── <session-id>.meta.json    upload metadata
```

Plain files on disk — back it up like any directory.

## Logging

The server logs structured text (slog) to stdout and to a daily file named
`YYYY-MM-DD.log` in the log directory (created 0700, files 0600):
`$SESSION_VAULT_LOG_DIR`, or `$XDG_STATE_HOME/session-vault/logs`, or
`~/.local/state/session-vault/logs`. The server switches to a new file at
midnight; files older than 21 days are deleted then and on startup. If the log file cannot be opened, the server logs to
stdout only and keeps running.

## Security notes

- **Encryption is end-to-end and enforced**: the client encrypts with its
  age key and the server refuses any upload not marked `age+gzip`, so
  `.data` files are ciphertext the server cannot decrypt. Session metadata
  (ids, project slugs, original cwd, labels) is not encrypted — treat the
  data directory as sensitive.
- Run behind TLS (e.g. a reverse proxy such as Caddy or nginx): bearer tokens
  travel in a header.
- Prefer `SESSION_VAULT_TOKEN_HASHES` so plaintext tokens never appear in the
  server's environment, unit files, or `/proc/<pid>/environ`. Generate long
  random tokens (`openssl rand -hex 32`); rotate by swapping the hash and
  restarting.
- Uploads are capped at 128 MiB per request and stored byte for byte (no transport decompression, so no gzip-bomb surface).
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
