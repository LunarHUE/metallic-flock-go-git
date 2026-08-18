package object

import (
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// DefaultTopoWalkLimit is the ancestry-pass bound used by
// NewCommitIterTopoOrder. It is 0 — unbounded — because a bounded pass cannot
// guarantee git's ordering, and a log that is subtly mis-ordered is worse than a
// log that took longer to produce.
//
// Topological order cannot be decided one commit at a time: a commit may only be
// emitted once every commit listing it as a parent has been emitted, so the
// iterator must first learn the child count of everything it might return. That
// pass is O(reachable history). NewCommitIterTopoOrderWithLimit exists for
// callers who knowingly trade exactness for cost; see its doc for what a bounded
// pass gives up.
const DefaultTopoWalkLimit = 0

// commitIteratorTopoOrder walks history in git's --topo-order: no commit is
// returned before every commit listing it as a parent has been returned, and
// among the commits eligible at any moment the most recently made eligible comes
// first.
//
// That second rule is what keeps a merged branch contiguous, and it is NOT a
// date rule. git reaches for a LIFO stack here; ordering the eligible set by
// date instead is a different traversal — git spells it --date-order — and it
// interleaves concurrently-developed branches commit by commit, which is exactly
// what LogOrderCommitterTime already does. Substituting one for the other
// produces output that looks plausible and is wrong.
type commitIteratorTopoOrder struct {
	seenExternal map[plumbing.Hash]bool
	seen         map[plumbing.Hash]bool

	// indegree counts, per commit, how many of its children have not yet been
	// emitted, seeded to 1 on first sight so a commit becomes eligible exactly
	// when this falls back to 1.
	indegree map[plumbing.Hash]int

	// expanded records which commits the ancestry pass fully accounted for. When
	// the pass is unbounded this is every reachable commit. When it is bounded, a
	// commit outside this set has an incomplete child count and is never emitted
	// — see NewCommitIterTopoOrderWithLimit.
	expanded map[plumbing.Hash]bool

	// ready is the eligible set as a stack: parents are pushed in their listed
	// order and popped from the top, so a merge's later parent — the branch that
	// was merged IN — is followed to its end before the first parent resumes.
	ready []*Commit
}

// NewCommitIterTopoOrder returns a CommitIter that walks the commit history
// starting at c in topological order (git's --topo-order).
//
// Each commit is visited only once. ignore lists commits to treat as already
// seen, and seenExternal is consulted — but never written — so a caller can
// share exclusion state across iterators, matching NewCommitIterCTime.
//
// Errors from the object store surface from Next; a history that cannot be
// traversed is never silently shortened into a plausible-looking short log.
func NewCommitIterTopoOrder(
	c *Commit,
	seenExternal map[plumbing.Hash]bool,
	ignore []plumbing.Hash,
) CommitIter {
	return NewCommitIterTopoOrderWithLimit(c, seenExternal, ignore, DefaultTopoWalkLimit)
}

// NewCommitIterTopoOrderWithLimit is NewCommitIterTopoOrder with an explicit
// bound on the ancestry pass. A limit <= 0 means unbounded.
//
// A bounded pass stops measuring at the oldest frontier it reached. Beyond that
// frontier child counts are incomplete, and a commit emitted on an incomplete
// count can precede one of its own children — precisely the defect topological
// order exists to prevent. So the iterator does not emit past the boundary: it
// returns io.EOF instead, yielding a correctly-ordered PREFIX of the history
// rather than a longer list that may be wrong. Callers needing a
// guaranteed-complete history must leave the walk unbounded.
func NewCommitIterTopoOrderWithLimit(
	c *Commit,
	seenExternal map[plumbing.Hash]bool,
	ignore []plumbing.Hash,
	walkLimit int,
) CommitIter {
	seen := make(map[plumbing.Hash]bool)
	for _, h := range ignore {
		seen[h] = true
	}

	w := &commitIteratorTopoOrder{
		seenExternal: seenExternal,
		seen:         seen,
		indegree:     make(map[plumbing.Hash]int),
		expanded:     make(map[plumbing.Hash]bool),
	}

	if c == nil || seen[c.Hash] || seenExternal[c.Hash] {
		return w
	}
	if err := w.measure(c, walkLimit); err != nil {
		return &commitIterTopoError{err: err}
	}
	w.ready = append(w.ready, c)
	return w
}

// measure performs the ancestry pass: for every commit reachable from root it
// counts how many children that commit has within the walked set.
//
// Commits the pass fully accounted for are recorded in expanded; Next refuses to
// emit anything else, which is what keeps a bounded pass correct rather than
// merely short. The traversal order here is irrelevant to the result — every
// complete traversal yields the same counts — so it is a plain stack.
func (w *commitIteratorTopoOrder) measure(root *Commit, walkLimit int) error {
	w.indegree[root.Hash] = 1

	frontier := []*Commit{root}
	walked := 0

	for len(frontier) > 0 {
		c := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]

		if walkLimit > 0 && walked >= walkLimit {
			return nil
		}
		walked++
		w.expanded[c.Hash] = true

		for _, h := range c.ParentHashes {
			if w.seen[h] || w.seenExternal[h] {
				continue
			}
			// A parent's count rises by one for each child emitted before it. The
			// seed of 1 on first sight is what makes "eligible" test as == 1.
			first := w.indegree[h] == 0
			if first {
				w.indegree[h] = 1
			}
			w.indegree[h]++

			if !first {
				continue
			}
			pc, err := GetCommit(c.s, h)
			if err != nil {
				return err
			}
			frontier = append(frontier, pc)
		}
	}
	return nil
}

func (w *commitIteratorTopoOrder) Next() (*Commit, error) {
	for {
		if len(w.ready) == 0 {
			return nil, io.EOF
		}
		c := w.ready[len(w.ready)-1]
		w.ready = w.ready[:len(w.ready)-1]

		if w.seen[c.Hash] || w.seenExternal[c.Hash] {
			continue
		}
		// A commit the ancestry pass did not fully account for sits past a bounded
		// walk's frontier: its child count may be short, so emitting it could
		// place it ahead of one of its own children. Stop with the correct prefix.
		if !w.expanded[c.Hash] {
			return nil, io.EOF
		}
		w.seen[c.Hash] = true

		for _, h := range c.ParentHashes {
			if w.seen[h] || w.seenExternal[h] {
				continue
			}
			// A parent the ancestry pass never reached lies beyond a bounded
			// walk's frontier; its child count is unknown, so it is not queued.
			degree, known := w.indegree[h]
			if !known {
				continue
			}
			degree--
			w.indegree[h] = degree
			if degree != 1 {
				continue
			}
			pc, err := GetCommit(c.s, h)
			if err != nil {
				return nil, err
			}
			w.ready = append(w.ready, pc)
		}

		return c, nil
	}
}

func (w *commitIteratorTopoOrder) ForEach(cb func(*Commit) error) error {
	for {
		c, err := w.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		err = cb(c)
		if err == storer.ErrStop {
			break
		}
		if err != nil {
			return err
		}
	}

	return nil
}

func (w *commitIteratorTopoOrder) Close() {}

// commitIterTopoError reports a failure from the ancestry pass on the first
// Next. CommitIter constructors in this package have no error return, and a
// store failure must not degrade into an empty log.
type commitIterTopoError struct{ err error }

func (w *commitIterTopoError) Next() (*Commit, error)            { return nil, w.err }
func (w *commitIterTopoError) ForEach(func(*Commit) error) error { return w.err }
func (w *commitIterTopoError) Close()                            {}
