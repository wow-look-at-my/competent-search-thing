# CI workflow notes

Explanations that used to live as multi-line comment blocks in
`.github/workflows/ci.yml` live here, so the workflow stays within the
one-comment-line-per-block limit enforced by
`wow-look-at-my/actions@yaml-comment-block`. Each section is keyed to
the step it explains; the workflow's single-line comments point here.

## Permissions

- `id-token: write` lets the buildhost actions (the deb steps and the
  publish job) mint the OIDC token they authenticate to pazer.build
  with.
- `contents: write` is required by go-toolchain's dependency-graph
  submission (a 403 from the Dependency Submission API fails the
  build).
- `artifact-metadata: write` -- buildhost-publish-release records every
  artifact the publish makes public on the org's linked artifacts page;
  that recording is part of publishing, not optional, so the publish
  fails without this.

## linux job

### Check sources are ASCII-only

CLAUDE.md requires ASCII in every source file, and that rule has been
broken twice by the same mechanism: gofmt applies typographic
substitution inside DOC COMMENTS, so a comment containing a doubled
straight quote gets rewritten to a curly one. The tree then carries a
non-ASCII byte, and whichever gofmt build runs next may disagree about
canonical form and fail the build with no obvious cause. A prose-only
rule did not catch it; the byte check does.

### Build frontend

`npm test` is the vitest DOM-ordering gate (priority sections above
file rows + the flat selection rules).

### go-toolchain autorelease pinning (linux)

The dedicated publish job is the SINGLE publish path. The action
DEFAULTS autorelease to 'true', so it must be pinned off explicitly or
this job publishes all of build/ (incl. the example color-http server
binary) to buildhost itself.

### Hand off the linux binary to the publish job

Org-standard cache-backed hand-off (the wow-look-at-my/actions
cache-upload/cache-download trio) -- this org does not use the GitHub
artifact service for job hand-offs. Only the app binaries ride to the
publish job; the example color-http server binaries also land in
build/ but are a dev sample, not a published deliverable. Distinct
hand-off names keep the cache keys
(cache-xfer-<name>-<run_id>-<run_attempt>) collision-free across jobs.

### Build Debian package

buildhost's on-the-fly deb repackaging cannot declare package
dependencies (its control file is Package/Version/Arch/Maintainer/
Description only -- internal/repackage/deb.go), so a raw-binary or
apt.pazer.build install lands without WebKitGTK and fails at the loader
on any machine that never had the build deps. Build a real .deb here
instead: same binary, plus a Depends line derived from the binary's
direct NEEDED libs (objdump -p), package names verified on Ubuntu 24.04
and 22.04 (noble's t64 packages Provide the unsuffixed names, so one
line covers both; glibc floor is GLIBC_2.34).

## darwin job

### go-toolchain autorelease pinning (darwin)

Same pinning as the linux job: the publish job is the single publish
path, and the action defaults autorelease to 'true'.

### GUI smoke

HARD GATE: a smoke failure fails the darwin job (and with it the
required all-builds status). Every SMOKE id in the script is a hard
pass/fail; screenshots are evidence, not checks.

### Upload smoke screenshots

Mirrors the linux job's Xvfb screenshots artifact: the smoke script
drops its best-effort captures in smoke-shots/ at the workspace root.
ignore (not warn): a failure before any capture -- or a TCC-restricted
screencapture -- legitimately leaves nothing to upload, and the smoke
step itself is the gate.

## publish job

ONE buildhost release per push, carrying every platform CI actually
builds, so the app project's "latest" download is always a complete
set. Runs on every branch unconditionally: branch pushes create branch
releases (?branch=<name> downloads), while the bare "latest" URL only
ever follows the default branch -- guaranteed by buildhost. The
buildhost actions mint their own OIDC token (audience
https://pazer.build) from the workflow's id-token: write grant.
Deliberately NOT restored from the pre-#25 autorelease era: the
color-http example server binary (project competent-search-thing/
server) -- a dev sample, not a user-facing deliverable.

### Download the linux binary hand-off

Single-file hand-offs restore as <dest>/<basename>, so the three
binaries land in binaries/ under their build/ names. A missing hand-off
fails the job loudly (fail-if-missing defaults true), and on a
"re-run failed jobs" the restore-keys prefix picks up the earlier
attempt's uploads.