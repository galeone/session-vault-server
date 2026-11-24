# Hosted mode

This is how [session-vault.dev](https://session-vault.dev) runs the server. A self-hosted
deployment doesn't need any of it: see the [README](../README.md).

## Postgres tokens and plan quotas

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
  id             BIGSERIAL PRIMARY KEY,
  username       TEXT NOT NULL UNIQUE,
  email          TEXT NOT NULL UNIQUE,
  plan           TEXT NOT NULL DEFAULT 'free',
  -- Deferred plan changes: an upgrade sets plan immediately and clears
  -- these; a downgrade sets pending_plan + plan_change_at (end of the
  -- paid period). The effective plan is computed at lookup time, so no
  -- background flip is needed.
  pending_plan   TEXT,
  plan_change_at TIMESTAMPTZ,
  -- Account deletion: the web app sets this to now() + 30 days when the
  -- user asks for a deletion (grace period, cancellable).
  delete_at      TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
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
| `max` | 500 |

A quota counts the **unique session ids** a user stores across all
projects. Re-saving an id the user already has is always allowed (updates
are free); only a *new* session is refused once the cap is hit, with HTTP
402 and a JSON body the client can relay to the user:

```json
{"error": "session limit reached", "plan": "free", "limit": 10, "used": 10, "upgrade_url": "https://your.vault.example/account"}
```

The `upgrade_url` field is only present when `SESSION_VAULT_BASE_URL` is
set (a self-hosted deployment has no billing page to point at).

Listing, downloading, and deleting are never blocked by the quota. A plan
value the server doesn't know is fail-closed (HTTP 403 + server log), so a
typo'd plan can never silently mean "unlimited". Env-token principals have
no plan and remain unlimited.

### Plan changes (upgrades and downgrades)

The vault only ever enforces `plan` as stored; the *timing* of a change is
the billing application's job, using `pending_plan` / `plan_change_at`:

- **Upgrade** — takes effect immediately:
  `UPDATE vault_users SET plan = 'max', pending_plan = NULL, plan_change_at = NULL WHERE ...;`
- **Downgrade** — takes effect at the end of the paid period, so the user
  keeps their current plan for what they already paid for:
  `UPDATE vault_users SET pending_plan = 'free', plan_change_at = <period_end> WHERE ...;`

No cron, goroutine, or "flip" job is needed anywhere: the vault computes
the *effective* plan at lookup time — `pending_plan` wins the moment
`plan_change_at` has passed, `plan` otherwise. The change takes effect
with zero drift, on every instance, without any process owning the
transition. (The billing app may optionally normalize the row later — set
`plan = pending_plan`, clear the pending columns — but that is cosmetic.)

Downgrade consequences at the vault, once the due time has passed: the
existing sessions (possibly far above the new limit) stay fully
accessible — list, download, and delete are never quota-gated and the
vault never deletes user data — re-saves of ids the user already stores
keep working, and only *new* sessions are refused with the 402 above
until the stored count drops below the new limit. The downgrade
confirmation page should warn the user to export or delete what they no
longer need.

### Account deletion (30-day grace period)

The web app sets `delete_at = now() + interval '30 days'` when a user
asks to delete the account, and `NULL` to cancel. The vault does the
purging:

- while `delete_at` is in the future the account works normally;
- once it has passed, the next authenticated request gets HTTP 410 and
  triggers the purge immediately (`data/<user>` removed from disk, then
  `DELETE FROM vault_users` — tokens and sessions cascade);
- an hourly sweep purges expired accounts of users who never come back,
  so the data is never kept past the grace period.

The purge is idempotent (disk removal and row deletion are independent
and race-safe), and a scheduled deletion never blocks anything in the
meantime — including re-saves and quota, which keep working until the
purge runs.
