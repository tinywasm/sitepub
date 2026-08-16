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

// validAppGo is a site source that sitec compiles: it declares a stylesheet and
// a page. A source that compiles is the baseline every test edits from.
const validAppGo = `package main

import (
	"github.com/tinywasm/css"
	"github.com/tinywasm/html"
)

var styles = css.New(".button { color: blue; }")

func Render() html.Page {
	return html.Page{Path: "/", Body: "<h1>Valid Page</h1>"}
}
`

// gitOut runs git in dir and returns its trimmed output. The tests drive real
// repositories — a bare repo as remote, cloned over a filesystem path — because
// this library wraps the git binary and a double would prove nothing.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v (%s)", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

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

	updatedAppGo := strings.Replace(validAppGo, "Valid Page", "Updated Page", 1)

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

	// The commit must be ON THE REMOTE: publishing locally is not publishing.
	if head := gitOut(t, remoteURL, "rev-parse", "HEAD"); head != res1.Commit {
		t.Errorf("remote HEAD is %s, expected the published commit %s", head, res1.Commit)
	}

	// The compiled site travels with it, not just the sources.
	tree := gitOut(t, remoteURL, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "web/public/index.html") {
		t.Errorf("remote does not carry the compiled site:\n%s", tree)
	}

	// Verify author config in commit
	expectedAuthor := author.Name + " <" + author.Email + ">"
	if got := gitOut(t, workDir, "log", "-1", "--format=%an <%ae>"); got != expectedAuthor {
		t.Errorf("expected commit author %q, got %q", expectedAuthor, got)
	}

	// A content edit is not a release: no version tag may be created or pushed.
	if tags := gitOut(t, remoteURL, "tag", "-l"); tags != "" {
		t.Errorf("publishing must not create tags, remote has: %q", tags)
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
	if head := gitOut(t, remoteURL, "rev-parse", "HEAD"); head != res1.Commit {
		t.Errorf("a no-op publish moved the remote from %s to %s", res1.Commit, head)
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
	if head := gitOut(t, remoteURL, "rev-parse", "HEAD"); head != res3.Commit {
		t.Errorf("remote HEAD is %s, expected the published commit %s", head, res3.Commit)
	}
	if count := gitOut(t, remoteURL, "rev-list", "--count", "HEAD"); count != "3" {
		t.Errorf("expected 3 commits on the remote (seed + 2 publishes), got %s", count)
	}
}

// TestFailedBuildIsNotPublishedAndRecovers is the most important assertion of
// the design: a site that does not compile never reaches the repository, and
// the failure must not leave the publisher stuck either — the leftovers of a
// failed build would make every later Publish fail on a dirty work tree.
func TestFailedBuildIsNotPublishedAndRecovers(t *testing.T) {
	remoteURL := setupTestBareRemote(t)
	workDir := filepath.Join(t.TempDir(), "work")

	pub, err := sitepub.New(sitepub.Config{
		RepoURL: remoteURL,
		WorkDir: workDir,
		Author:  sitepub.Author{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create Publisher: %v", err)
	}
	pub.SetLog(t.Log)

	good := []sitepub.File{{Path: "app.go", Content: []byte(validAppGo)}}
	if _, err := pub.Publish(good, "first publish"); err != nil {
		t.Fatalf("first publish failed: %v", err)
	}

	localBefore := gitOut(t, workDir, "rev-parse", "HEAD")
	remoteBefore := gitOut(t, remoteURL, "rev-parse", "HEAD")

	broken := []sitepub.File{{Path: "app.go", Content: []byte("package main\nBROKEN GO SYNTAX")}}
	if _, err := pub.Publish(broken, "broken build publish"); err == nil {
		t.Fatal("expected error on broken build, got nil")
	}

	if got := gitOut(t, workDir, "rev-parse", "HEAD"); got != localBefore {
		t.Errorf("a failed build produced a local commit: %s -> %s", localBefore, got)
	}
	if got := gitOut(t, remoteURL, "rev-parse", "HEAD"); got != remoteBefore {
		t.Errorf("a failed build reached the remote: %s -> %s", remoteBefore, got)
	}

	// The next publish must work: a single bad build cannot brick the publisher.
	fixed := []sitepub.File{{Path: "app.go", Content: []byte(strings.Replace(validAppGo, "Valid Page", "Valid Page v2", 1))}}
	res, err := pub.Publish(fixed, "publish after fixing the build")
	if err != nil {
		t.Fatalf("publisher did not recover after a failed build: %v", err)
	}
	if !res.Published {
		t.Error("expected the recovery publish to reach the remote")
	}
	if head := gitOut(t, remoteURL, "rev-parse", "HEAD"); head != res.Commit {
		t.Errorf("remote HEAD is %s, expected the recovery commit %s", head, res.Commit)
	}
}

// TestDirtyWorkTreeIsNotOverwritten covers a change someone left by hand in the
// working copy: guessing (discarding it, or committing it with the content)
// would be worse than stopping.
func TestDirtyWorkTreeIsNotOverwritten(t *testing.T) {
	remoteURL := setupTestBareRemote(t)
	workDir := filepath.Join(t.TempDir(), "work")

	pub, err := sitepub.New(sitepub.Config{
		RepoURL: remoteURL,
		WorkDir: workDir,
		Author:  sitepub.Author{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create Publisher: %v", err)
	}

	files := []sitepub.File{{Path: "app.go", Content: []byte(validAppGo)}}
	if _, err := pub.Publish(files, "first publish"); err != nil {
		t.Fatalf("first publish failed: %v", err)
	}
	remoteBefore := gitOut(t, remoteURL, "rev-parse", "HEAD")

	handEdit := filepath.Join(workDir, "dirty.txt")
	if err := os.WriteFile(handEdit, []byte("uncommitted change"), 0644); err != nil {
		t.Fatalf("failed to create dirty file: %v", err)
	}

	_, err = pub.Publish(files, "publish on dirty tree")
	if err == nil {
		t.Fatal("expected error on dirty working tree, got nil")
	}
	if !errors.Is(err, devflow.ErrDirtyWorkTree) {
		t.Errorf("expected error to wrap devflow.ErrDirtyWorkTree, got: %v", err)
	}

	if _, err := os.Stat(handEdit); err != nil {
		t.Errorf("the hand-made change was destroyed: %v", err)
	}
	if got := gitOut(t, remoteURL, "rev-parse", "HEAD"); got != remoteBefore {
		t.Errorf("the remote moved while the work tree was dirty: %s -> %s", remoteBefore, got)
	}
}

// TestPendingCommitIsPushed covers a publish whose push failed earlier: the
// commit is already local, nothing new is generated, and it still must reach
// the remote instead of sitting there forever.
func TestPendingCommitIsPushed(t *testing.T) {
	remoteURL := setupTestBareRemote(t)
	workDir := filepath.Join(t.TempDir(), "work")

	pub, err := sitepub.New(sitepub.Config{
		RepoURL: remoteURL,
		WorkDir: workDir,
		Author:  sitepub.Author{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create Publisher: %v", err)
	}

	files := []sitepub.File{{Path: "app.go", Content: []byte(validAppGo)}}
	if _, err := pub.Publish(files, "first publish"); err != nil {
		t.Fatalf("first publish failed: %v", err)
	}

	// A commit that never got pushed, exactly what a failed push leaves behind.
	if err := os.WriteFile(filepath.Join(workDir, "extra.txt"), []byte("pending"), 0644); err != nil {
		t.Fatalf("failed to write extra file: %v", err)
	}
	gitOut(t, workDir, "add", "extra.txt")
	gitOut(t, workDir, "commit", "-m", "commit that never reached the remote")
	pending := gitOut(t, workDir, "rev-parse", "HEAD")

	res, err := pub.Publish(files, "publish with nothing new to generate")
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if !res.Published {
		t.Error("expected the pending commit to be reported as published")
	}
	if head := gitOut(t, remoteURL, "rev-parse", "HEAD"); head != pending {
		t.Errorf("remote HEAD is %s, expected the pending commit %s", head, pending)
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

	if got := gitOut(t, workDir, "branch", "--show-current"); got != "preview" {
		t.Errorf("expected branch preview, got %s", got)
	}

	mainBefore := gitOut(t, remoteURL, "rev-parse", "main")

	res, err := pub.Publish([]sitepub.File{{Path: "app.go", Content: []byte(validAppGo)}}, "publish to preview")
	if err != nil {
		t.Fatalf("publish on branch preview failed: %v", err)
	}

	// The configured branch is the one that moves, and only that one.
	if head := gitOut(t, remoteURL, "rev-parse", "preview"); head != res.Commit {
		t.Errorf("remote preview is %s, expected the published commit %s", head, res.Commit)
	}
	if got := gitOut(t, remoteURL, "rev-parse", "main"); got != mainBefore {
		t.Errorf("publishing to preview moved main: %s -> %s", mainBefore, got)
	}
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
