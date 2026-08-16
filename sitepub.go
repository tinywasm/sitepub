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
)

// Author representa la identidad de quien realiza los commits.
type Author struct {
	Name  string // p. ej. "MJosefa CMS"
	Email string // p. ej. "cms@monjitaschillan.cl"
}

// Config contiene la configuración requerida para publicar un sitio.
type Config struct {
	RepoURL string // git@github.com:org/site.git
	WorkDir string // copia de trabajo local
	Branch  string // vacío = la rama por defecto del remoto
	SiteDir string // subdirectorio del repo donde corre sitec; vacío = raíz
	Author  Author
}

// File es un fichero generado, con ruta relativa a la RAÍZ del repo.
type File struct {
	Path    string
	Content []byte
}

// Result representa el resultado de la operación de publicación.
type Result struct {
	Published bool   // false cuando no había nada que cambiar
	Commit    string // sha del commit hecho; vacío si Published es false
	Summary   string
}

// Publisher maneja el ciclo de publicación de un sitio estático.
type Publisher struct {
	cfg   Config
	git   *devflow.Git
	logFn func(...any)
}

// New valida la configuración y prepara la copia de trabajo local.
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

// SetLog establece la función de logging para el Publisher y sus dependencias.
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

// Publish corre el ciclo completo y es atómico respecto al remoto: o el
// sitio compila y se publica entero, o no se hace ningún commit.
func (p *Publisher) Publish(files []File, message string) (Result, error) {
	if err := p.git.Pull(); err != nil {
		return Result{}, fmt.Errorf("git pull failed: %w", err)
	}

	if err := p.writeFiles(files); err != nil {
		return Result{}, fmt.Errorf("failed to write files: %w", err)
	}

	if err := p.runSitecBuild(); err != nil {
		return Result{}, err
	}

	committed, err := p.git.CommitPaths(message, ".")
	if err != nil {
		return Result{}, fmt.Errorf("git commit failed: %w", err)
	}

	if !committed {
		return Result{
			Published: false,
			Commit:    "",
			Summary:   summaryNoChanges,
		}, nil
	}

	if _, err := p.git.PushWithoutTags(); err != nil {
		return Result{}, fmt.Errorf("git push failed: %w", err)
	}

	commitSHA, err := p.getCurrentCommitSHA()
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
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			return fmt.Errorf("failed to create parent directories for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(absPath, f.Content, 0644); err != nil {
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

	cmd := getSitecCommand(buildDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sitec build failed: %w (output: %s)", err, string(output))
	}
	p.log("sitec build succeeded:", string(output))
	return nil
}

func (p *Publisher) getCurrentCommitSHA() (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = p.cfg.WorkDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Publisher) checkoutBranch() error {
	if p.cfg.Branch == "" {
		return nil
	}
	cmd := exec.Command("git", "checkout", p.cfg.Branch)
	cmd.Dir = p.cfg.WorkDir
	if out, err := cmd.CombinedOutput(); err != nil {
		cmdTrack := exec.Command("git", "checkout", "-b", p.cfg.Branch, "origin/"+p.cfg.Branch)
		cmdTrack.Dir = p.cfg.WorkDir
		if outTrack, errTrack := cmdTrack.CombinedOutput(); errTrack != nil {
			return fmt.Errorf("failed to checkout branch %s: %v (%s / %s)", p.cfg.Branch, err, string(out), string(outTrack))
		}
	}
	return nil
}

func getSitecCommand(dir string) *exec.Cmd {
	bin := "sitec"
	if path, err := exec.LookPath("sitec"); err == nil {
		bin = path
	} else if gopath := os.Getenv("GOPATH"); gopath != "" {
		candidate := filepath.Join(gopath, "bin", "sitec")
		if _, err := os.Stat(candidate); err == nil {
			bin = candidate
		}
	} else if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "go", "bin", "sitec")
		if _, err := os.Stat(candidate); err == nil {
			bin = candidate
		}
	}
	cmd := exec.Command(bin, "build")
	cmd.Dir = dir
	return cmd
}
