# Release process (this fork)

Fork releases publish plain binaries only — darwin/linux × amd64/arm64 tar.gz archives
plus `checksums.txt` — via GoReleaser ([.goreleaser.yaml](../.goreleaser.yaml), driven by
[.github/workflows/release.yml](../.github/workflows/release.yml)). None of upstream's
Docker/winget/snap/AUR packaging.

**Tag scheme:** CalVer — `vYYYY.M.PATCH` (e.g. `v2026.7.0`; bump `PATCH` for re-cuts within
the month). The year prefix can never collide with upstream's inherited `v1`–`v5` tags.
Upstream lineage belongs in the release notes, not the tag string. Rationale and the
versioning decision record: [UPSTREAM.md](UPSTREAM.md). (`v5.36.1-parquet.1` predates this
scheme and remains valid history.)

1. write the notes: copy [releases/TEMPLATE.md](releases/TEMPLATE.md) to
   `docs/releases/<tag>.md`, fill it in (the matching UPSTREAM.md decision-log entry has
   most of it), commit — the workflow **fails without this file**; notes are never
   auto-generated (after an upstream rebase they would describe upstream's commits)
2. `make lint && make test`, commit everything, push `master`
3. optional local rehearsal: `goreleaser release --snapshot --clean` (artifacts in `dist/`)
4. tag: `git tag -sa v2026.7.0 -m "gdu v2026.7.0"`
5. push **the tag by name** — never `git push --tags`; the repo carries upstream's v5.x
   tags (the workflow's tag filter is the backstop): `git push origin v2026.7.0`
6. the Release workflow runs tests, cross-builds, and creates a **draft** release whose
   body is exactly `docs/releases/<tag>.md`
7. review the draft under GitHub → Releases, then **Publish**

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
1. release is created on GitHub from the tag, or `make release`
1. update `gdu.spec`
1. Release snapcraft, AUR, ...
