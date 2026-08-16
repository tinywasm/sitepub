// Package sitepub publishes a compiled static site to a git remote, atomically:
// the site is built first and only a build that succeeds is ever committed.
//
// sitec compiles a site; sitepub publishes it.
package sitepub

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tinywasm/devflow"
)

const (
	errEmptyRepoURL  = "repo URL is required"
	errEmptyWorkDir  = "work dir is required"
	summaryNoChanges = "no changes to publish"
	summaryPublished = "published successfully"

	gitBin     = "git"
	sitecBin   = "sitec"
	sitecBuild = "build"

	dirPerm  = 0o755
	filePerm = 0o644
)

// Author is the identity that signs the publishing commits.
type Author struct {
	Name  string // e.g. "MJosefa CMS"
	Email string // e.g. "cms@monjitaschillan.cl"
}

// Config is the publishing target. It is validated in New, not in Publish: an
// empty RepoURL is a programming error and must surface at start-up, not on the
// first publish attempt at 3 a.m.
type Config struct {
	RepoURL string // git@github.com:org/site.git
	WorkDir string // local working copy
	Branch  string // empty = the remote's default branch
	SiteDir string // repo subdirectory where sitec runs; empty = root
	Author  Author
}

// File is a generated file, with a path relative to the ROOT of the repo.
// Content replaces the whole file; nothing is merged.
type File struct {
	Path    string
	Content []byte
}

// Result reports what reached the remote.
type Result struct {
	Published bool   // false when there was nothing to change
	Commit    string // sha of the commit made; empty when Published is false
	Summary   string
}

// Publisher owns the working copy and runs the publish cycle.
type Publisher struct {
	cfg   Config
	git   *devflow.Git
	logFn func(...any)
}

// New validates the configuration and prepares the local working copy.
func New(cfg Config) (*Publisher, error) {
	if cfg.RepoURL == "" {
		return nil, errors.New(errEmptyRepoURL)
	}
	if cfg.WorkDir == "" {
		return nil, errors.New(errEmptyWorkDir)
	}

	g, err := devflow.NewGit()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git handler: %w", err)
	}

	g.SetRootDir(cfg.WorkDir)

	p := &Publisher{
		cfg: cfg,
		git: g,
	}

	// Clone is idempotent, so it runs unconditionally: asking first would be a
	// second answer to a question Clone already answers.
	if _, err := g.Clone(cfg.RepoURL); err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	if cfg.Branch != "" {
		if err := p.checkoutBranch(); err != nil {
			return nil, err
		}
	}

	if cfg.Author.Name != "" || cfg.Author.Email != "" {
		if err := g.SetUserConfig(cfg.Author.Name, cfg.Author.Email); err != nil {
			return nil, fmt.Errorf("failed to set git author config: %w", err)
		}
	}

	return p, nil
}

// SetLog sets the logging function for the Publisher and its dependencies.
func (p *Publisher) SetLog(fn func(...any)) {
	p.logFn = fn
	if p.git != nil {
		p.git.SetLog(fn)
	}
}

func (p *Publisher) log(args ...any) {
	if p.logFn != nil {
		p.logFn(args...)
	}
}

// Publish runs the full cycle and is atomic with respect to the remote: either
// the site compiles and is published whole, or no commit is made at all.
func (p *Publisher) Publish(files []File, message string) (Result, error) {
	if err := p.git.Pull(); err != nil {
		return Result{}, fmt.Errorf("git pull failed: %w", err)
	}

	if err := p.writeFiles(files); err != nil {
		p.restoreWorkTree()
		return Result{}, fmt.Errorf("failed to write files: %w", err)
	}

	// The build runs BEFORE the commit: a site that does not compile never
	// reaches the repository, let alone the hosting.
	if err := p.runSitecBuild(); err != nil {
		p.restoreWorkTree()
		return Result{}, err
	}

	committed, err := p.git.CommitPaths(message, ".")
	if err != nil {
		return Result{}, fmt.Errorf("git commit failed: %w", err)
	}

	if !committed {
		// Nothing new to commit, but an earlier publish may have committed and
		// failed to push. Leaving that commit behind would strand the site one
		// edit short of what was already published.
		return p.pushPending()
	}

	return p.push()
}

// pushPending publishes commits that exist locally but never reached the
// remote. "Nothing changed" is a Result, not an error: a caller that publishes
// every N minutes with nobody editing must not fill the log with failures.
func (p *Publisher) pushPending() (Result, error) {
	ahead, err := p.git.IsAheadOfRemote()
	if err != nil || !ahead {
		return Result{
			Published: false,
			Commit:    "",
			Summary:   summaryNoChanges,
		}, nil
	}
	return p.push()
}

// push sends the local branch to the remote without tags: a text edit is not a
// release, so no version tag is generated for it.
func (p *Publisher) push() (Result, error) {
	if _, err := p.git.PushWithoutTags(); err != nil {
		return Result{}, fmt.Errorf("git push failed: %w", err)
	}

	commitSHA, err := p.headSHA()
	if err != nil {
		return Result{}, fmt.Errorf("failed to get commit sha: %w", err)
	}

	return Result{
		Published: true,
		Commit:    commitSHA,
		Summary:   summaryPublished,
	}, nil
}

func (p *Publisher) writeFiles(files []File) error {
	for _, f := range files {
		absPath := filepath.Join(p.cfg.WorkDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(absPath), dirPerm); err != nil {
			return fmt.Errorf("failed to create parent directories for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(absPath, f.Content, filePerm); err != nil {
			return fmt.Errorf("failed to write file %s: %w", f.Path, err)
		}
	}
	return nil
}

func (p *Publisher) runSitecBuild() error {
	buildDir := p.cfg.WorkDir
	if p.cfg.SiteDir != "" {
		buildDir = filepath.Join(p.cfg.WorkDir, p.cfg.SiteDir)
	}

	cmd := exec.Command(sitecPath(), sitecBuild)
	cmd.Dir = buildDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sitec build failed: %w (output: %s)", err, string(output))
	}
	p.log("sitec build succeeded:", string(output))
	return nil
}

// restoreWorkTree discards everything this publish wrote. Pull guarantees the
// tree was clean when Publish started, so every change dropped here is our own
// output — and leaving it behind would make the NEXT Publish fail with
// ErrDirtyWorkTree, bricking the publisher until a human intervenes.
func (p *Publisher) restoreWorkTree() {
	if out, err := p.runGit("checkout", "--", "."); err != nil {
		p.log("failed to restore work tree:", err, out)
	}
	if out, err := p.runGit("clean", "-fd"); err != nil {
		p.log("failed to clean work tree:", err, out)
	}
}

func (p *Publisher) headSHA() (string, error) {
	return p.runGit("rev-parse", "HEAD")
}

func (p *Publisher) checkoutBranch() error {
	if p.cfg.Branch == "" {
		return nil
	}
	out, err := p.runGit("checkout", p.cfg.Branch)
	if err == nil {
		return nil
	}
	outTrack, errTrack := p.runGit("checkout", "-b", p.cfg.Branch, "origin/"+p.cfg.Branch)
	if errTrack != nil {
		return fmt.Errorf("failed to checkout branch %s: %v (%s / %s)", p.cfg.Branch, err, out, outTrack)
	}
	return nil
}

// runGit runs a plain git command in the working copy. Every workflow-level git
// operation goes through devflow; this covers only the three primitives devflow
// does not expose today — read HEAD, check out a branch, restore the work tree.
// They belong upstream in devflow, and this helper goes away when they land.
func (p *Publisher) runGit(args ...string) (string, error) {
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = p.cfg.WorkDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// sitecPath resolves the sitec binary: PATH first, then the usual GOPATH bin
// directories. Each candidate is tried in turn — a GOPATH without the binary
// must not stop the search at ~/go/bin.
func sitecPath() string {
	if path, err := exec.LookPath(sitecBin); err == nil {
		return path
	}
	var candidates []string
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		candidates = append(candidates, filepath.Join(gopath, "bin", sitecBin))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "go", "bin", sitecBin))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return sitecBin
}
