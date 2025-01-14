package gitops

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/greymatter-io/operator/api/v1alpha1"
	"github.com/greymatter-io/operator/pkg/cuemodule"
)

var logger = ctrl.Log.WithName("gitops")

type Sync struct {
	GitDir        string
	SSHPrivateKey string
	SSHPassphrase string
	Remote        string
	Branch        string
	Tag           string
	Interval      int
	SyncState     *SyncState

	// Internal callback that is executed at the end
	// of every sync iteration.
	OnSyncCompleted func() error
	ctx             context.Context
	cancel          func()
}

// New will build a sync with provided constructor options.
// A remote should be specified it attempting to fetch
// config from a remote repo. If not specified, operator
// will use its default bundled config.
func New(remote string, ctx context.Context, cancel func(), options ...func(*Sync)) *Sync {
	s := &Sync{
		Remote: remote,
		ctx:    ctx,
		cancel: cancel,
	}

	// iterate through our options and do overrides.
	for _, o := range options {
		o(s)
	}

	return s
}

// WithSSHInfo will set a users ssh information on sync config.
// Passwords are not required.
func WithSSHInfo(privateKeyPath, password string) func(*Sync) {
	return func(s *Sync) {
		s.SSHPassphrase = password
		s.SSHPrivateKey = privateKeyPath
	}
}

// WithRepoInfo will set target repository information
// on a sync configuration object.
func WithRepoInfo(remote, branch string, tag string) func(*Sync) {
	// You cannot specify both a branch and a tag
	if branch != "" && tag != "" {
		log.Fatalf("You must specify a branch OR a tag for GitOps, not both. Tag: %s, Branch: %s", tag, branch)
	}

	return func(s *Sync) {
		s.Remote = remote
		s.Branch = branch
		s.Tag = tag
	}
}

// WithOnSyncCompleted will inject a callback
// function in the sync configuration.
func WithOnSyncCompleted(callback func() error) func(*Sync) {
	return func(s *Sync) {
		s.OnSyncCompleted = callback
	}
}

// Bootstrap will fetch a provided repository from the configured
// bootstrap flags. Once that repository is fetched it will write out its contents
// to disk where the operator expects its configuration to live.
// If no bootstrap flags were provided on startup, we ignore and
// use a bundled local configuration tree for defaults.
func (s *Sync) Bootstrap() error {
	if s.Remote != "" {
		err := clone(s)
		if err != nil {
			return err
		}
	}

	return nil
}

// StartStateBackup creates and maintains the SyncState object and connection to Redis, which is responsible for
// ensuring that we only apply objects that have actually *changed* during GitOps updates.
func (s *Sync) StartStateBackup(ctx context.Context, operatorCUE *cuemodule.OperatorCUE, mesh *v1alpha1.Mesh) {
	_, defaults := operatorCUE.ExtractConfig()
	ss := NewSyncState(ctx, defaults)
	s.SyncState = ss

	// cleanup routine that is executed
	// once an operator receives a done signal from the context.
	go func() {
		<-ctx.Done()
		err := s.Close()
		if err != nil {
			// If we fail to close the sync connection we need to blow up
			// so users can see why that was the case.
			panic("Failed to close internal sync connections: " + err.Error())
		}
	}()
}

// Close cleans up open sync connections when the operator dies so it
// doesn't linger and waste resources.
func (s *Sync) Close() error {
	// Close any open watches
	if s.cancel != nil {
		s.cancel()
	}

	// we return nil if we detect that SyncState is nil
	// since we can assume no redis connection has been
	// established other this would exist.
	if s.SyncState == nil {
		return nil
	}

	// Cleanup our open channels and close the go routines
	for _, ch := range s.SyncState.saveChans {
		close(ch)
	}

	return s.SyncState.redis.Close()
}

// Watch will kick off a loop that will pull a git project for changes on an interval
// provided by the users configuration. The default watch interval is 10s. A callback is exposed
// in the sync configuration object that is called on a successful completion of a pull.
// This can be used to reconcile mesh changes internally to the operator.
// Watch uses the internal sync context to handle routine cancellation. This means that
// the callback can also cancel this routine.
func (s *Sync) Watch() {
	if s.Remote == "" {
		return
	}

	lastSHA := ""
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			currentSHA, err := gitUpdate(s)
			if err != nil {
				logger.Error(err, fmt.Sprintf("failed while watching repo %s", s.Remote))
			}

			if s.OnSyncCompleted != nil && lastSHA != "" && lastSHA != currentSHA {
				err = s.OnSyncCompleted()
				if err != nil {
					logger.Error(err, "failed during callback execution OnSyncCompleted()")
				}
			}
			lastSHA = currentSHA
			time.Sleep(time.Second * time.Duration(s.Interval))
		}
	}
}

// clone will clone a repository given a singular sync config instance.
func clone(s *Sync) error {
	// if the gitdir is empty, assume cwd according to cueroot
	if s.GitDir == "" {
		s.GitDir, _ = os.Getwd()
	}

	var refName plumbing.ReferenceName
	if s.Branch != "" {
		refName = plumbing.NewBranchReferenceName(s.Branch)
	}
	if s.Tag != "" {
		refName = plumbing.NewTagReferenceName(s.Tag)
	}

	opts := &git.CloneOptions{
		URL:               s.Remote,
		ReferenceName:     refName,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth, // we need this to pull the cue config submodules
	}

	if s.SSHPrivateKey != "" {
		auth, err := ssh.NewPublicKeysFromFile("git", s.SSHPrivateKey, s.SSHPassphrase)
		if err != nil {
			return fmt.Errorf("failed to find private key from file: %w ", err)
		}
		opts.Auth = auth
		//opts.InsecureSkipTLS = true

		_, err = git.PlainClone(s.GitDir, false, opts)
		if err != nil {
			return fmt.Errorf("failed to clone with ssh: %w", err)
		}
	} else {
		if _, err := git.PlainClone(s.GitDir, false, opts); err != nil {
			return fmt.Errorf("failed to clone without auth: %w", err)
		}
	}

	return nil
}

// gitUpdate will do automatic fetching of the upstream repo
// and apply the local changes to the specified root.
func gitUpdate(sc *Sync) (string, error) {
	repo, err := git.PlainOpen(sc.GitDir)
	if err != nil {
		return "", fmt.Errorf("unable to open local repository %s: %w", sc.GitDir, err)
	}

	// FetchOptions configured with: 1) ssh private key, or 2) no auth
	opts := &git.FetchOptions{
		Auth:            nil,
		InsecureSkipTLS: true,
		Tags:            git.AllTags,
	}

	if sc.SSHPrivateKey != "" {
		opts.Auth, err = ssh.NewPublicKeysFromFile("git", sc.SSHPrivateKey, sc.SSHPassphrase)
		if err != nil {
			return "", fmt.Errorf("failed to read in ssh private key: %w", err)
		}
	}
	if err := repo.Fetch(opts); err != nil {
		if !errors.Is(git.NoErrAlreadyUpToDate, err) {
			return "", fmt.Errorf("failed to fetch remote %s: %w", sc.Remote, err)
		}
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}

	// Behavior is now different depending on whether we're pointed at a branch or a tag
	var refName plumbing.ReferenceName
	if sc.Branch != "" {
		refName = plumbing.NewBranchReferenceName(sc.Branch)

		// Attempt a checkout WITH create, but throw away the error. :(
		// NOTE(cm): we throw this error away, because we haven't figured out
		// how to reliably continue when a harmless "branch exists" error is
		// returned. I find this library difficult to use, but a pure Go git
		// implementation is worth it.
		wt.Checkout(&git.CheckoutOptions{
			Branch: refName,
			Create: true,
			Force:  true,
		})

		// Do checkout WITHOUT create. Required for a pull operation.
		if err := wt.Checkout(&git.CheckoutOptions{
			Branch: refName,
			Create: false,
			Force:  true,
		}); err != nil {
			return "", fmt.Errorf("failed to successfully checkout: %w", err)
		}

		// Do the pull
		if err := wt.Pull(&git.PullOptions{
			RemoteName:        "origin",
			ReferenceName:     refName,
			SingleBranch:      true,
			Auth:              opts.Auth,
			Force:             true,
			InsecureSkipTLS:   true,
			RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		}); err != nil {
			if !errors.Is(err, git.NoErrAlreadyUpToDate) {
				return "", fmt.Errorf("failed to pull changes from remote: %w", err)
			}
		}

	} else if sc.Tag != "" {
		refName = plumbing.NewTagReferenceName(sc.Tag)
		tagRef, err := storer.ResolveReference(repo.Storer, refName)
		if err != nil {
			return "", fmt.Errorf("unable to resolve tag '%s': %w", sc.Tag, err)
		}
		err = wt.Checkout(&git.CheckoutOptions{
			Hash:  tagRef.Hash(),
			Force: true,
		})
		if err != nil {
			return "", fmt.Errorf("unable to checkout tag '%s': %w", sc.Tag, err)
		}
	}

	// Finally, perform a clean, to remove any untracked files from the tree
	if err := wt.Clean(&git.CleanOptions{
		Dir: true,
	}); err != nil {
		return "", fmt.Errorf("failed to run git clean: %w", err)
	}

	// Extract the hash from this pull
	ref, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get repo HEAD: %w", err)
	}
	return ref.Hash().String(), nil
}
