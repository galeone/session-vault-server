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
//		id            BIGSERIAL PRIMARY KEY,
//		username      TEXT NOT NULL UNIQUE,
//		email         TEXT NOT NULL UNIQUE,
//		password_hash TEXT NOT NULL,
//		plan          TEXT NOT NULL DEFAULT 'free',
//		created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
//	);
//
//	CREATE TABLE vault_tokens (
//		id         BIGSERIAL PRIMARY KEY,
//		user_id    BIGINT NOT NULL REFERENCES vault_users(id) ON DELETE CASCADE,
//		token_hash TEXT NOT NULL UNIQUE,
//		label      TEXT NOT NULL DEFAULT '',
//		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
//	);
//
//	CREATE INDEX vault_tokens_user_idx ON vault_tokens (user_id);
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
	err := s.db.QueryRowContext(ctx,
		`SELECT u.username, u.plan
		   FROM vault_tokens t
		   JOIN vault_users u ON u.id = t.user_id
		  WHERE t.token_hash = $1`,
		tokenHash).Scan(&p.user, &p.plan)
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
	return p, true
}
