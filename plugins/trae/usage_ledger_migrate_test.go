package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUsageLedgerMigratesLegacyAuthDirFile verifies the one-time relocation:
// a pre-relocation ledger inside the auth dir moves to the auth dir's PARENT
// on first load, and its accounts survive the trip. The auth-dir copy must
// be gone afterwards — that is the whole point (the host lists auth-dir
// contents as unrecognized credentials).
func TestUsageLedgerMigratesLegacyAuthDirFile(t *testing.T) {
	resetUsageNoteTestState(t)
	defer resetUsageNoteTestState(t)

	authDir := t.TempDir()
	usageDataDirOnce.Lock()
	usageDataDirOnce.dir = authDir
	usageDataDirOnce.done = true
	usageDataDirOnce.Unlock()

	legacy := filepath.Join(authDir, "."+providerName+"-usage.json")
	doc := usageLedger{Version: usageLedgerVersion, StartedAt: "2026-09-27T00:00:00Z", Accounts: map[string]*usageAccount{}}
	doc.Accounts["idx-legacy"] = &usageAccount{AuthID: "id-legacy", Total: usageTotals{Requests: 3, Success: 2, Failed: 1, Tokens: 3000}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	usageLedgerState.Lock()
	led := usageLedgerLocked()
	a := led.Accounts["idx-legacy"]
	usageLedgerState.Unlock()
	if a == nil || a.AuthID != "id-legacy" || a.Total.Requests != 3 {
		t.Fatalf("legacy ledger not loaded after migration: %+v", a)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy file still present in auth dir (err=%v)", err)
	}
	migrated := filepath.Join(filepath.Dir(authDir), "."+providerName+"-usage.json")
	if _, err := os.Stat(migrated); err != nil {
		t.Fatalf("migrated ledger missing at %s: %v", migrated, err)
	}
	os.Remove(migrated)
}

// TestUsageLedgerMigrationKeepsNewWorldFile verifies the migration is not a
// clobber: when a new-world ledger already exists at the relocated path, the
// stale legacy copy is left untouched and the new-world content wins.
func TestUsageLedgerMigrationKeepsNewWorldFile(t *testing.T) {
	resetUsageNoteTestState(t)
	defer resetUsageNoteTestState(t)

	authDir := t.TempDir()
	usageDataDirOnce.Lock()
	usageDataDirOnce.dir = authDir
	usageDataDirOnce.done = true
	usageDataDirOnce.Unlock()

	legacy := filepath.Join(authDir, "."+providerName+"-usage.json")
	doc := usageLedger{Version: usageLedgerVersion, StartedAt: "2026-09-01T00:00:00Z", Accounts: map[string]*usageAccount{}}
	doc.Accounts["idx-stale"] = &usageAccount{AuthID: "id-stale"}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(filepath.Dir(authDir), "."+providerName+"-usage.json")
	newDoc := usageLedger{Version: usageLedgerVersion, StartedAt: "2026-09-29T00:00:00Z", Accounts: map[string]*usageAccount{}}
	newDoc.Accounts["idx-current"] = &usageAccount{AuthID: "id-current"}
	newRaw, err := json.Marshal(newDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, newRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	usageLedgerState.Lock()
	led := usageLedgerLocked()
	usageLedgerState.Unlock()
	if led.Accounts["idx-current"] == nil || led.Accounts["idx-stale"] != nil {
		t.Fatalf("new-world ledger must win: %+v", led.Accounts)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy copy must be left alone when new-world exists: %v", err)
	}
	os.Remove(newPath)
	os.Remove(legacy)
}
