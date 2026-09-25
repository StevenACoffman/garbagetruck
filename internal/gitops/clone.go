// Package gitops clones the GitOps Kubernetes manifest repository whose
// contents tell garbagetruck which container images are in use. It is the only
// package that knows about git.
package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	gitssh "github.com/go-git/go-git/v6/plumbing/transport/ssh"
)

const (
	// sshScheme is the only protocol garbagetruck clones over: the manifest
	// repository is private, and ssh is what its deploy keys authenticate.
	sshScheme = "ssh"
	// sshUser is the account every git forge expects over ssh, whoever owns
	// the key being offered.
	sshUser = "git"
	// gitSuffix is the repository path suffix forges accept with or without,
	// normalized on so that two spellings of one repository compare equal.
	gitSuffix = ".git"
	// tempDirPattern prefixes the clone directory, so that debris left by a
	// killed process is recognizable.
	tempDirPattern = "garbagetruck-"
)

var (
	// ErrNoRepo is returned when no repository was given at all.
	ErrNoRepo = errors.New("no manifest repository given")
	// ErrBadRepo is returned for a repository specification that names no
	// host and path.
	ErrBadRepo = errors.New("unrecognized manifest repository")
)

// Source describes the GitOps manifest repository to clone.
type Source struct {
	// Repo is the repository: "github.com/Khan/districts-k8s", or any of the
	// ssh, scp, or https spellings SSHURL accepts.
	Repo string
	// Branch is the branch to clone. Empty means the remote's default branch.
	Branch string
	// SSHKey is the path to a private key. Empty means use the ssh-agent.
	SSHKey string
}

// Worktree is a cloned repository in a temporary directory. Closing it removes
// the directory; a Worktree that is never closed leaks one.
type Worktree struct {
	dir string
}

// Clone makes a shallow, single-branch clone of src in a new temporary
// directory. The caller owns the result and must Close it.
//
// Authentication is by ssh-agent unless src.SSHKey names a private key file,
// which is what a Kubernetes deployment has: a mounted deploy key and no agent
// to ask.
func Clone(ctx context.Context, src Source) (*Worktree, error) {
	target, err := SSHURL(src.Repo)
	if err != nil {
		return nil, fmt.Errorf("clone: %w", err)
	}
	opts, err := cloneOptions(target, src)
	if err != nil {
		return nil, fmt.Errorf("clone %s: %w", target, err)
	}
	dir, err := os.MkdirTemp("", tempDirPattern)
	if err != nil {
		return nil, fmt.Errorf("clone %s: %w", target, err)
	}
	if _, err = git.PlainCloneContext(ctx, dir, opts); err != nil {
		// The directory exists only to hold this clone; a failed clone must
		// not leave it behind, because nothing else will ever remove it.
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("clone %s: %w", target, err)
	}
	return &Worktree{dir: dir}, nil
}

// SSHURL normalizes a repository specification to an ssh:// clone URL:
//
//	github.com/Khan/districts-k8s          ssh://git@github.com/Khan/districts-k8s.git
//	https://github.com/Khan/districts-k8s  ssh://git@github.com/Khan/districts-k8s.git
//	git@github.com:Khan/districts-k8s.git  ssh://git@github.com/Khan/districts-k8s.git
//
// An ssh:// URL is returned unchanged, so an explicit port or account survives.
func SSHURL(repo string) (string, error) {
	spec := strings.TrimSpace(repo)
	switch {
	case spec == "":
		return "", ErrNoRepo
	case strings.HasPrefix(spec, sshScheme+"://"):
		return spec, nil
	case strings.Contains(spec, "://"):
		return urlForm(spec)
	case strings.Contains(spec, "@"):
		return scpForm(spec)
	default:
		return hostPathForm(sshUser, spec)
	}
}

// FS reads the checked-out worktree, including its .git directory. Callers
// that walk it are expected to skip that.
func (w *Worktree) FS() fs.FS { return os.DirFS(w.dir) }

// Close removes the temporary directory and everything cloned into it.
func (w *Worktree) Close() error {
	if err := os.RemoveAll(w.dir); err != nil {
		return fmt.Errorf("remove %s: %w", w.dir, err)
	}
	return nil
}

// cloneOptions builds the clone go-git should perform: one commit, one branch,
// no tags. Nothing but the current manifests is of any use here, and the
// manifest repository's whole history is not worth fetching to read them.
func cloneOptions(target string, src Source) (*git.CloneOptions, error) {
	opts := &git.CloneOptions{
		URL:          target,
		Depth:        1,
		SingleBranch: true,
		Tags:         plumbing.NoTags,
	}
	if src.Branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(src.Branch)
	}
	if src.SSHKey != "" {
		keys, err := gitssh.NewPublicKeysFromFile(sshUser, src.SSHKey, "")
		if err != nil {
			return nil, fmt.Errorf("ssh key %s: %w", src.SSHKey, err)
		}
		opts.ClientOptions = []client.Option{client.WithSSHAuth(keys)}
	}
	return opts, nil
}

// hostPathForm builds the canonical URL from a "host/owner/repo" spelling.
func hostPathForm(user, hostPath string) (string, error) {
	host, repoPath, found := strings.Cut(hostPath, "/")
	repoPath = strings.Trim(repoPath, "/")
	if !found || host == "" || repoPath == "" {
		return "", fmt.Errorf("%q: %w", hostPath, ErrBadRepo)
	}
	repoPath = strings.TrimSuffix(repoPath, gitSuffix)
	return sshScheme + "://" + user + "@" + host + "/" + repoPath + gitSuffix, nil
}

// scpForm converts git's scp-like "git@host:owner/repo.git" spelling.
func scpForm(spec string) (string, error) {
	user, hostPath, _ := strings.Cut(spec, "@")
	if user == "" || !strings.Contains(hostPath, ":") {
		return "", fmt.Errorf("%q: %w", spec, ErrBadRepo)
	}
	return hostPathForm(user, strings.Replace(hostPath, ":", "/", 1))
}

// urlForm converts an https:// or http:// browse URL.
func urlForm(spec string) (string, error) {
	parsed, err := url.Parse(spec)
	if err != nil {
		return "", fmt.Errorf("%q: %w: %w", spec, ErrBadRepo, err)
	}
	return hostPathForm(sshUser, parsed.Host+parsed.Path)
}
