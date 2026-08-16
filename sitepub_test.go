package sitepub_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinywasm/devflow"
	"github.com/tinywasm/sitepub"
)

func setupTestBareRemote(t *testing.T) string {
	t.Helper()
	bareDir := filepath.Join(t.TempDir(), "remote.git")

	cmd := exec.Command("git", "init", "--bare", "-b", "main", bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init bare repo: %v (%s)", err, out)
	}

	seedDir := filepath.Join(t.TempDir(), "seed")
	cmd = exec.Command("git", "clone", bareDir, seedDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to clone bare repo for seeding: %v (%s)", err, out)
	}

	_ = exec.Command("git", "-C", seedDir, "config", "user.name", "Seeder").Run()
	_ = exec.Command("git", "-C", seedDir, "config", "user.email", "seed@example.com").Run()

	goMod := `module testapp

go 1.25.2

require (
	github.com/tinywasm/css v0.4.12
	github.com/tinywasm/html v0.0.17
)
`
	appGo := `package main

import (
	"github.com/tinywasm/css"
	"github.com/tinywasm/html"
)

var styles = css.New(".button { color: red; }")

func Render() html.Page {
	return html.Page{Path: "/", Body: "<h1>Initial</h1>"}
}
`
	if err := os.WriteFile(filepath.Join(seedDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "app.go"), []byte(appGo), 0644); err != nil {
		t.Fatalf("failed to write app.go: %v", err)
	}

	cmd = exec.Command("go", "mod", "tidy")
	cmd.Dir = seedDir
	_ = cmd.Run()

	cmd = exec.Command("git", "-C", seedDir, "add", ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed git add in seed repo: %v (%s)", err, out)
	}

	cmd = exec.Command("git", "-C", seedDir, "commit", "-m", "initial commit")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed git commit in seed repo: %v (%s)", err, out)
	}

	cmd = exec.Command("git", "-C", seedDir, "push", "origin", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed git push in seed repo: %v (%s)", err, out)
	}

	// Create a secondary branch "preview" in remote
	cmd = exec.Command("git", "-C", seedDir, "checkout", "-b", "preview")
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", seedDir, "push", "origin", "preview")
	_ = cmd.Run()

	return bareDir
}

func TestNewValidation(t *testing.T) {
	t.Run("empty RepoURL returns error", func(t *testing.T) {
		cfg := sitepub.Config{
			WorkDir: t.TempDir(),
		}
		_, err := sitepub.New(cfg)
		if err == nil {
			t.Fatal("expected error for empty RepoURL, got nil")
		}
	})

	t.Run("empty WorkDir returns error", func(t *testing.T) {
		cfg := sitepub.Config{
			RepoURL: "git@github.com:org/site.git",
		}
		_, err := sitepub.New(cfg)
		if err == nil {
			t.Fatal("expected error for empty WorkDir, got nil")
		}
	})
}

func TestPublisherLifecycle(t *testing.T) {
	remoteURL := setupTestBareRemote(t)
	workDir := filepath.Join(t.TempDir(), "work")

	author := sitepub.Author{
		Name:  "MJosefa CMS",
		Email: "cms@monjitaschillan.cl",
	}

	cfg := sitepub.Config{
		RepoURL: remoteURL,
		WorkDir: workDir,
		Author:  author,
	}

	pub, err := sitepub.New(cfg)
	if err != nil {
		t.Fatalf("failed to create Publisher: %v", err)
	}
	pub.SetLog(t.Log)

	updatedAppGo := `package main

import (
	"github.com/tinywasm/css"
	"github.com/tinywasm/html"
)

var styles = css.New(".button { color: blue; }")

func Render() html.Page {
	return html.Page{Path: "/", Body: "<h1>Updated Page</h1>"}
}
`

	files := []sitepub.File{
		{
			Path:    "app.go",
			Content: []byte(updatedAppGo),
		},
	}

	// 1. Full cycle clean publish
	res1, err := pub.Publish(files, "first publish")
	if err != nil {
		t.Fatalf("first publish failed: %v", err)
	}
	if !res1.Published {
		t.Errorf("expected res1.Published to be true")
	}
	if res1.Commit == "" {
		t.Errorf("expected res1.Commit to not be empty")
	}

	// Verify author config in commit
	cmd := exec.Command("git", "log", "-1", "--format=%an <%ae>")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get commit author: %v", err)
	}
	expectedAuthor := author.Name + " <" + author.Email + ">"
	if strings.TrimSpace(string(out)) != expectedAuthor {
		t.Errorf("expected commit author %q, got %q", expectedAuthor, strings.TrimSpace(string(out)))
	}

	// 2. Second publish without changes (no-op)
	res2, err := pub.Publish(files, "second publish no changes")
	if err != nil {
		t.Fatalf("second publish failed: %v", err)
	}
	if res2.Published {
		t.Errorf("expected res2.Published to be false for no changes")
	}
	if res2.Commit != "" {
		t.Errorf("expected res2.Commit to be empty when not published")
	}

	// 3. Second publish with changes
	files[0].Content = []byte(strings.Replace(updatedAppGo, "Updated Page", "Updated Page v2", 1))
	res3, err := pub.Publish(files, "third publish with changes")
	if err != nil {
		t.Fatalf("third publish failed: %v", err)
	}
	if !res3.Published {
		t.Errorf("expected res3.Published to be true when changes exist")
	}
	if res3.Commit == res1.Commit {
		t.Errorf("expected new commit SHA, got same SHA as res1")
	}

	// 4. Failed build ensures no commit is made
	cmdCountBefore := exec.Command("git", "rev-list", "--count", "HEAD")
	cmdCountBefore.Dir = workDir
	countBeforeOut, _ := cmdCountBefore.Output()
	countBefore := strings.TrimSpace(string(countBeforeOut))

	brokenFiles := []sitepub.File{
		{
			Path:    "app.go",
			Content: []byte("package main\nBROKEN GO SYNTAX"),
		},
	}
	_, err = pub.Publish(brokenFiles, "broken build publish")
	if err == nil {
		t.Fatalf("expected error on broken build, got nil")
	}

	cmdCountAfter := exec.Command("git", "rev-list", "--count", "HEAD")
	cmdCountAfter.Dir = workDir
	countAfterOut, _ := cmdCountAfter.Output()
	countAfter := strings.TrimSpace(string(countAfterOut))

	if countBefore != countAfter {
		t.Errorf("expected commit count to remain %s after failed build, got %s", countBefore, countAfter)
	}

	// Fix app.go back to valid code so working tree is clean for dirty tree test
	_, _ = pub.Publish(files, "fix after broken build attempt")

	// 5. Dirty work tree propagation
	dirtyFilePath := filepath.Join(workDir, "dirty.txt")
	if err := os.WriteFile(dirtyFilePath, []byte("uncommitted change"), 0644); err != nil {
		t.Fatalf("failed to create dirty file: %v", err)
	}
	// Stage dirty file or leave it uncommitted so git pull or devflow detects dirty tree
	cmdAdd := exec.Command("git", "add", "dirty.txt")
	cmdAdd.Dir = workDir
	_ = cmdAdd.Run()

	_, err = pub.Publish(files, "publish on dirty tree")
	if err == nil {
		t.Fatalf("expected error on dirty working tree, got nil")
	}
	if !errors.Is(err, devflow.ErrDirtyWorkTree) {
		t.Errorf("expected error to wrap devflow.ErrDirtyWorkTree, got: %v", err)
	}
}

func TestPublisherSpecificBranch(t *testing.T) {
	remoteURL := setupTestBareRemote(t)
	workDir := filepath.Join(t.TempDir(), "work-branch")

	cfg := sitepub.Config{
		RepoURL: remoteURL,
		WorkDir: workDir,
		Branch:  "preview",
		Author: sitepub.Author{
			Name:  "Test",
			Email: "test@example.com",
		},
	}

	pub, err := sitepub.New(cfg)
	if err != nil {
		t.Fatalf("New with Branch failed: %v", err)
	}

	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}
	if strings.TrimSpace(string(out)) != "preview" {
		t.Errorf("expected branch preview, got %s", strings.TrimSpace(string(out)))
	}

	_ = pub
}

func TestIdempotentClone(t *testing.T) {
	remoteURL := setupTestBareRemote(t)
	workDir := filepath.Join(t.TempDir(), "work")

	cfg := sitepub.Config{
		RepoURL: remoteURL,
		WorkDir: workDir,
		Author: sitepub.Author{
			Name:  "Test",
			Email: "test@example.com",
		},
	}

	// First initialization
	pub1, err := sitepub.New(cfg)
	if err != nil {
		t.Fatalf("first New failed: %v", err)
	}

	// Second initialization on same WorkDir
	pub2, err := sitepub.New(cfg)
	if err != nil {
		t.Fatalf("second New failed (not idempotent): %v", err)
	}

	if pub1 == nil || pub2 == nil {
		t.Fatal("expected publishers to be non-nil")
	}
}
