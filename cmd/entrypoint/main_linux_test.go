package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestConfiguredID(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		valid bool
	}{
		{"", 1000, true}, {"99", 99, true}, {"100", 100, true},
		{"10001", 10001, true}, {"0", 0, true}, {"2147483647", 2147483647, true},
		{"-1", 0, false}, {"+99", 0, false}, {" 99", 0, false},
		{"nobody", 0, false}, {"2147483648", 0, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("PUID", tc.value)
			got, err := configuredID("PUID")
			if (err == nil) != tc.valid || err == nil && got != tc.want {
				t.Fatalf("got %d, %v; want %d, valid=%v", got, err, tc.want, tc.valid)
			}
		})
	}
	t.Setenv("PUID", "99")
	t.Setenv("PGID", "")
	if gid, err := configuredID("PGID"); err != nil || gid != 1000 {
		t.Fatalf("PGID must default independently: %d, %v", gid, err)
	}
}

func TestPrepareDataPreservesContentAndUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	data := []byte("existing database or session")
	for _, name := range []string{"control.sqlite3", "events.sqlite3", "events.sqlite3-wal", "session.key", "unrelated"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0400); err != nil {
			t.Fatal(err)
		}
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if err := prepareData(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"control.sqlite3", "events.sqlite3", "events.sqlite3-wal", "session.key", "unrelated"} {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(data) {
			t.Fatalf("%s content changed: %v", name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if name == "unrelated" {
			want = 0400
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o; want %o", name, info.Mode().Perm(), want)
		}
	}
}

func TestPrepareDataRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("untouched"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "session.key")); err != nil {
		t.Fatal(err)
	}
	if err := prepareData(dir, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("symlink accepted")
	}
	var stat unix.Stat_t
	if err := unix.Stat(outside, &stat); err != nil || stat.Mode&0777 != 0400 {
		t.Fatalf("symlink target changed: %v", err)
	}
}
