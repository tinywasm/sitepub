# sitepub
<img src="docs/img/badges.svg">

Publishes a compiled static site to a git remote, atomically.

`sitec` compiles a site; `sitepub` publishes it. The cycle — update the working
copy, write the generated files, build, commit, push — is identical for every
application that publishes a site, so it is written once, here.

## Usage

```go
pub, err := sitepub.New(sitepub.Config{
    RepoURL: "git@github.com:org/site.git",
    WorkDir: "/var/lib/mycms/site",
    Branch:  "",         // empty = the remote's default branch
    SiteDir: "",         // repo subdirectory where sitec runs; empty = root
    Author:  sitepub.Author{Name: "MJosefa CMS", Email: "cms@monjitaschillan.cl"},
})
if err != nil {
    return err // an incomplete Config fails at start-up, not at 3 a.m.
}
pub.SetLog(log.Println)

res, err := pub.Publish([]sitepub.File{
    {Path: "content/pages.go", Content: generated},
}, "publish: home page updated")
if err != nil {
    return err
}
if !res.Published {
    return nil // nobody edited anything; not an error
}
log.Println("published", res.Commit)
```

## What it guarantees

- **The build runs before the commit.** If `sitec build` fails, `Publish`
  returns an error and no commit exists — not even locally. A broken site never
  reaches the repository, let alone the hosting.
- **A failed build leaves nothing behind.** The working copy is restored, so the
  next `Publish` runs normally instead of failing on a dirty tree.
- **"Nothing changed" is a `Result`, not an error.** `Published: false` when
  there was nothing to publish.
- **A commit that never got pushed is pushed on the next run**, so a network
  failure cannot strand the site one edit behind.
- **A hand-made change in the working copy stops the publish** (the error wraps
  `devflow.ErrDirtyWorkTree`); guessing would be worse than stopping.
- **No version tags.** A text edit is not a release.

## What it does not do

It does not decide *when* to publish (no timers, no queues — the application
owns that), does not generate the content, does not talk to any hosting
provider, and does not know about authentication: the transport comes from the
remote URL, so an SSH deploy key scoped to one repo is resolved by the machine's
git config.

## Requirements

- The `sitec` binary on `PATH` (or in `$GOPATH/bin` / `~/go/bin`): the site is
  built by running `sitec build`, so the publisher gets exactly the artifact a
  human or a CI would get, and a build failure is an exit code.
- `git` on `PATH`. Workflow-level git goes through `github.com/tinywasm/devflow`.

## Upstream gaps

Three git primitives are still run directly here because `devflow` does not
expose them: reading `HEAD`, checking out a branch, and restoring the working
tree. They belong in `devflow.Git`; when they land, `runGit` in
[sitepub.go](sitepub.go) goes away.
