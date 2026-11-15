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

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `SESSION_VAULT_ADDR` | `:8080` | Listen address |
| `SESSION_VAULT_DATA` | `data` | Storage directory |
| `SESSION_VAULT_TOKEN_HASHES` | — | **Preferred** multi-user auth: comma-separated `sha256hex:user` pairs — no plaintext tokens on the server |
| `SESSION_VAULT_TOKENS` | — | Convenience multi-user auth: comma-separated plaintext `token:user` pairs (hashed in memory at startup) |
| `SESSION_VAULT_TOKEN` | — | Single-user shortcut: one plaintext token mapped to user `default` |

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
| `POST /api/v1/sessions` | Upload a session blob. Headers: `X-Session-Id`, `X-Project` (required, `[A-Za-z0-9_-]+`), `X-Cwd` (optional), `X-Name` (optional user-chosen label, `[A-Za-z0-9_-]{1,64}`; an unnamed re-upload keeps the existing label), `Content-Encoding: gzip` (optional transport compression, decompressed before storage), `X-Encrypted` (optional client-side encryption scheme, e.g. `age+gzip`, stored verbatim in the metadata). Body: the transcript, or its ciphertext. Returns the stored metadata. Re-uploading the same id overwrites (last write wins). |
| `GET /api/v1/sessions` | List the caller's sessions as a JSON array of metadata, newest first |
| `GET /api/v1/sessions/{id}` | Download one blob exactly as stored (`application/x-ndjson`, or `application/octet-stream` + `X-Encrypted` header when encrypted) |
| `GET /api/v1/sessions/{id}/meta` | Metadata for one session |
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
