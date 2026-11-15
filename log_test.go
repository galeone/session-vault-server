package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"nonsense", slog.LevelInfo},
	}
	for _, c := range cases {
		if got := parseLevel(c.in); got != c.want {
			t.Errorf("parseLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCleanupOldLogs(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	recent := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	for _, f := range []string{old + ".log", recent + ".log", "notadate.log", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupOldLogs(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if names[old+".log"] {
		t.Errorf("log older than retention was not deleted: %s", old+".log")
	}
	for _, keep := range []string{recent + ".log", "notadate.log", "keep.txt"} {
		if !names[keep] {
			t.Errorf("%s should have been kept", keep)
		}
	}
}

func TestOpenLogFileDaily(t *testing.T) {
	dir := t.TempDir()
	f, err := openLogFile(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	want := filepath.Join(dir, time.Now().Format("2006-01-02")+".log")
	if f.Name() != want {
		t.Fatalf("opened %s, want %s", f.Name(), want)
	}
	fi, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("log file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestOpenLogFileExplicit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "app.log")
	f, err := openLogFile("", p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if f.Name() != p {
		t.Fatalf("opened %s, want %s", f.Name(), p)
	}
}

func TestOpenLogFileAppends(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.log")
	for i := 0; i < 2; i++ {
		f, err := openLogFile("", p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "line\nline\n" {
		t.Errorf("expected two appended lines, got %q", string(data))
	}
}
