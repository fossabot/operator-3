package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var (
	gitRemote = "git@github.com:greymatter-io/gitops-core.git"
)

func TestNewSyncOpts(t *testing.T) {
	callback := func() error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cases := map[string]struct {
		remote string
		opts   []func(*Sync)
		want   *Sync
	}{
		"defaults": {
			remote: "",
			opts:   []func(*Sync){},
			want: &Sync{
				Branch: "main",
				ctx:    ctx,
				cancel: cancel,
			},
		},
		"with-ssh-info": {
			remote: gitRemote,
			opts:   []func(*Sync){WithSSHInfo("./my/key/path.key", "my-pass")},
			want: &Sync{
				Remote:        gitRemote,
				Branch:        "main",
				ctx:           ctx,
				cancel:        cancel,
				SSHPrivateKey: "./my/key/path.key",
				SSHPassphrase: "my-pass",
			},
		},
		"with-repo-info": {
			remote: gitRemote,
			opts:   []func(*Sync){WithRepoInfo(gitRemote, "diff-branch")},
			want: &Sync{
				Remote: gitRemote,
				Branch: "diff-branch",
				ctx:    ctx,
				cancel: cancel,
			},
		},
		"with-callback": {
			remote: gitRemote,
			opts:   []func(*Sync){WithOnSyncCompleted(callback)},
			want: &Sync{
				Remote:          gitRemote,
				Branch:          "main",
				ctx:             ctx,
				cancel:          cancel,
				OnSyncCompleted: callback,
			},
		},
	}

	// Check to make sure all our test cases pass
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := New(tc.remote, ctx, cancel, tc.opts...)
			assert.Equal(t, tc.want.Branch, got.Branch)
			assert.Equal(t, tc.want.Remote, got.Remote)

			assert.Equal(t, tc.want.SSHPassphrase, got.SSHPassphrase)
			assert.Equal(t, tc.want.SSHPrivateKey, got.SSHPrivateKey)

			if name == "with-callback" {
				assert.Equal(t, true, assert.NotEmpty(t, got.OnSyncCompleted))
			}
		})
	}
}

func TestSyncLifecycle(t *testing.T) {
	// get ssh key path - right now this looks for an
	// ecdsa key due to github deprecating rsa keys support.
	path := filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519")

	// create a new sync instance
	// We want an ssh key with no password protection
	ctx, cancel := context.WithCancel(context.Background())
	s := New(gitRemote, ctx, cancel, WithSSHInfo(path, ""))
	s.GitDir = "/tmp/operator-tests/checkout"

	// Clone the initial repository to disk at the "gitdir"
	err := s.Bootstrap()
	assert.NoError(t, err)

	// Start a sync watch given the current remote we have
	go s.Watch()

	// Cleanup the watch routine
	time.Sleep(time.Second * 3)
	s.Close()

	// Cleanup the repository from the local dir
	// Using RemoveAll() function
	err = os.RemoveAll(s.GitDir)
	assert.NoError(t, err)
	assert.NoDirExists(t, s.GitDir)
}
