# Staying in sync with upstream

This fork tracks [dundee/gdu](https://github.com/dundee/gdu) and re-integrates its changes on a
regular cycle. This document is the durable record of **how** that is done and **what was decided**
each round. It exists so no round starts from scratch: the process is a runbook, the decisions are
an append-only log, and each round's working notes live in a *transient* plan document that is
deleted once its round lands (its durable residue is a log entry here).

Related prose: [FORK.md](../FORK.md) is the product tour; [DESIGN.md](DESIGN.md) holds feature
design rationale; [run-books.md](run-books.md) has the release mechanics. Code never cites any of
these — comments state their reasoning in place.

## The shape of the repository

**`master` is always `upstream + a small, clean commit stack`.** The stack is the fork: every commit
in it is one coherent, buildable, tested unit. Detailed development history is preserved under
`archive/*` tags, never as long-lived branches. The delta against upstream is always readable as
`git log upstream/master..master`.

The stack is deliberately layered, bottom to top:

1. **Upstream-bound fixes** — candidates for upstream PRs. When one merges upstream, the commit
   simply drops out of the stack on the next sync.
2. **Core fork features** — in dependency order (snapshot format → archive → time travel →
   launcher).
3. **Fork identity** — docs, release pipeline. Upstream never touches these concerns, so they stay
   conflict-free on top.

## Naming

| Ref | Purpose | Lifetime |
|---|---|---|
| `master` | Public line: `upstream + stack` | Permanent; rewritten only at sync points, archive-tagged first |
| `dev/<yyyymm>-<phase>` | Messy development for one phase | Deleted after compaction (tip archive-tagged) |
| `sync/<yyyymmdd>-<upstreamsha>` | Working branch for one sync round | Deleted after landing |
| `archive/pre-rebase-<yyyymmdd>` (tag) | `master` tip before a sync rewrite | Forever |
| `archive/dev-<phase>-<yyyymmdd>` (tag) | Dev-branch tip at compaction | Forever |
| `vYYYY.M.PATCH` (tag) | Releases (CalVer — see Releases below) | Forever |

Force-pushing `master` at sync points is acceptable **while nobody downstream consumes the git
branches** (binaries are the product). If downstream branch consumers ever appear, adopt the
Git-for-Windows "merging rebase": start each rebase with a `merge -s ours` pseudo-commit tying the
previous history in, so their pulls fast-forward.

## Development between syncs: rolling compaction

The stack is kept clean *continuously*, not squashed in a crunch before each sync:

- Work happens on a `dev/` branch; fold-in candidates are committed as
  `git commit --fixup=<stack-commit>` and folded at natural checkpoints with
  `git rebase -i --autosquash` — so the branch is always "stack + short messy tail".
- A finished phase is compacted into the stack (new feature commit or folded into an existing one),
  the dev tip is archive-tagged, and the dev branch deleted.
- Every stack commit stays green: `git rebase -x 'go build ./...'` (add tests when touched areas
  warrant) enforces it mechanically.

This is what keeps sync cost low: an upstream rebase then only ever replays the small stack.

## The sync cycle (per upstream release, or ~monthly — sooner if upstream lands something in fork
territory)

1. **Fetch and pin.** `git fetch upstream master`; pin the target SHA. The whole round targets that
   SHA even if upstream moves again mid-round.
2. **Analyze before touching anything.** Classify each new upstream commit: *trivial* (deps, CI,
   docs), *take as-is*, or *integrate* (lands in fork territory or clashes with fork intent). For
   the integrate class, write the semantic reconciliation down: what upstream built, what the fork
   already does, what the merged behavior should be. Decisions of consequence get agreed with the
   maintainer *before* implementation.
3. **Write the round's transient plan doc** (`docs/upstream-rebase-plan.md` or similar): pinned
   SHAs, per-commit conflict map, integration specs, test list, verification matrix. It rides as
   the branch tip during the round and is dropped before landing.
4. **Rebase the stack** on a `sync/` branch: `git rebase --onto <pin> <old-base>`. Resolve per the
   plan; integration work is **folded into the feature commits that own it** (their messages grow a
   paragraph documenting the integration) — no "integrate upstream" commit on top. Adapt upstream's
   new tests to fork semantics where they deliberately diverge; add fork tests for every new rule.
5. **Verify** — all of: every stack commit builds; full tests + lint; `git range-diff` of the old
   vs new stack; endpoint tree-diff (`git diff <old-tip> <new-tip>` must equal upstream's delta
   plus intended glue, nothing else); pty-drive the TUI flows the round touched (see the `verify`
   skill — mocked tests cannot catch tview event-loop deadlocks); zero AI attribution trailers.
6. **Land.** Tag `archive/pre-rebase-<yyyymmdd>` on the old `master`, drop the plan-doc tip commit,
   fast-forward `master`, push (`--force-with-lease` for the rewritten refs), append the decision
   log entry below, delete the `sync/` branch.

Enable `git config rerere.enabled true` once per clone — recorded conflict resolutions replay
automatically across attempts and rounds.

### Contingency: upstream collides mid-phase

When upstream lands a structural collision while a long uncompacted `dev/` tail exists and the
phase isn't ready to compact, don't replay the messy tail onto the new base. Either:

- **Plain merge into dev** (default): `git merge upstream/master` on the dev branch, resolve once,
  keep developing. The merge never reaches `master`: at phase end, compaction is computed as the
  *endpoint diff* (`git diff <pin>...dev` carved into stack commits), not by replaying history.
- **Revert-sandwich** (when dev must stay linear): rebase dev onto `upstream + a commit reverting
  the colliding upstream change`, then finish with a commit that re-integrates it properly. The
  intermediate span misrepresents upstream behavior (fails bisect discipline), so reserve this for
  when the linearity is genuinely needed, and compact soon after.

## Releases

- **Tags are CalVer:** `vYYYY.M.PATCH` (e.g. `v2026.7.0`; bump PATCH for re-cuts within a month).
  CalVer never collides with upstream's `v5.x` (the repo carries upstream's historic tags), avoids
  SemVer prerelease-suffix pitfalls, and matches a cadence driven by syncs, not API versioning.
  Upstream lineage is stated in the release notes and in `gdu --version`, not the tag string.
- **Release notes are hand-written, never auto-generated.** After a rebase, the previous fork tag
  is not an ancestor of the new history, so any "changes since last tag" generator (GitHub's, or
  goreleaser's git changelog) sweeps in every upstream commit and describes the wrong thing. The
  workflow passes `--release-notes docs/releases/<tag>.md`; CI fails loudly if the file wasn't
  written. Template: [releases/TEMPLATE.md](releases/TEMPLATE.md) — *what's new in the fork* /
  *now based on upstream X (highlights + link)* / *compatibility notes*. Most of it falls out of
  the decision log entry for the round.
- Cut mechanics live in [run-books.md](run-books.md).

## Delegating a phase to an agent

Every phase above is documented to the point where an agent session can run it — CLAUDE.md points
here, so a plain request is enough. Canonical request shapes (adjust names, dates, tags):

- **Sync round** — *"Run an upstream sync round per docs/UPSTREAM.md: analyze what upstream added
  since our base, discuss the integration decisions with me, then plan, rebase, verify, and hand
  the branch back for review."* The decision discussion comes before any rebase; the agent should
  not resolve a fork-territory clash without agreement.
- **Cut a release** — *"Cut release vYYYY.M.P per docs/run-books.md: write docs/releases/<tag>.md
  from the template and the decision log, run lint and tests, tag, push the tag by name, and tell
  me when the draft is ready."* Publishing the draft release stays a human click.
- **Compact a phase** — *"Compact dev/<branch> per docs/UPSTREAM.md: archive-tag the dev tip, fold
  the tail into clean stack commits on master, delete the dev branch."*
- **Land a sync branch** — *"Land sync/<branch> per docs/UPSTREAM.md step 6."*

Two standing rules for any delegated phase: the agent confirms the exact SHAs/tags it derived
before anything irreversible (tags, force-pushes, releases), and it reports what it verified, not
just what it changed.

## Decision log

Append one entry per sync round (and per decision of record between rounds). Keep entries ~10
lines: range, decisions with one-line rationales, deviations, artifacts.

### 2026-07 — onto upstream #600 (`1868609`), first full sync

- Range: fork point #590 (`301ef84`) → #600; 8 upstream commits, 3 required integration
  (#593 quit-confirm, #594 Tab preview, #600 export filters).
- **Quit confirmation is snapshot-aware**: post-scan confirm only when nothing recorded
  (`unsavedScanDuration`); mid-scan when recording or ≥3s; upstream's `--no-confirm-quit`
  adopted as the master off-switch for all of it, including the fork's recording confirm.
  Upstream's "results are not saved, export first" dialog was factually wrong under
  `save-snapshots: auto`.
- **Tab preview integrated as page-level state, not a View**: preview counts as watching the live
  position; completion replaces it; `[`/`]` exit into the timeline; `q` enters the quit chain
  (upstream leaves it dead). `setCurrentDir` also wired into the stable analyzer (upstream missed
  it).
- **Parquet output rejects `--top`/`--depth`/`--summarize`** at startup: a snapshot's manifest
  claims a complete scan; `--export-threshold` stays the only sanctioned lossy knob. JSON applies
  upstream's filters first, then threshold rollup.
- Process decisions this round: archive refs are **tags** not branches; releases move to
  **CalVer** (`v5.36.1-parquet.1` remains historical); release notes hand-written per the template;
  README leads with the fork's own identity.
- Known quirks inherited, not fixed: upstream #594 publishes the scan root only after the walk in
  the sequential/stable analyzers, so mid-scan preview no-ops there (works under the default
  parallel analyzer); several upstream tests assume non-root and misbehave in root containers
  (`/xyzxyz`, permission-error fixtures).
- Artifacts: transient plan doc (dropped at landing), `archive/pre-rebase-20260720` at landing,
  release `v2026.7.x` planned.
- Post-landing appends: the mounts-parser guard and a root-proofed test suite (both
  upstream-bound — slot to layer 1 at the next sync) and this delegation section. The suite now
  passes fully in root containers.
