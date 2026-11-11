package groups

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreLoadMissingFile(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "allowed_groups.json"))
	if err := store.Load(); err != nil {
		t.Fatalf("Load() on missing file: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", store.Len())
	}
}

func TestStoreAddPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowed_groups.json")
	store := New(path)

	group, created, err := store.Add(-1001234567890, "f1", 5719473797)
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if !created {
		t.Fatal("first Add() must create the group")
	}
	if group.Title != "f1" {
		t.Fatalf("Title = %q, want f1", group.Title)
	}
	if !store.Contains(-1001234567890) {
		t.Fatal("Contains() must report the added group")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat group file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("group file mode = %o, want 600", info.Mode().Perm())
	}

	reloaded := New(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load() after Add(): %v", err)
	}
	if !reloaded.Contains(-1001234567890) {
		t.Fatal("reloaded store lost the group")
	}
	all := reloaded.All()
	if len(all) != 1 || all[0].Title != "f1" {
		t.Fatalf("All() = %#v, want one f1 group", all)
	}
}

func TestStoreAddIsIdempotentAndRefreshesTitle(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "allowed_groups.json"))

	if _, created, err := store.Add(-1001, "old", 1); err != nil || !created {
		t.Fatalf("first Add() created=%t err=%v", created, err)
	}
	group, created, err := store.Add(-1001, "new title", 2)
	if err != nil {
		t.Fatalf("second Add(): %v", err)
	}
	if created {
		t.Fatal("second Add() must not create a duplicate")
	}
	if group.Title != "new title" {
		t.Fatalf("Title = %q, want refreshed title", group.Title)
	}
	if store.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", store.Len())
	}
}

func TestStoreRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowed_groups.json")
	store := New(path)
	if _, _, err := store.Add(-1001, "f1", 1); err != nil {
		t.Fatalf("Add(): %v", err)
	}

	removed, err := store.Remove(-1001)
	if err != nil {
		t.Fatalf("Remove(): %v", err)
	}
	if !removed {
		t.Fatal("Remove() must report true for an existing group")
	}
	if store.Contains(-1001) {
		t.Fatal("Contains() still reports the removed group")
	}

	reloaded := New(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if reloaded.Contains(-1001) {
		t.Fatal("removed group came back after reload")
	}

	removed, err = store.Remove(-1001)
	if err != nil {
		t.Fatalf("second Remove(): %v", err)
	}
	if removed {
		t.Fatal("second Remove() must report false")
	}
}

func TestStoreRejectsEmptyID(t *testing.T) {
	store := New("")
	if _, _, err := store.Add(0, "bad", 1); err == nil {
		t.Fatal("Add(0) must fail")
	}
}
