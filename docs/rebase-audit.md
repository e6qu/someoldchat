# Rebase audit

`make rebase-audit` checks that work a branch actually contained survived being
rebased. It exists because the repository gates cannot answer that question.

## The failure it catches

Rebasing a branch onto a base that has moved is a three-way merge, and the
damaging outcome is silent. When a conflict is resolved by keeping the base
side, the branch's change is discarded. The result still compiles, `go vet` is
still clean, and the test suite still passes — usually because the test that
would have failed travelled in the same discarded hunk. Reviewing the merged
diff does not help either: measured against the new base, the result looks
entirely self-consistent.

Every gate in this repository verifies that the tree is internally consistent.
None of them verifies that a branch's intent is still present. Those are
different properties, and only the second one is at risk during a rebase.

This has already happened here. Rebasing the phase6 wave stack dropped, among
others, incoming-webhook Block Kit support and three `sqlstore` durability
assertions. Both survived a full CI run on every affected pull request.

## Usage

    make rebase-audit PARENT=<ref> BRANCH=<ref> [TARGET=<ref>]

- `PARENT` — the revision the branch was written against.
- `BRANCH` — the branch tip **as authored**, before any rebase.
- `TARGET` — the revision that should now contain the work. Defaults to `HEAD`.

The command exits non-zero and lists every declaration that is `missing` (the
branch added it and the target does not have it) or `stale` (the branch changed
it and the target still holds the parent's body).

## Choosing PARENT

This is the part that decides whether the audit is meaningful.

For a branch cut from the trunk, `PARENT` is the fork point:

    make rebase-audit PARENT=$(git merge-base my-branch origin/main) BRANCH=my-branch

For a branch stacked on another branch, `PARENT` is **that branch's tip** — not
the merge base with the trunk:

    make rebase-audit PARENT=origin/feature-a BRANCH=origin/feature-b

Using a merge base for a stacked branch produces a clean report that means
nothing. The merge base predates the ancestor's own work, so every declaration
the ancestor introduced looks newly added rather than modified, and the audit
skips it. When the phase6 stack was audited this way it reported clean while a
dropped feature was live in `main`.

Because `BRANCH` must be the tip *as authored*, tag the branch tips before
starting a rebase; force-pushing destroys the only reference the audit needs.

## Scope

Go declarations only, keyed by kind and — for methods — by receiver type, so
two same-named methods on different types are never confused. Formatting is
normalised, so reindentation does not read as a change. Generated `.pb.go`
files are skipped; they are reproduced from their `.proto` source, where the
`generated-check` gate already covers drift.

A declaration that the branch changed and the target changed differently is a
genuine conflict someone resolved, so it is not reported. The audit finds
changes that vanished, not changes that were overruled.
