package git

import (
	"os"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// inProgressState lists the repository-metadata paths that mark a half-finished
// merge, rebase, cherry-pick, revert, or bisect. Directories and files are mixed
// deliberately: rebase keeps a directory, the rest keep single files.
var inProgressState = []string{
	"MERGE_HEAD",
	"MERGE_MSG",
	"MERGE_MODE",
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"BISECT_LOG",
	"BISECT_START",
	"BISECT_TERMS",
	"BISECT_EXPECTED_REV",
	"rebase-merge",
	"rebase-apply",
	"sequencer",
}

// AbortInProgress clears any half-finished merge, rebase, cherry-pick, revert,
// or bisect recorded in the repository, the effect of `git merge --abort` and
// its siblings on the metadata side.
//
// It exists because this package never CREATES those states but can inherit
// them: a repository it manages may also be touched by a person at a shell, and
// a tree left mid-merge will fail every later operation with an error that
// describes the symptom rather than the cause. It does not restore the working
// tree — a caller wanting git's full --abort semantics pairs this with a hard
// Reset, which is the operation that actually rewinds the files.
//
// It is idempotent: a clean repository is left untouched and no error is
// returned. Errors surface only when state exists and cannot be removed, since
// silently leaving a wedge in place is the outcome this is meant to prevent.
func (w *Worktree) AbortInProgress() error {
	fs, err := w.repositoryFilesystem()
	if err != nil {
		return err
	}
	for _, name := range inProgressState {
		if err := removeAllIfExists(fs, name); err != nil {
			return err
		}
	}
	return nil
}

// ClearStash drops every stash entry, the effect of `git stash clear`.
//
// The stash is a reflog on refs/stash, so clearing it means removing the ref
// itself; the objects it referenced become unreachable and are reclaimed by the
// next prune. A repository with no stash is not an error.
func (w *Worktree) ClearStash() error {
	const stashRef = plumbing.ReferenceName("refs/stash")

	if err := w.r.Storer.RemoveReference(stashRef); err != nil && err != plumbing.ErrReferenceNotFound {
		return err
	}
	fs, err := w.repositoryFilesystem()
	if err != nil {
		return err
	}
	// The ref carries the stack in its reflog, which outlives the ref itself.
	// Leaving it behind would let a later `git stash list` resurrect entries this
	// call was asked to drop.
	return removeAllIfExists(fs, "logs/refs/stash")
}

// repositoryFilesystem returns the repository's metadata filesystem — the .git
// directory for an on-disk repository.
//
// Not every storer is backed by files (in-memory storage is a supported
// configuration), so this reports a typed error rather than assuming. Callers
// treat that as "there is no metadata to clean", which is true for a storer that
// keeps none.
func (w *Worktree) repositoryFilesystem() (billy.Filesystem, error) {
	fsStorer, ok := w.r.Storer.(interface{ Filesystem() billy.Filesystem })
	if !ok {
		return nil, ErrRepositoryNotFilesystem
	}
	return fsStorer.Filesystem(), nil
}

func removeAllIfExists(fs billy.Filesystem, path string) error {
	if _, err := fs.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return removeAllRecursive(fs, path)
}

// removeAllRecursive deletes path and anything under it. billy has no RemoveAll
// on the base Filesystem interface, so directories are emptied depth-first.
func removeAllRecursive(fs billy.Filesystem, path string) error {
	fi, err := fs.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		entries, err := fs.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := removeAllRecursive(fs, fs.Join(path, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := fs.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
