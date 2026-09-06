package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfilesReadsAllYAMLSortedByName(t *testing.T) {
	dir := t.TempDir()
	write := func(file, body string) {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("z.yaml", "name: zulu\nimage: alpine\n")
	write("a.yaml", "name: alpha\nimage: alpine\n")
	write("notes.txt", "ignored")

	got, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d profiles, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zulu" {
		t.Fatalf("not sorted by name: %v %v", got[0].Name, got[1].Name)
	}
}

func TestLoadProfilesSkipsInvalidProfileAndKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	write := func(file, body string) {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good.yaml", "name: good\nimage: alpine\n")
	write("bad.yaml", "image: alpine\n") // no name

	got, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("one bad profile must not fail the load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("got %+v, want just the good profile", got)
	}
}

func TestLoadProfilesMissingDirIsEmptyNotError(t *testing.T) {
	got, err := LoadProfiles(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d profiles, want 0", len(got))
	}
}

func TestPresentFilesListsRepoRootOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := PresentFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range got {
		if f == "go.mod" {
			found = true
		}
	}
	if !found {
		t.Fatalf("go.mod not listed: %v", got)
	}
}
