# Rebase audit

`make rebase-audit` (`cmd/rebaseaudit`) checks that the work a branch contained
survived a rebase. The repository gates cannot answer that question.

## The failure it catches

Rebasing onto a moved base is a three-way merge, and its damaging outcome is
silent. When a conflict is resolved by keeping the base side, the branch's
change is discarded. The result still compiles, `go vet` is still clean, and the
tests still pass, usually because the test that would have failed was in the
same discarded hunk. Reviewing the merged diff does not help: measured against
the new base, the result is self-consistent.

The gates verify that the tree is internally consistent. Only this audit
verifies that a branch's intent is still present.

## Usage

    make rebase-audit PARENT=<ref> BRANCH=<ref> [TARGET=<ref>]

- `PARENT`: the revision the branch was written against.
- `BRANCH`: the branch tip **as authored**, before any rebase.
- `TARGET`: the revision that should now contain the work. Defaults to `HEAD`.

The command exits non-zero and lists every Go file the branch touched that the
target lacks (`file-missing`), and every declaration that is `missing` (the
branch added it and the target does not have it) or `stale` (the branch changed
it and the target still holds the parent's body).

## Choosing PARENT

The choice of `PARENT` decides whether the audit is meaningful.

For a branch cut from the trunk, `PARENT` is the fork point:

    make rebase-audit PARENT=$(git merge-base my-branch origin/main) BRANCH=my-branch

For a branch stacked on another branch, `PARENT` is **that branch's tip**, not
the merge base with the trunk:

    make rebase-audit PARENT=origin/feature-a BRANCH=origin/feature-b

A merge base for a stacked branch produces a clean report that means nothing.
The merge base predates the ancestor's own work, so every declaration the
ancestor introduced looks newly added rather than modified, and the audit
reports it only if it is missing entirely.

Because `BRANCH` must be the tip as authored, tag the branch tips before
starting a rebase; a force-push destroys the only reference the audit needs.

## Scope

The audit compares top-level Go declarations, keyed by kind and, for methods,
by receiver type, so same-named methods on different types are never confused.
Declarations are compared after `go/printer` normalization, so reindentation
does not read as a change. Generated `.pb.go` files are skipped; they are
reproduced from their `.proto` source, and `make generated-check` covers drift.

A declaration that the branch changed and the target changed differently is a
conflict someone resolved, so it is not reported. Declarations the branch
deleted are not checked. The audit finds changes that vanished, not changes
that were overruled.
