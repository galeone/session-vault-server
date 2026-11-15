package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// logRetention is how long daily log files are kept before deletion.
const logRetention = 21 * 24 * time.Hour

// initLogger sets up the process-wide slog logger. It writes structured text
// to stdout and to a daily log file (YYYY-MM-DD.log) in the log directory:
// $SESSION_VAULT_LOG_DIR, or $XDG_STATE_HOME/session-vault/logs, or
// ~/.local/state/session-vault/logs. Log files older than 21 days are
// deleted.
//
// The logger must never take the server down: if the log file cannot be
// opened, it falls back to stdout only.
func initLogger() {
	var w io.Writer = os.Stdout

	if f, err := openLogFile(os.Getenv("SESSION_VAULT_LOG_DIR"), os.Getenv("SESSION_VAULT_LOG_FILE")); err == nil {
		// The file stays open for the life of the process; the OS closes
		// it on exit.
		w = io.MultiWriter(os.Stdout, f)
	} else {
		slog.Error("log file unavailable, using stdout only", "error", err)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: parseLevel(os.Getenv("SESSION_VAULT_LOG_LEVEL")),
	})))
}

// parseLevel maps a level name to a slog.Level; anything unrecognized (or
// empty) falls back to info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info", "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// openLogFile returns an append-mode, 0600 log file. If logFile is non-empty
// it is used directly (parents created). Otherwise a daily file named
// YYYY-MM-DD.log is opened in dir (or the default location) and old logs are
// cleaned up first.
func openLogFile(dir, logFile string) (*os.File, error) {
	if logFile != "" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return nil, err
		}
		return os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	if dir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			base = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(base, "session-vault", "logs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cleanupOldLogs(dir)
	return os.OpenFile(filepath.Join(dir, time.Now().Format("2006-01-02")+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// cleanupOldLogs deletes *.log files whose date in the filename is older
// than the retention period. Files that don't match the YYYY-MM-DD.log
// naming are left untouched.
func cleanupOldLogs(dir string) {
	cutoff := time.Now().Add(-logRetention)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		d, err := time.Parse("2006-01-02", strings.TrimSuffix(e.Name(), ".log"))
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
