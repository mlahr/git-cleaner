package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIgnoreRemotes(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial")
	git("remote", "add", "ignored", filepath.Join(dir, "missing"))
	git("remote", "add", "kept", dir)
	git("update-ref", "refs/remotes/ignored/feature", "HEAD")
	git("update-ref", "refs/remotes/kept/feature", "HEAD")
	config := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(config, []byte("ignore_remotes:\n  - ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseOptions([]string{"--config", config})
	if err != nil {
		t.Fatal(err)
	}
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDir) })
	if err := fetchRemotes(context.Background(), opts.IgnoreRemotes); err != nil {
		t.Fatalf("fetch should skip inaccessible ignored remote: %v", err)
	}
	result, err := scan(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var foundLocal, foundKept bool
	for _, branch := range result.Branches {
		if branch.Remote == "ignored" {
			t.Errorf("ignored branch included: %s", branch.Name)
		}
		foundLocal = foundLocal || branch.Kind == LocalBranch
		foundKept = foundKept || branch.Remote == "kept"
	}
	if !foundLocal || !foundKept {
		t.Fatalf("expected local and kept remote branches, got %+v", result.Branches)
	}
	override, err := parseOptions([]string{"--config", config, "--ignore-remotes", "kept,kept"})
	if err != nil || len(override.IgnoreRemotes) != 1 || override.IgnoreRemotes[0] != "kept" {
		t.Fatalf("flag should replace configured list: %+v, %v", override.IgnoreRemotes, err)
	}
}
