package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	var f io.Writer
	var err error
	if logFile := os.Getenv("SESSION_VAULT_LOG_FILE"); logFile != "" {
		// An explicit file stays open for the life of the process; the
		// OS closes it on exit.
		f, err = openLogFile("", logFile)
	} else {
		// The daily log switches files at midnight and prunes old ones,
		// for as long as the server runs.
		f, err = newDailyLog(os.Getenv("SESSION_VAULT_LOG_DIR"), time.Now)
	}
	if err == nil {
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
	dir, err := logDir(dir)
	if err != nil {
		return nil, err
	}
	return openDailyFile(dir, time.Now())
}

// logDir returns dir, or the default log directory when dir is empty.
func logDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "session-vault", "logs"), nil
}

// openDailyFile prunes old logs in dir and opens the file for day t.
func openDailyFile(dir string, t time.Time) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cleanupOldLogs(dir, t)
	return os.OpenFile(filepath.Join(dir, t.Format("2006-01-02")+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// dailyLog is an io.Writer over YYYY-MM-DD.log files: each write goes to
// the file of the current day, switching (and pruning files past the
// retention) when the date changes. Opening the file once at startup
// would put every line of a long-running server into its first day's
// file and never prune anything.
type dailyLog struct {
	dir string
	now func() time.Time

	mu  sync.Mutex
	day string
	f   *os.File
}

func newDailyLog(dir string, now func() time.Time) (*dailyLog, error) {
	dir, err := logDir(dir)
	if err != nil {
		return nil, err
	}
	d := &dailyLog{dir: dir, now: now}
	if err := d.rotate(now()); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *dailyLog) rotate(t time.Time) error {
	f, err := openDailyFile(d.dir, t)
	if err != nil {
		return err
	}
	if d.f != nil {
		_ = d.f.Close()
	}
	d.f, d.day = f, t.Format("2006-01-02")
	return nil
}

func (d *dailyLog) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := d.now(); t.Format("2006-01-02") != d.day {
		// On failure keep writing to the previous day's file rather
		// than dropping the line (logging the error here would recurse).
		_ = d.rotate(t)
	}
	return d.f.Write(p)
}

// cleanupOldLogs deletes *.log files whose date in the filename is older
// than the retention period. Files that don't match the YYYY-MM-DD.log
// naming are left untouched.
func cleanupOldLogs(dir string, now time.Time) {
	cutoff := now.Add(-logRetention)
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
