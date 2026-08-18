package git

import (
	"os"
	"path/filepath"
)

// cleanIgnored removes every file under dir that the index does not track,
// implementing the -x of `git clean -fdx`.
//
// It is a second pass rather than a flag threaded through doClean because the
// two passes answer different questions from different sources. doClean asks
// Status, which by construction cannot see ignored files — that is what being
// ignored means — so no amount of flag-passing would surface them there. This
// pass ignores Status entirely and asks the index instead: anything the index
// does not list is not tracked, and under -x everything not tracked goes.
//
// Running it after doClean is harmless: doClean has already removed the
// untracked-but-not-ignored files, and removing a file that is already gone is
// not an error here.
func (w *Worktree) cleanIgnored(dir string) error {
	idx, err := w.r.Storer.Index()
	if err != nil {
		return err
	}
	tracked := make(map[string]bool, len(idx.Entries))
	for _, e := range idx.Entries {
		tracked[filepath.ToSlash(e.Name)] = true
	}
	_, err = w.cleanIgnoredDir(dir, tracked)
	return err
}

// cleanIgnoredDir removes untracked entries under dir and reports whether dir
// still holds anything afterwards, so empty directories left behind by the sweep
// are removed on the way back up — the "d" in -fdx.
func (w *Worktree) cleanIgnoredDir(dir string, tracked map[string]bool) (kept bool, err error) {
	files, err := w.Filesystem.ReadDir(dir)
	if err != nil {
		// A directory that vanished under us (removed by the untracked pass) is
		// not a failure; there is simply nothing left to clean there.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	for _, fi := range files {
		// The repository's own metadata is never worktree content. Skipping it by
		// name matches doClean, and is what keeps a clean from destroying the repo.
		if fi.Name() == GitDirName {
			kept = true
			continue
		}
		path := filepath.Join(dir, fi.Name())

		if fi.IsDir() {
			subKept, err := w.cleanIgnoredDir(path, tracked)
			if err != nil {
				return false, err
			}
			if subKept {
				kept = true
				continue
			}
			if _, err := removeDirIfEmpty(w.Filesystem, path); err != nil {
				return false, err
			}
			continue
		}

		if tracked[filepath.ToSlash(path)] {
			kept = true
			continue
		}
		if err := w.Filesystem.Remove(path); err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	return kept, nil
}
