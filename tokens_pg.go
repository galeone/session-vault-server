package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// pgTokenStore resolves tokens in a Postgres database (hosted mode). It
// assumes the following schema, owned by the hosted-service application:
//
//	CREATE TABLE vault_users (
//		id             BIGSERIAL PRIMARY KEY,
//		username       TEXT NOT NULL UNIQUE,
//		email          TEXT NOT NULL UNIQUE,
//		plan           TEXT NOT NULL DEFAULT 'free',
//		-- Deferred plan changes: an upgrade sets plan immediately and
//		-- clears these; a downgrade sets pending_plan + plan_change_at
//		-- (end of the paid period). No flip is needed anywhere: the
//		-- effective plan is computed at lookup time (pending_plan wins
//		-- once plan_change_at has passed).
//		pending_plan   TEXT,
//		plan_change_at TIMESTAMPTZ,
//		-- Account deletion: the web app sets delete_at = now() + 30 days
//		-- when the user requests a deletion; until it passes the account
//		-- works normally. Once it has passed the server purges the user's
//		-- stored sessions and deletes the row (tokens cascade).
//		delete_at      TIMESTAMPTZ,
//		created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
//	);
//
//	CREATE TABLE vault_tokens (
//		id         BIGSERIAL PRIMARY KEY,
//		user_id    BIGINT NOT NULL REFERENCES vault_users(id) ON DELETE CASCADE,
//		token_hash TEXT NOT NULL UNIQUE,
//		label      TEXT NOT NULL DEFAULT '',
//		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
//		-- bumped by lookup on every authenticated request
//		last_used_at TIMESTAMPTZ
//	);
//
//	CREATE INDEX vault_tokens_user_idx ON vault_tokens (user_id);
//
//	CREATE TABLE vault_sessions (
//		username   TEXT NOT NULL REFERENCES vault_users(username) ON DELETE CASCADE,
//		session_id TEXT NOT NULL,
//		project    TEXT NOT NULL DEFAULT '',
//		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
//		PRIMARY KEY (username, session_id)
//	);
//
// token_hash stores the lowercase hex sha256 of the bearer token — the
// vault never sees or stores the raw token.
type pgTokenStore struct{ db *sql.DB }

func newPGTokenStore(dsn string) (*pgTokenStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the token database: %w", err)
	}
	db.SetMaxOpenConns(10)
	// Fail fast at startup rather than 401-ing the first request: a bad
	// DSN or an unreachable database should not boot a host that can
	// authenticate nobody.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to the token database: %w", err)
	}
	return &pgTokenStore{db: db}, nil
}

func (s *pgTokenStore) lookup(ctx context.Context, tokenHash string) (principal, bool) {
	var p principal
	var deleteAt sql.NullTime
	// The effective plan is computed here rather than flipped by some
	// background job: a pending change (always a downgrade, scheduled at
	// the end of the paid period) takes effect the moment it is due, with
	// zero drift and no process to own the flip.
	err := s.db.QueryRowContext(ctx,
		`SELECT u.username,
			   CASE WHEN u.pending_plan IS NOT NULL AND u.plan_change_at <= now()
			        THEN u.pending_plan
			        ELSE u.plan
			   END,
			   u.delete_at
		   FROM vault_tokens t
		   JOIN vault_users u ON u.id = t.user_id
		  WHERE t.token_hash = $1`,
		tokenHash).Scan(&p.user, &p.plan, &deleteAt)
	if errors.Is(err, sql.ErrNoRows) {
		return principal{}, false
	}
	if err != nil {
		// A lookup failure must read as "unknown token" (401) rather than
		// leaking the outage; the error is logged with the hash, never
		// the raw token.
		slog.Error("token lookup", "error", err, "token_hash", tokenHash)
		return principal{}, false
	}
	if deleteAt.Valid {
		t := deleteAt.Time
		p.deleteAt = &t
	}
	// Bookkeeping for the web app's "last used" column; a failure here
	// must not fail the request.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE vault_tokens SET last_used_at = now() WHERE token_hash = $1`,
		tokenHash); err != nil {
		slog.Error("token last_used update", "error", err, "token_hash", tokenHash)
	}
	return p, true
}

// recordSession books a stored session so the web app can show
// per-account usage; re-saving the same id only bumps updated_at.
func (s *pgTokenStore) recordSession(ctx context.Context, user, sessionID, project string) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vault_sessions (username, session_id, project, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (username, session_id)
			DO UPDATE SET project = EXCLUDED.project, updated_at = now()`,
		user, sessionID, project)
	if err != nil {
		slog.Error("recording the stored session", "user", user, "session", sessionID, "error", err)
	}
}

// forgetSession drops the usage entry for a deleted session.
func (s *pgTokenStore) forgetSession(ctx context.Context, user, sessionID string) {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM vault_sessions WHERE username = $1 AND session_id = $2`,
		user, sessionID); err != nil {
		slog.Error("forgetting the stored session", "user", user, "session", sessionID, "error", err)
	}
}

// purgeUser deletes the account row; vault_tokens and vault_sessions
// cascade via their foreign keys. It does not touch the on-disk data:
// the server owns the data directory and removes it separately.
func (s *pgTokenStore) purgeUser(ctx context.Context, user string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM vault_users WHERE username = $1`, user)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errAlreadyPurged
	}
	return nil
}

// expiredUsers lists the accounts whose deletion date has passed.
func (s *pgTokenStore) expiredUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT username FROM vault_users WHERE delete_at IS NOT NULL AND delete_at <= now()`)
	if err != nil {
		return nil, err
	}
	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			_ = rows.Close()
			return nil, err
		}
		users = append(users, u)
	}
	err = rows.Close()
	return users, errors.Join(err, rows.Err())
}
