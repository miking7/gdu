# Release process (this fork)

Fork releases publish plain binaries only — darwin/linux × amd64/arm64 tar.gz archives
plus `checksums.txt` — via GoReleaser ([.goreleaser.yaml](../.goreleaser.yaml), driven by
[.github/workflows/release.yml](../.github/workflows/release.yml)). None of upstream's
Docker/winget/snap/AUR packaging.

**Tag scheme:** `v<upstream-base>-parquet.<n>` — the upstream tag the fork is currently
based on (`git describe --tags --abbrev=0`), plus a fork counter. Bump `.<n>` for
successive fork releases; after merging a newer upstream release, restart at `.1` on the
new base (e.g. `v5.36.1-parquet.2` → merge upstream v5.37.0 → `v5.37.0-parquet.1`).

1. `make lint && make test`, commit everything, push `master`
2. optional local rehearsal: `goreleaser release --snapshot --clean` (artifacts in `dist/`)
3. tag: `git tag -a v5.36.1-parquet.2 -m "gdu v5.36.1-parquet.2"` — plain `-a`, not `-sa`:
   no GPG signing key is configured, and `-sa` fails outright rather than falling back
   (nothing in the release path checks tag signatures)
4. push **the tag by name** — never `git push --tags`; the repo carries upstream's v5.x
   tags (the workflow's `v*-parquet*` filter is the backstop):
   `git push origin v5.36.1-parquet.2`
5. the Release workflow runs tests, cross-builds, and creates a **draft** release with
   auto-generated notes
6. review the draft under GitHub → Releases, then **Publish**

If the workflow goes red on the test step, suspect the known flaky `cmd/gdu/app` GUI tests
before believing it: re-run the failed job from the Actions tab — the tag is unchanged, so
no re-tagging is needed.

Stable download URLs (archive names carry no version):
`https://github.com/miking7/gdu/releases/latest/download/gdu_<os>_<arch>.tar.gz`

macOS Gatekeeper: binaries are unsigned/un-notarized. Browser downloads get quarantined —
users run `xattr -d com.apple.quarantine ./gdu` (or right-click → Open) once. `curl`
downloads carry no quarantine flag:
`curl -L https://github.com/miking7/gdu/releases/latest/download/gdu_darwin_arm64.tar.gz | tar xz`

# Upstream release process (reference — not used in this fork)

1. update usage in README.md and gdu.1.md
1. `make show-man`
1. `make man`
1. commit the changes
1. tag new version with `-sa`
1. `make`
1. `git push --tags`
1. `git push`
1. relase is created on Github from the tag, or `make release`
1. update `gdu.spec`
1. Release snapcraft, AUR, ...
