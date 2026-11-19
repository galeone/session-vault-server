package main

import "context"
import "time"

// principal identifies an authenticated caller: the storage namespace the
// token owns (user), the plan its quotas come from and, for hosted
// accounts, the scheduled account deletion (nil when none is pending).
// An empty plan means "no quota applies" — that is how static env-token
// deployments keep their current unlimited behavior.
type principal struct {
	user     string
	plan     string
	deleteAt *time.Time
}

// tokenStore resolves the sha256 hex of a bearer token to its principal.
// Two implementations exist: staticTokenStore (tokens from the
// environment, self-hosted, unlimited) and pgTokenStore (Postgres-backed,
// hosted service with per-plan session quotas).
type tokenStore interface {
	lookup(ctx context.Context, tokenHash string) (principal, bool)

	// recordSession books a stored session (vault_sessions in hosted
	// mode) so the web app can show per-account usage; forgetSession
	// removes the entry when the session is deleted. Both are no-ops
	// for the static env-token store, which has no database.
	recordSession(ctx context.Context, user, sessionID, project string)
	forgetSession(ctx context.Context, user, sessionID string)
}

// planLimits bounds how many sessions a plan may store. The check counts
// unique session ids across all of a user's projects; re-saving an id the
// user already stores is always allowed, so the cap is on *new* sessions.
// A plan missing from this map is fail-closed: uploads are refused and the
// mismatch is logged, so a typo'd plan never silently grants unlimited
// storage.
var planLimits = map[string]int{
	"free": 10,
	"plus": 100,
	"max":  500,
}

// staticTokenStore is the env-token store: the startup-built map of token
// hashes to users. Every caller is plan-less, i.e. unlimited.
type staticTokenStore struct{ tokens map[string]string }

func (s staticTokenStore) lookup(_ context.Context, tokenHash string) (principal, bool) {
	user, ok := s.tokens[tokenHash]
	return principal{user: user}, ok
}

func (s staticTokenStore) recordSession(context.Context, string, string, string) {}
func (s staticTokenStore) forgetSession(context.Context, string, string)           {}
