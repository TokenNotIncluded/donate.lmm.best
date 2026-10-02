package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// run reads process arguments and writes to the process stdout. These tests must
// stay sequential so each invocation has the same isolation as a real CLI.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	previousArgs, previousStdout := os.Args, os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var output bytes.Buffer
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(&output, reader)
		copyDone <- err
	}()
	os.Args, os.Stdout = append([]string{"donate"}, args...), writer
	defer func() {
		os.Args, os.Stdout = previousArgs, previousStdout
		writer.Close()
	}()
	runErr := run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-copyDone; err != nil {
		t.Fatal(err)
	}
	return output.String(), runErr
}

func cliEnvironment(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv("DONATE_DATA_DIR", dir)
	t.Setenv("DONATE_PUBLIC_URL", "http://localhost:8080")
	t.Setenv("DONATE_ADDR", "127.0.0.1:0")
	return dir
}

func openCLIDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExecCLI(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func assertCLIPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %04o, want %04o", filepath.Base(path), got, want)
	}
}

func TestAdminPasswordUsesDataDirFlagAndPersistsBootstrap(t *testing.T) {
	envDir := cliEnvironment(t)
	dataDir := filepath.Join(t.TempDir(), "selected data")
	output, err := runCLI(t, "--data-dir", dataDir, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(output)
	secret, err := base64.RawURLEncoding.DecodeString(password)
	if err != nil || len(secret) < 32 || output != password+"\n" {
		t.Fatal("admin password must print only one random bootstrap secret")
	}
	if _, err := os.Stat(envDir); !os.IsNotExist(err) {
		t.Fatalf("--data-dir must take precedence over DONATE_DATA_DIR: %v", err)
	}
	passwordFile := filepath.Join(dataDir, "bootstrap-password")
	stored, err := os.ReadFile(passwordFile)
	if err != nil || string(stored) != output {
		t.Fatalf("CLI secret differs from the persisted bootstrap file: %v", err)
	}
	assertCLIPermission(t, dataDir, 0700)
	assertCLIPermission(t, filepath.Join(dataDir, "donate.sqlite"), 0600)
	assertCLIPermission(t, passwordFile, 0600)
	second, err := runCLI(t, "--data-dir", dataDir, "admin", "password")
	if err != nil || second != output {
		t.Fatalf("reopening the CLI must preserve the initial bootstrap secret: %v", err)
	}
}

func TestAdminResetRotatesPasswordAndRevokesPriorAuthentication(t *testing.T) {
	dataDir := cliEnvironment(t)
	original, err := runCLI(t, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	db := openCLIDatabase(t, filepath.Join(dataDir, "donate.sqlite"))
	var originalUserID []byte
	var originalGeneration int
	if err := db.QueryRow("SELECT user_id, generation FROM auth_state WHERE id=1").Scan(&originalUserID, &originalGeneration); err != nil {
		t.Fatal(err)
	}
	// Model an enrolled administrator with a session and an unfinished passkey
	// ceremony. The CLI recovery must invalidate all three across process opens.
	mustExecCLI(t, db, "UPDATE auth_state SET password_enabled=0 WHERE id=1")
	mustExecCLI(t, db, "INSERT INTO auth_credentials(id,data,generation) VALUES('old-passkey','{}',?)", originalGeneration)
	mustExecCLI(t, db, "INSERT INTO auth_sessions(token_hash,csrf,level,expires,generation) VALUES('old-session','csrf','admin',9999999999,?)", originalGeneration)
	mustExecCLI(t, db, "INSERT INTO auth_challenges(token_hash,kind,session_hash,data,expires,generation) VALUES('old-challenge','login','old-session','{}',9999999999,?)", originalGeneration)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, "admin", "password")
	if err == nil || !strings.Contains(err.Error(), "disabled") || output != "" {
		t.Fatalf("password command must refuse enrolled passkey authentication: output=%q error=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "bootstrap-password")); !os.IsNotExist(err) {
		t.Fatalf("disabled bootstrap password should be removed: %v", err)
	}
	resetOutput, err := runCLI(t, "admin", "reset")
	if err != nil {
		t.Fatal(err)
	}
	if resetOutput == original || len(strings.TrimSpace(resetOutput)) < 32 {
		t.Fatal("reset must issue a new random bootstrap password")
	}
	retrieved, err := runCLI(t, "admin", "password")
	if err != nil || retrieved != resetOutput {
		t.Fatalf("password command must return the reset secret: %v", err)
	}
	db = openCLIDatabase(t, filepath.Join(dataDir, "donate.sqlite"))
	var userID, passwordHash []byte
	var generation int
	var enabled bool
	if err := db.QueryRow("SELECT user_id,password_hash,password_enabled,generation FROM auth_state WHERE id=1").Scan(&userID, &passwordHash, &enabled, &generation); err != nil {
		t.Fatal(err)
	}
	if !enabled || generation <= originalGeneration || bytes.Equal(userID, originalUserID) {
		t.Fatal("reset must reopen bootstrap authentication with a new identity and generation")
	}
	if bcrypt.CompareHashAndPassword(passwordHash, []byte(strings.TrimSpace(resetOutput))) != nil {
		t.Fatal("the reset secret must match the new stored password hash")
	}
	if bcrypt.CompareHashAndPassword(passwordHash, []byte(strings.TrimSpace(original))) == nil {
		t.Fatal("the original bootstrap password still matches after reset")
	}
	for _, table := range []string{"auth_credentials", "auth_sessions", "auth_challenges"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("reset retained %d prior records in %s", count, table)
		}
	}
}

func TestBackupIncludesCommittedWALAndDoesNotOverwrite(t *testing.T) {
	dataDir := cliEnvironment(t)
	if _, err := runCLI(t, "admin", "password"); err != nil {
		t.Fatal(err)
	}
	db := openCLIDatabase(t, filepath.Join(dataDir, "donate.sqlite"))
	mustExecCLI(t, db, "PRAGMA journal_mode=WAL")
	mustExecCLI(t, db, "PRAGMA wal_autocheckpoint=0")
	mustExecCLI(t, db, "INSERT INTO settings(key,value) VALUES('backup-proof','committed before backup')")
	mustExecCLI(t, db, `INSERT INTO donations(id,status_token,amount_minor,currency,method_id,method_type,method_name,status,source,created_at,paid_at) VALUES('backup-donation','private-token',1234,'USD','manual','custom','Community','paid','manual','2026-10-03T00:00:00Z','2026-10-03T00:00:00Z')`)
	walInfo, err := os.Stat(filepath.Join(dataDir, "donate.sqlite-wal"))
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("test requires committed data in the live WAL: %v", err)
	}
	backupPath := filepath.Join(t.TempDir(), "snapshot with spaces.sqlite")
	output, err := runCLI(t, "backup", backupPath)
	if err != nil || output != "backup saved: "+backupPath+"\n" {
		t.Fatalf("backup command failed: output=%q error=%v", output, err)
	}
	assertCLIPermission(t, backupPath, 0600)
	backup := openCLIDatabase(t, backupPath)
	var integrity, proof string
	if err := backup.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("backup is not a valid SQLite snapshot: %q %v", integrity, err)
	}
	if err := backup.QueryRow("SELECT value FROM settings WHERE key='backup-proof'").Scan(&proof); err != nil || proof != "committed before backup" {
		t.Fatalf("backup lost a committed settings write: %q %v", proof, err)
	}
	var amount int
	var status string
	if err := backup.QueryRow("SELECT amount_minor,status FROM donations WHERE id='backup-donation'").Scan(&amount, &status); err != nil || amount != 1234 || status != "paid" {
		t.Fatalf("backup lost a committed donation: amount=%d status=%q error=%v", amount, status, err)
	}
	mustExecCLI(t, db, "UPDATE settings SET value='changed after backup' WHERE key='backup-proof'")
	output, err = runCLI(t, "backup", backupPath)
	if err == nil || output != "" {
		t.Fatalf("backup must refuse an existing file: output=%q error=%v", output, err)
	}
	if err := backup.QueryRow("SELECT value FROM settings WHERE key='backup-proof'").Scan(&proof); err != nil || proof != "committed before backup" {
		t.Fatalf("refused backup replaced the existing snapshot: %q %v", proof, err)
	}
}

func TestMalformedCommandsDoNotCreateDataDirectory(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"serve", "extra"}, {"admin"}, {"admin", "unknown"},
		{"admin", "password", "extra"}, {"admin", "reset", "extra"},
		{"backup"}, {"backup", "one", "two"}, {"version", "extra"},
		{"--data-dir"}, {"--unknown-flag"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dataDir := cliEnvironment(t)
			output, err := runCLI(t, args...)
			if err == nil || output != "" {
				t.Fatalf("malformed invocation must fail without printing a secret: output=%q error=%v", output, err)
			}
			if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
				t.Fatalf("malformed invocation created persistent state: %v", err)
			}
		})
	}
}

func TestVersionDoesNotCreateDataDirectory(t *testing.T) {
	dataDir := cliEnvironment(t)
	previousVersion := version
	version = "test-version"
	defer func() { version = previousVersion }()
	output, err := runCLI(t, "version")
	if err != nil || output != "test-version\n" {
		t.Fatalf("version command: output=%q error=%v", output, err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("version command must not initialize a database: %v", err)
	}
}
