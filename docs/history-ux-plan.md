# Unified history UX — design plan

**Status:** agreed plan, 2026-07-21. This is a *transient planning document* (the repo's
convention): implementation inherits it, its rationale gets folded into
[DESIGN.md](DESIGN.md) as decisions land, and the file is then deleted — it survives in git
history. Code comments must never cite this document or its section numbers; state reasoning
in place (see the comment policy in [../CLAUDE.md](../CLAUDE.md)).

**Scope:** the TUI's snapshot/history experience — the View/Baseline model, the diff
("compare") view, the snapshot pickers, time-travel stepping, and their keyboard grammar.
The launcher, CLI flags, Parquet layer, and non-interactive modes are untouched except where
explicitly noted.

---

## 1. Why — findings against the current implementation

The fork's history features are individually strong but grew as separate organs. A
consistency review found:

- **F1 — Two pickers, one component in disguise.** `S` (baseline) and `O` (open) launch the
  same `buildPicker` with different titles, columns, and select actions. They *look* like
  different windows, teach different columns, and neither shows the live scan — the most
  important tree in the app is invisible in every list.
- **F2 — The header collapses asymmetrically.** In the most common compare state (live view +
  baseline set) only the Baseline line renders; the screen never states "X compared with Y."
  The two-slot header machinery exists but the primary slot stays empty when the view is live.
- **F3 — The diff view is a replacement layout, not the normal view plus columns.** It drops
  the percentage, item-count, mtime, and mark columns, recolors the size column, and rescales
  the bar to |Δ|. Marking (`space`) still *works* in diff mode but renders invisibly — so the
  natural workflow "sort by growth → mark → delete" is half-broken.
- **F4 — Sorting is modal and inconsistent.** Diff mode forces a growth sort with private
  direction keys (`>`/`<`), while `s`/`n`/`C`/`M` silently mutate a hidden sort that only
  takes effect after leaving diff mode.
- **F5 — Vocabulary drift.** The same concept is "compare" in the header hint, "Baseline" in
  the picker title, and both at once in the help screen.
- **F6 — Latent bug: partial-tree diffs.** With a baseline set, `r` (rescan) then `Tab` into
  the mid-scan preview renders a diff of the *partial* tree: everything not yet scanned shows
  as removed, half-scanned directories as massive shrink. Misleading output with authority.
- **F7 — Key meanings shift across screens.** `s`/`S` mean sort/baseline in the tree view but
  open-latest/open-picker in the launcher.

## 2. The model

### Roles

Every screen shows a **Viewing** tree (the primary — live disk or one snapshot), optionally
against a **Baseline** (the comparison reference). The roles are *asymmetric*: the Viewing
tree is the room you stand in (browsable; mutable when live), the Baseline is a reference
overlay. Controls for the two are *symmetric* (mirrored key pairs); visuals must never let
them blur.

| Role | Glyph | Fallback (`--no-unicode`) | Semantics |
|---|---|---|---|
| Viewing (primary) | `●` | `*` | solid — the tree you inhabit |
| Baseline | `◇` | `o` | hollow — the reference you compare against |

### Three independent state axes

| Axis | Values | Nature |
|---|---|---|
| **A. Baseline set?** | none / one snapshot | data — does a comparison exist |
| **B. Δ rendering** | plain rows / compare rows | view — how the tree is drawn (only meaningful when A is set) |
| **C. Browser focus** | moving ● / moving ◇ | picker-local — which cursor the arrows drive |

Axis A drives everything: two-line header iff a baseline is set; Esc clears it. Axis B is a
peek toggle (Tab) whose state is always announced in the header. Axis C exists only inside
the browser. The axes are never conflated: turning Δ rendering off does not clear the
baseline; moving a browser cursor does not change what's applied until Enter.

### Principles

1. **State is visible or it doesn't exist** — every mode readable from the header alone.
2. **Mode = data, plus at most one loud view-toggle** — diff exists because a baseline
   exists; the one toggle (Δ shown/hidden) is always announced.
3. **Cheap gestures for common moves, the browser for deliberate ones** — brackets/braces
   for adjacent hops, the browser for long jumps; identical ordering and folding rules.
4. **Tab flips to the counterpart** — every screen has exactly one meaningful pair:
   progress ↔ preview (scan), plain ↔ Δ (tree), ● ↔ ◇ cursor (browser).
5. **Asymmetric roles, symmetric controls** — `[` `]` move ●, `{` `}` move ◇.

### Jobs served, with keypress budgets

- **J1** "What's eating my disk now?" — untouched upstream flow.
- **J2** "What grew since last time?" — **one keypress** (`{`).
- **J3** "What did this look like in June?" — `[` stepping / browser.
- **J4** "Find what grew and delete it" — compare view keeps marks + delete.
- **J5** "Compare two arbitrary points" — the browser's two cursors.

## 3. Visual grammar

### Glyphs and colors

- `●` / `◇` as above. Near-neighbor check: `✦` (the "new item" delta marker) is solid,
  warm-colored, and appears only in the Δ column; `◇` appears only in header/picker margins.
  Different columns, different fill — acceptable, and now a checked decision.
- Colors must not collide with the diff triad (warm orange = grew, teal = shrank, violet =
  removed, amber = approx). Working choice: `●` default-bold, `◇` the device-table blue.
  Final call at implementation with a pty-harness screenshot.
- `▲` was rejected for the baseline role: it already means "grew" in the Δ column.
- The current picker uses `●` to mean *active baseline* — the opposite of its new role. The
  implementation sweep must leave no residual old-meaning `●`.

### The header

- **One line** when no baseline is set (today's hint line, live) or a snapshot view's
  Viewing line.
- **Two lines** iff a baseline is set — line 1 always `●` Viewing (even when live; fixes F2),
  line 2 `◇` Baseline with the Δ-state tail.
- The same two lines, same glyphs, render at the top of the **browser** — the unity device.
- Header-hidden configs: `dirLabelPrefix` carries compact `[● …] [◇ …]` prefixes instead.

### Vocabulary

One noun set everywhere: **Viewing**, **Baseline**, and **compare** as the verb. The word
"anchor" may appear in help copy as a teaching metaphor for the baseline. No more
"diff mode" in user-facing copy (fine in code).

## 4. Screens

### 4.1 Tree view — plain (no baseline)

Unchanged from today except the hint line:

```
 gdu ~ [ back in time · { compare · O snapshots · ? help
 --- /home/michael ---
    98.4 GiB  45.8% ██████████  /Media
    62.1 GiB  28.9% ███████     /Library
 Total disk usage: 214.6 GiB  Apparent size: 209.8 GiB  Items: 1 204 511
```

### 4.2 Tree view — compare (baseline set)

Same table anatomy as plain view with a **Δ column appended**; all optional columns
(`%`, count, mtime, marks) keep working. The bar stays **usage-scaled** (it is the same bar
as the plain view; the Δ magnitude ranking comes from the sort and the signed numbers).
Removed items render inline: parenthesized then-size, `✗` marker, name + `(removed)`.

```
 ● Viewing   live /home/michael — scanned 14:02
 ◇ Baseline  2026-07-14 09:30 (7 d ago) — Δ shown · Tab plain
 --- /home/michael ---
    98.4 GiB  45.8% ██████████  ▲ +12.3 GiB   /Media
     4.1 GiB   1.9% █           ✦  +4.1 GiB   /node_modules
    62.1 GiB  28.9% ███████     ▼  −1.2 GiB   /Library
    (9.8 GiB)       —           ✗  −9.8 GiB   old-backup.dmg (removed)
 Growth: +16.4 GiB grown · −11.0 GiB shrunk · net +5.4 GiB · Sorting by: Δ desc
```

- **Tab** toggles Δ rendering. Δ hidden: rows render exactly as plain view; header line 2
  stays with tail `Δ hidden · Tab compare`. The comparison is never silently alive (fixes
  the F2-class invisibility).
- Delta markers/categories unchanged: `▲` grew, `▼` shrank, `✦` new, `✗` removed, `~`
  approx, `?` uncovered, `·` unchanged — the existing CVD-safe triad and `--no-color`
  glyph story carry over.
- Marks render in compare view exactly as in plain view; `d`/`e` work on a live Viewing
  tree (J4). A delete re-renders the compare view with updated deltas.

### Sorting — per-mode memory

- Plain view and compare view each remember their own `(sortBy, direction)` for the session.
- Compare view's default: **Δ descending** (biggest growth first). Plain default unchanged.
- All sort keys work in both modes and modify the *active* mode's sort; re-press flips
  direction (the app-wide convention). New key **`D` = sort by Δ** (compare view; in plain
  view it teach-flashes). **`>` and `<` retire** — Δ-asc (biggest shrink/removed first)
  replaces `<`.
- Rationale: this is the sort-order expression of Tab-as-counterpart — *the other rendering
  is exactly as you left it*. It avoids both the forced-reset and the leaked-state failure
  modes of a single shared sort.

### 4.3 The snapshot browser (one window, two doors)

`showSnapshotPicker`/`showOpenPicker` collapse into **one browser**. `O` opens it with the
`●` cursor active, `B` with the `◇` cursor active — same window, different initial focus.

```
┌ Snapshots — /home/michael ─────────────────────────────────────┐
│ ● Viewing   live — scanned 14:02                               │
│ ◇ Baseline  none → 2026-07-14 09:30 (pending)                  │
├────────────────────────────────────────────────────────────────┤
│ ●  live         scanned 14:02   214.6 GiB      —    (this scan)│
│    07-20 18:11  26 h ago        213.9 GiB  +0.7 GiB  ~/        │
│ ◇▸ 07-14 09:30  7 d ago         211.9 GiB  +2.7 GiB  ~/        │
│    06-30 22:04  3 wk ago        209.4 GiB  +5.2 GiB  ~/        │
│    06-19 15:30  1 mo ago        207.1 GiB  +7.5 GiB  /         │
│  other roots (view only)                                       │
│    07-01 02:00  3 wk ago            —          —     /Volumes/SD│
├────────────────────────────────────────────────────────────────┤
│ Tab move ● · [ ] ● · { } ◇ · Enter apply · Esc cancel          │
└────────────────────────────────────────────────────────────────┘
```

- **Header lines mirror the tree view** — same glyphs, same copy shapes; pending changes
  render as `old → new (pending)`.
- **The live scan is a real row**, pinned first, distinctly styled, `●`-only. Enter with `●`
  on it runs the existing go-live flow (instant switch when the live tree covers the folder,
  else the confirmed spot-rescan offer). The invisible timeline endpoint becomes a visible,
  chooseable object.
- **Sections**: covering snapshots (both cursors allowed) first, newest-first; then a dim
  `other roots (view only)` section (`●`-only — a baseline must cover the current folder to
  compare it). The grouping teaches the rule; no error dialogs.
- **Columns**: When (+ dim age) · This folder (async fill, as today's S picker — the fill
  machinery, generation guard, and absent/unreadable markers carry over) · Δ vs ● · Root ·
  Host (only when some snapshot is foreign). One column set serves both roles; the Δ column
  reads against wherever ● currently sits. Other-roots rows show the absent marker in the
  folder columns.
- **Cursor rules**: Tab flips which cursor arrows drive; `[` `]` and `{` `}` move their
  respective cursors directly regardless of focus. `◇` cannot rest on the live row or in
  other-roots (v1). Opening with no baseline set pre-positions the `◇` cursor on the
  snapshot immediately before `●` (the J2 default).
- **Enter applies** whatever changed — `●`, `◇`, or both (changing both lands you in a
  compare view of the new pair). **Esc/q discards** pending changes and closes. The
  startup `-f` multi-snapshot chooser remains this browser seeded from the file, `●`-only,
  with its existing Esc-quits behavior.
- The hint line keeps teaching the scriptable equivalents (`gdu --baseline …`,
  `gdu --snapshot …`) exactly as today.

### 4.4 Scan screens — the Tab boundary

**Rule: the partial preview belongs to the scan screen's pair, and a partial tree never
renders Δ.** The boundary is scan completion — there is no state where Tab is ambiguous.

```
 while the scan runs                        when it completes
 ──────────────────────────────────        ────────────────────────
  progress  ◄── Tab ──►  preview            results tree
  screen                 (partial —          plain ◄── Tab ──► Δ
     │                    plain only)        (when ◇ is set)
     │ [  step into the past
     ▼
  snapshot view (complete tree)
   plain ◄── Tab ──► Δ        ← Tab already means Δ here, mid-scan
```

- Progress screen: Tab enters the preview (upstream behavior). Preview: Tab or Esc returns
  to progress; the preview renders plain rows *always* (fixes F6 — diffing a partial tree
  against a complete baseline shows phantom removals).
- With a baseline set, the preview keeps the `◇` header line with tail
  `Δ paused — resumes when the scan completes`. On completion, Δ rendering resumes
  automatically — a refresh-while-comparing just updates the diff.
- Stepped into the past mid-scan (a complete snapshot view over a background scan): the full
  grammar applies there, including Tab = plain ↔ Δ.
- **Completion flash** (adopted): when a scan finishes and a previous covering snapshot
  exists, flash the footer: `+2.7 GiB since 07-14 — { to compare`. Reuses the existing
  micro-diff footer idiom; the root's previous total comes cheaply from the footer manifest
  (`total_dsize`), no data-page reads.
- `{`/`}` on the progress screen: flash `scan running — Δ available when it completes`
  (pre-arming is future work).

### 4.5 Launcher

Keys unchanged (`s` open latest, `S` picker — established fork keys on a different screen;
F7 is resolved by documentation, not remapping). The launcher's `S` now opens the **unified
browser scoped to the row's root** with `●` active; the full two-cursor grammar is available
inside, so a view *and* a baseline can be chosen before the first tree is ever shown —
landing directly in a compare view.

### 4.6 Degraded modes

- `--no-unicode`: `●`→`*`, `◇`→`o`; delta markers keep their existing fallbacks. (The
  existing code keys unicode fallbacks off the old-style-bar flag; keep that convention.)
- `--no-color`: glyphs carry all distinctions (that's why every state has one); bold/dim
  only, as today.
- `header.hidden`: `dirLabelPrefix` renders `[● snapshot 2026-06-19] [◇ 2026-07-14 Δ]`
  style prefixes with the new glyphs.

## 5. Keymap and divergences

| Key | Tree view | Browser | Scan screens |
|---|---|---|---|
| `[` `]` | step ● older / newer | move ● cursor | step ● (as today) |
| `{` `}` | step ◇; `{` with none set = compare vs previous | move ◇ cursor | flash "Δ when complete" |
| **Tab** | plain ↔ Δ (when ◇ set; else teach-flash) | ● ↔ ◇ focus | progress ↔ preview |
| `O` | browser, ● active | — | — |
| `B` | browser, ◇ active | — | — |
| `D` | sort by Δ (compare; else teach-flash) | — | — |
| `%` | bar-alignment toggle (was `B`) | — | same in preview |
| `>` `<` | retired | — | — |
| `S` | unbound (reserved) | — | — |
| Esc | clear ◇ → return view (unchanged ladder) | discard pending, close | exit preview / today's rules |
| Enter | navigate (unchanged) | apply pending ● and/or ◇ | — |

**Divergence log** (deliberate; for the UPSTREAM.md decision log when implemented):

| Change | Rationale |
|---|---|
| `B`: bar-alignment → snapshot browser (◇ door) | Baseline mnemonic; the evicted toggle moves to `%` |
| `%`: new home of bar-alignment toggle | The key that changes proportion display is the percent key — a better mnemonic than `B` was |
| `>` `<` retired | Direction-flipping has an app-wide convention (re-press the sort key); no private mechanism |
| `S` unbound | Superseded by `B`; reserved rather than aliased so help shrinks |
| `D` added (sort by Δ) | Joins `s n C M`; d/D adjacency is within the existing e/E risk envelope (delete confirms by default) |
| `{` `}` added (baseline stepping) | Physical mirror of `[` `]` (Shift+bracket); unbound upstream. AltGr layouts have the browser + Tab as full fallback |
| Tab extended to plain ↔ Δ | Same counterpart idiom as upstream's progress ↔ preview; pairs never co-occur on one screen |

Upstream keys otherwise untouched (`b` shell, `s n C M` sorts, `a c m` toggles, `d e r v o
i E / T space p I q Q ?`, vim navigation).

## 6. Edge rulings

- **E1** Tab with no baseline → teach-flash: `no baseline — { compare previous · B choose`.
- **E2** `{` with no covering snapshot → the existing no-coverage notice (copy updated for
  the new keys).
- **E3** `{` with no baseline set → baseline = snapshot immediately before ●'s position,
  compare view enters immediately; further `{` walks older.
- **E4** `}` stepping ◇ onto ●'s position → clears the baseline (flash `baseline cleared`).
  The gesture "walk the comparison back to nothing" mirrors `{` entering it. ◇ never equals ●.
- **E5** ● stepping onto ◇'s snapshot ([ / ]) → allowed; renders honestly (all `·`
  unchanged, Δ 0); flash `viewing the baseline snapshot`. No skip-over: hiding timeline
  points would break the timeline model.
- **E6** ◇ newer than ● (reachable when ● has stepped back) → allowed. Δ = ● − ◇ always;
  direction is legible from the two header timestamps.
- **E7** Baseline persistence: ◇ survives ● changes (stepping, `O` jumps, go-live,
  refresh) *while it still covers the shown folder*; otherwise it is cleared with a flash.
  Applies to spot-rescans (subtree Viewing trees) too.
- **E8** The mid-scan preview never renders Δ (see 4.4); `◇` line shows the paused tail.
- **E9** Scan completion: Δ rendering resumes automatically if ◇ set; completion flash when
  a previous covering snapshot exists.
- **E10** `{` `}` on the progress screen → flash only (pre-arm is future work).
- **E11** Tab precedence: while the filter bar is open, Tab focuses the filter input
  (form-focus convention wins — upstream behavior); otherwise the counterpart toggle.
- **E12** Items uncovered by the baseline keep the `?` category and marker (unchanged).
- **E13** `D` in plain view → teach-flash: `no baseline — { to compare`.
- **E14** Deletes/marks in compare view: permitted exactly when the Viewing tree is live
  (existing mutation guard); the diff re-renders after the tree updates.
- **E15** Esc ladder unchanged and exhaustive: modal/help → clear baseline → return view.
  Clearing the baseline exits compare rendering by definition (axis A drives axis B).
- **E16** Per-mode sort memory is session-scoped; it survives baseline clear/set cycles and
  is never persisted to config.

## 7. Copy inventory

All new/changed user-facing strings in one place (the deliberate-copy discipline):

| Where | Copy |
|---|---|
| Hint line (history exists) | ` gdu ~ [ back in time · { compare · O snapshots · ? help ` |
| ● line, live | `● Viewing   live /home/michael — scanned 14:02` |
| ● line, snapshot | `● Viewing   snapshot 2026-06-19 15:30 · <root> · read-only — [ ] step · Esc return` |
| ◇ line, shown | `◇ Baseline  2026-07-14 09:30 (7 d ago) — Δ shown · Tab plain` |
| ◇ line, hidden | `… — Δ hidden · Tab compare` |
| ◇ line, scanning | `… — Δ paused — resumes when the scan completes` |
| Teach-flash (Tab, no ◇) | `no baseline — { compare previous · B choose` |
| Teach-flash (D, no ◇) | `no baseline — { to compare` |
| Flash (◇ cleared via }) | `baseline cleared` |
| Flash (● on ◇) | `viewing the baseline snapshot` |
| Flash (◇ auto-cleared, E7) | `baseline no longer covers this folder — cleared` |
| Flash (scan completion) | `+2.7 GiB since 07-14 — { to compare` |
| Flash ({ } mid-scan) | `scan running — Δ available when it completes` |
| Browser title | ` Snapshots — <folder> ` |
| Browser ◇ pending | `◇ Baseline  none → 2026-07-14 09:30 (pending)` |
| Browser section | `other roots (view only)` |
| Browser hint | `Tab move ●/◇ · [ ] ● · { } ◇ · Enter apply · Esc cancel` |
| Compare footer | `Growth: +… grown · −… shrunk · −… removed (n) · net … · Sorting by: Δ desc` |

Existing copy that must be updated for the new keys: the no-coverage notice (`S` → `B`),
the old history hint, the picker titles, and the help screen's History section.

## 8. Implementation notes (existing seams)

- **Header** (`tui/header.go`): `viewingLine` must render whenever a baseline is set, live
  or not (fixes F2). The 1↔2-line grid resize (`setHeaderHeight`) is reused as-is; line
  count now keys off "baseline set", not "both slots non-empty".
- **Browser** (`tui/snapshotpicker.go`): `pickerConfig` grows a two-cursor mode replacing
  the S/O split; `fillPickerRows`, the async size fill, generation guard, and
  `activeBaselineRow` generalize (two marked rows instead of one). `showSnapshotPicker` +
  `showOpenPicker` collapse into one `showBrowser(activeRole)`. The startup `-f` chooser is
  the same component seeded from the file.
- **Compare rendering** (`tui/diff.go` + `tui/show.go`): converge `showDiffDir` toward
  `showDir` — one table-build pass where the Δ field is appended to `formatFileRow` output;
  removed-entry rows keep their dedicated renderer; marks/ignores styling unified.
- **Sort** (`tui/sort.go`): a second `(sortBy, sortOrder)` pair for compare mode;
  `getSortParams` selects by mode; `D` wires into `handleSorting`.
- **Keys** (`tui/keys.go`): Tab dispatch order — filter-input focus (open filter bar) →
  scan-screen pairs (existing) → tree Δ toggle. `handleToggles` loses `B`, gains `%`
  (also in `handlePreviewKeys`). `{` `}` join `[` `]` in the step handlers and the
  loading-page key path.
- **Baseline stepping** (`tui/timeline.go`): ◇ walks the same covering timeline as ● with
  its own position; baseline loads reuse the off-loop load + loading-page pattern
  (`setBaselineFromListing`).
- **Preview** (`tui/keys.go` `enterPreview` path): force plain rendering while previewing
  regardless of baseline state (fixes F6).
- **Completion flash**: at the scan-complete hook, list covering snapshots (manifest-only)
  and compare `total_dsize` — no tree reads.
- **Testing**: extend `keys_test`, `diff_test`, `snapshotpicker_test`, `timeline_test`;
  drive the Tab boundary, browser cursor flows, and header states end-to-end through the
  pty harness (the `verify` skill) — this class of change is exactly where
  `QueueUpdateDraw` self-deadlocks hide.
- **Docs**: help text (History + Sort sections, `%` relocation), README, FORK.md tour,
  configuration.md (no new config keys in v1 — say so), `gdu.1.md` + `make gdu.1`.

## 9. Phasing (each stage ships)

Stages are strictly sequential: each bases on the previous stage's merged result.
**Copy staging:** a stage advertises only keys that exist when it ships — the §7 inventory
is the *final* state; transitional copy is expected mid-sequence and stage 4 retires the
last of it. Each stage keeps the help screen truthful for any key it adds, removes, or
moves; the full help restructure is stage 6's.

1. **Header + grammar**: ●/◇ glyphs with fallbacks, always-on Viewing line, ◇ line with a
   plain `Δ shown` tail (the Tab hint arrives with stage 2), header-hidden prefixes, copy
   sweep (old `●` meaning removed). Pure presentation; no behavior change.
2. **Compare table unification**: normal anatomy + Δ column, marks visible, per-mode sort,
   `D`, retire `>`/`<`; the **Tab plain ↔ Δ toggle** with the E11 filter-bar precedence
   (teach-flashes use transitional copy naming today's keys).
3. **Unified browser**: one window, O/B doors, live row, sections, two cursors, Enter/Esc
   semantics; `%` relocation lands here (same commit as the `B` rebind); `S` unbound and
   every copy string naming `S` swept to `B`.
4. **Baseline stepping**: `{` `}`, E3–E7 rulings, final teach-flash and hint-line copy
   (retires all remaining transitional copy).
5. **Scan boundary**: preview never diffs, paused tail, completion flash, E8–E10.
6. **Docs**: help restructure, README, FORK.md, configuration.md (no new keys — say so),
   `gdu.1.md` + `make gdu.1`; fold surviving rationale into DESIGN.md and CLAUDE.md,
   append the §5 divergence log to UPSTREAM.md's decision log, then delete this document.

## 10. Future work (explicitly out of v1)

- **Timeline strip**: a transient one-line strip flashed on `[` `]` `{` `}` showing dots for
  snapshots with ● and ◇ positioned on it — spatial feedback for stepping. Revisit once the
  new footer flashes are felt in practice.
- **`{` pre-arming**: pressing `{` during a scan arms "compare vs previous when the scan
  completes" — scan, one keypress, coffee, growth report.
- **Live as baseline**: requires a live-tree baseline builder (today's baseline is
  Parquet-fed); would enable "how does June differ from now" phrased either way.
- **`-f` files as baseline sources** in the browser.
- **Config surface**: none added in v1; revisit only if real usage asks for defaults (e.g.
  auto-compare-on-open).

## 11. Delegating the stages

One clean agent session per stage, in order, following the delegation convention in
[UPSTREAM.md](UPSTREAM.md). Standing rules for every stage, in addition to that document's
two (confirm derived facts before anything irreversible; report what was verified, not just
what was changed):

- Read this document fully before touching code, plus CLAUDE.md's TUI sections. If this
  document is missing from your base, stop and say so — the base is behind.
- Base on the default branch with all prior stages merged; do not start a stage while the
  previous one is unmerged.
- Honor the §9 copy-staging rule: never advertise a key that doesn't exist yet.
- Update any CLAUDE.md claims your stage invalidates, in the same change.
- Verify with `make lint` and `make test`, and drive every TUI-visible change end-to-end
  through the pty harness (the `verify` skill) — this class of change is where
  `QueueUpdateDraw` deadlocks and layout regressions hide.

Canonical requests (adjust freely):

- **Stage 1** — *"Implement stage 1 (header + grammar) of docs/history-ux-plan.md: the ●/◇
  role glyphs with their `--no-unicode`/`--no-color` fallbacks, the always-on Viewing line
  whenever a baseline is set (fixes finding F2), the ◇ line with its `Δ shown` tail, the
  header-hidden prefixes, and the sweep removing the old ●=active-baseline meaning from the
  picker. Presentation only — no new keys, no behavior change; keep today's key names in
  all hints. Verify the header states through the pty harness (plain live, snapshot view,
  baseline set, header-hidden), then push and hand back for review."*
- **Stage 2** — *"Implement stage 2 (compare table unification) of docs/history-ux-plan.md
  §4.2: compare view = the normal table anatomy plus an appended Δ column (bar stays
  usage-scaled, removed rows inline), marks visible, per-mode sort memory defaulting to Δ
  desc, the `D` sort key, retire `>`/`<`, and Tab toggling plain ↔ Δ when a baseline is set
  (rulings E11–E16; teach-flashes use transitional copy naming today's keys). Update the
  help lines for `D` and `>`/`<`. Verify by pty: set a baseline, Tab both ways, sort in
  both modes, mark and delete inside the compare view."*
- **Stage 3** — *"Implement stage 3 (unified browser) of docs/history-ux-plan.md §4.3: one
  browser replacing the S/O pickers — `O` opens with ● active, `B` with ◇ active (rebinding
  `B` and moving the bar-alignment toggle to `%` in the same commit, including the mid-scan
  preview key list and help), `S` unbound and all copy naming `S` swept; the pinned live
  row wired to the existing go-live flow; covering and other-roots sections; two
  always-visible cursors with Tab focus flip and `[` `]` `{` `}` cursor moves in-browser;
  default-◇ pre-position; Enter applies, Esc discards; the `-f` startup chooser reseeded;
  launcher `S` opening the browser row-scoped (§4.5). This is the largest stage — sequence
  commits so each compiles and passes. Verify the browser flows by pty."*
- **Stage 4** — *"Implement stage 4 (baseline stepping) of docs/history-ux-plan.md: `{`/`}`
  step the baseline per §4.2 and rulings E3–E7 (`{` with none set = compare vs previous;
  `}` onto ● clears; ● onto ◇ renders honest zero; coverage-loss auto-clears), with
  baseline loads off the event loop behind the loading page, reusing the covering-timeline
  machinery. Land the final §7 teach-flash and hint-line copy, retiring all transitional
  copy. Verify stepping and every E3–E7 ruling by pty."*
- **Stage 5** — *"Implement stage 5 (scan boundary) of docs/history-ux-plan.md §4.4 and
  rulings E8–E10: the mid-scan preview never renders Δ (fixes finding F6 — add a
  regression test for baseline + `r` + Tab), the ◇ paused tail during scans with Δ
  resuming on completion, the completion flash from manifest totals, and `{`/`}` on the
  progress screen flashing only. Verify with a real scan through the pty harness,
  including stepping into the past mid-scan."*
- **Stage 6** — *"Finish the history UX plan with stage 6 (docs) per
  docs/history-ux-plan.md §9: restructure the help History and Sort sections, update
  README, FORK.md, configuration.md (no new config keys — say so), and gdu.1.md with
  `make gdu.1`; audit every user-facing string against §7; fold the plan's surviving
  rationale into docs/DESIGN.md and CLAUDE.md; append the §5 divergence log to
  docs/UPSTREAM.md's decision log; then delete docs/history-ux-plan.md."*
