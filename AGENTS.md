# Agents

## Code Hooks

Hook repository: `https://github.com/mose-x/code-hooks`

The hooks (pre-commit identity + lang fmt/lint, commit-msg message rules, pre-push branch + identity + tag validation) are the single source of truth; CI commit-lint is the backstop. Do not bypass with `--no-verify` unless a ref deletion/force-push is truly unavoidable (the hook can't process those — see Caveats below).

Key rules (full rules take precedence from the hook repo):
- Both commit author **and** committer emails must be on the allowlist (`602187256@qq.com`).
- Total commit message length must NOT exceed 200 characters.
- Conventional Commits format (`feat: ...`, `fix(scope): ...`, `docs: ...`, `test: ...`).
- Subject ASCII only, <=100 chars. No forbidden tokens (`co-authored by`, `trae agent`, etc.).
- Allowed push branch: `dev` only. `main` is protected, must go through PR.

## Branches

- `main` — release branch. A `vX.Y.Z` tag push triggers `build.yml` (6-platform build).
- `dev` — the only pushable working branch. `[allowed_branches]` in `hook-rules.conf` is `dev` only. No other branch can be pushed and `main` cannot be pushed directly.

## Unit Test Requirement

**All new code, bug fixes, and refactors MUST include unit tests. No exceptions.**

- Every new function, bug fix, or behavior change requires at least one test.
- Tests should cover: **happy path, edge cases, and error paths**.
- Go tests live alongside the code: `*_test.go` in the same package.
- Use `t.TempDir()` for filesystem-touching logic (fake SVC home via `config.Config{}.SetHomeDir(dir); cfg.SetSvcDir(dir)`); **never touch the real `~/.svc/` in tests**.
- Tests must be platform-aware where the code is (use `runtime.GOOS`/`runtime.GOARCH` for .exe vs bare-name, hardlink vs .cmd wrapper) so they pass on all 3 CI OSes (windows/macos/linux).
- Pure-logic functions (parsers, mappers, alias maps, version comparison) get pure-logic tests (no filesystem). Extract arch-dependent logic into pure functions taking `(goos, goarch string)` params so all 6 platform combos are testable on any host.
- Shell script changes require syntax checks (`bash -n`) at minimum; content-verification tests are encouraged.
- PRs without tests for their code changes will NOT be merged.

### Existing test locations

- SDK fetchers: `internal/sdk/*_test.go` (rust, dotnet, flutter, perl, php, android, jdk, nodejs, dart, endpoint)
- Shim system: `internal/shim/shim_test.go` + `internal/shimmanager/manager_test.go` + `rcfile_unix_test.go`
- Install lifecycle: `internal/installer/*_test.go` (residual-dir filter, reinstall wait, fetcher locks, checksum, extractor failure)
- Import flows: `internal/importer/importer_test.go` (atomic copy, layout alignment, critical files)
- Storage/migration: `internal/storage/*_test.go`
- Updater: `internal/update/*_test.go` (script rendering, sha256, ParseAppInfo)
- Settings/proxy/helpers/pkgmgr/logmgr/wailsrt: `internal/<pkg>/*_test.go`
- Installer template: `internal/packaging/project_nsi_test.go`
- RC file (Unix): `internal/shimmanager/rcfile_unix_test.go`
- Shared bootstrap: `internal/appcore/appcore_test.go`; the GUI/CLI drift guard is `app_startup_test.go` in the root package
- Operation lock: `internal/svclock/svclock_test.go`; windowless spawn: `internal/detach/*_test.go` including the `nospawn_guard_test.go` AST guard
- CLI shell: `internal/cli/*_test.go` — one `cmd_<group>_test.go` per command group, plus the output layer (`output_test.go`, `format_test.go`), progress (`progress_test.go`), exit codes (`exitcode_test.go`), options/mode/confirm and `root_test.go`
- CLI runtime and catalogs: `internal/cli/clirt/*_test.go`, `internal/cli/i18n/*_test.go` (`catalog_drift_test.go` pins the CLI reader to the frontend's `error.ts`)
- CLI/GUI parity guards: `internal/cli/output_guard_test.go` (wording shared with the GUI, no dead `cli` keys), `progress_guard_test.go` (progress text read back out of the TSX), `events_guard_test.go` (event names), `dispatch_test.go` (legacy dispatch + SDK name collisions), `cmd_import_test.go` (`TestNoCliCommandReachesForAFileDialog`)
- README guards: `readme_test.go` in the root package — the Homebrew install path, and the CLI chapter walked against the live command tree

## Command-Line Shell

`svc` is one binary with two shells. `main.go` asks `cli.Dispatch(os.Args[1:])` which one handles argv: no arguments launches the GUI, `init` / `setup` / `version` / `-v` / `--version` keep their original pre-CLI code paths byte for byte, and everything else goes to the cobra tree.

- `internal/cli` — the cobra tree (`cmd_*.go`, one file per command group), the output layer, the progress renderers, the exit-code mapping and the op-lock wrapper.
- `internal/cli/clirt` — the runtime adapter (signals, quit) behind an interface, so the tree is testable without Wails.
- `internal/cli/i18n` — reads the GUI's own catalogs. The root package embeds `frontend/src/i18n/{zh,en}.json` (`i18n_embed.go`) and passes the bytes in, because only the root package can see `frontend/`.
- `internal/appcore` — the service bootstrap both shells share. A CLI command never constructs a service itself.

### Contracts a new command must keep

- **stdout is results only.** Progress, notes, warnings and lock messages go to stderr (`Printer.Note`, `ProgressRenderer`). `svc list | grep` and `svc install --json | jq` both depend on it.
- **`--json` emits exactly one document on stdout.** A command with nothing to report still emits one, so a cron job can parse unconditionally.
- **Never open a window or a file dialog, and never relaunch the binary.** A path is an argument; a confirmation is `--yes`. `TestNoCliCommandReachesForAFileDialog` scans every `cmd_*.go` for dialog calls, and `runner.swapBinary` hard-codes `update.ApplyOptions{Relaunch: false}`.
- **The exit code comes from the error, not from the command.** Return an `apperr` and `exitcode.go` maps its code; that table is exhaustive and `exitcode_test.go` fails when `apperr` declares a code the table lacks. A mistyped command line must be a `UsageError` (exit 2) rather than a bare `fmt.Errorf` (exit 1), which is what the `noArgs` / `exactArgs` / `rangeArgs` / `anyArgs` wrappers are for.
- **Wording is borrowed from the GUI catalogs, not rewritten.** A command that ports a window reuses that window's keys; only text with no GUI counterpart goes into the `cli` group of *both* `zh.json` and `en.json`. `TestCliWordingIsSharedWithTheGUI` and `TestCliCatalogGroupHasNoDeadKeys` enforce the two halves.
- **A mutating command takes the op lock after its confirmation**, through `runner.withOpLock`, at the outermost point of one user-visible operation. Never take it inside a service: installer calls storage, and the lock is not re-entrant.

### Adding a command

1. `internal/cli/cmd_<group>.go` plus `cmd_<group>_test.go` in the same package.
2. Register the constructor in `addCommands` (`internal/cli/commands.go`). The list is explicit on purpose, so the whole shipped surface is visible in one place and the collision guard enumerates exactly that tree.
3. The name must not collide with a binary any SDK installs (`npm`, `go`, `java`, ...) — `TestNoCommandNameCollidesWithAnSdkBinary` fails if it does. That is why the npm-scoped commands live under `svc pkg`.
4. Bootstrap through `readCore()` unless the command genuinely writes. `writeCore()` is the only path to `appcore.LevelWrite`, the level permitted to edit the shell rc or the Windows registry PATH.
5. Document it in the CLI chapter of **both** `README.md` and `README.ZH_CN.md`, as a table row or an example. `readme_test.go` walks the live command tree and fails until both do; it also pins the global-flag table, the exit-code table, the SDK count and the legacy invocations.

### Testing a command

Drive `cli.Execute` with buffers and inject the seams: `OpenHome` (a `config.Config` pointed at `t.TempDir()` via `SetHomeDir` / `SetSvcDir`), `Env` (so the resolved mode does not depend on whether the test binary happens to have a tty), and `Spawn` / `Swap` (so no background process outlives the suite). Never touch the real `~/.svc`, and never let a test run the real swap script: it waits for *this* process to exit and then renames the binary it ran from, which under `go test` is the test executable.

### Help text stays English

cobra's usage and help are not localized, and that is deliberate rather than an oversight. `--lang` is itself a flag, so the catalog cannot be resolved before the parse that failed; a translated usage template would mean a second copy of several hundred lines of `Short` / `Long` prose with nothing to diff it against. What users actually read — results, errors, progress — is localized through the catalogs, and the Chinese README carries the full command reference.

## TODO Tracking

When a fix or feature addresses an item documented in a tracking file (e.g. `note/todo.md`), the **same commit** (or PR) that fixes the code MUST also update the item's status in the tracking file (e.g. change `unfixed` to `fixed (#PR)`). This keeps tracking files in sync with the codebase — no stale unfixed items for already-fixed bugs.

Rules:
- If the tracking file is in `.gitignore` (like `note/`), update it locally but do NOT `git add` it — the status is for local reference only.
- If the tracking file IS tracked by git, `git add` it together with the code fix so both land in the same commit.
- Update status to include the PR number or commit hash for traceability (e.g. `fixed (#74)`).

## Why Recreate `dev` Each Cycle

**Always create a fresh `dev` branch from `main` for each development cycle. Never reuse an old `dev` or rebase `dev` onto `main`.**

`main`'s squash-merge commits have `committer = GitHub <noreply@github.com>`. The pre-push identity scan rejects any noreply-committer commit inside `dev`'s push range. If `dev` is rebased onto or merged with `main`, the noreply squash commits enter `dev`'s push range and pre-push fails.

Creating `dev` fresh from `main` each cycle keeps the push range = only the new commits (main's noreply squashes sit on `origin/main`, not counted as "new" and not scanned), so pre-push passes.

## Caveats

- `git push --delete <branch>` and force-pushes are rejected by the pre-push hook (it errors on `new=0000` ref deletions and on noreply in a force-push range). Use the GitHub API (`gh api -X DELETE repos/mose-x/svc/git/refs/heads/dev`) or `gh release delete --cleanup-tag` for tags.
- CI merge race: after CI completes, `mergeStateStatus` may show `BLOCKED` for ~10-30s before flipping to `CLEAN` (the push-event run has `commit-lint` skipped; the PR-event run passes). Poll `gh pr view <n> --json mergeStateStatus` until `CLEAN` before merging.

## Development Workflow (Mandatory)

Every code change MUST follow this exact sequence. No steps may be skipped.

### Step 1: Branch
```bash
git checkout main && git fetch origin && git merge --ff-only origin/main
git checkout -b dev   # only "dev" is allowed by pre-push hook
```

### Step 2: Code + Tests
- Write the fix/feature code.
- Write unit tests covering happy path + edge cases + error paths.
- Extract arch-dependent logic into pure functions taking `(goos, goarch string)` for cross-platform testability.
- If modifying shell scripts (`scripts/*.sh`), run `bash -n` for syntax + ensure LF line endings (`.gitattributes` forces `*.sh` to LF).

### Step 3: Local Verification (MUST pass before commit)
```bash
gofmt -w .                    # auto-format
go vet ./...                  # static analysis
go build ./...                # compile check
go test ./...                 # ALL tests must pass
```

For CRLF check on Go files (Windows autocrlf artifacts):
```bash
for f in <changed .go files>; do tr -d '\r' < "$f" > /tmp/x.lf && gofmt -l /tmp/x.lf; done
# (empty output = clean)
```

**If `go test ./...` fails, do NOT proceed. Fix the failing test first.**
**Do NOT use `--no-verify` to bypass hooks — fix the root cause.**

### Step 4: Commit
```bash
git add <specific files>   # never `git add -A` or `git add .`
git commit -m "fix(scope): description"
```

The pre-commit hook will run `go vet` on staged Go files. If the hook rejects, fix the issue and re-commit.

### Step 5: Push
```bash
# Delete old remote dev if it exists (from previous PRs):
# Use gh API (not git push --delete — the hook can't handle ref deletions):
HTTPS_PROXY=http://127.0.0.1:7890 gh api -X DELETE repos/mose-x/svc/git/refs/heads/dev 2>/dev/null || true

# Push with proxy (Clash on port 7890 — github.com is blocked without proxy):
HTTPS_PROXY=http://127.0.0.1:7890 HTTP_PROXY=http://127.0.0.1:7890 git push -u origin dev
```

The pre-push hook will scan the push range + run the per-language `test` stage (`go test`).

### Step 6: PR + CI
```bash
GH="/c/Users/mose/AppData/Local/Microsoft/WinGet/Packages/GitHub.cli_Microsoft.Winget.Source_8wekyb3d8bbwe/bin/gh.exe"
HTTPS_PROXY=http://127.0.0.1:7890 HTTP_PROXY=http://127.0.0.1:7890 "$GH" pr create --base main --head dev --title "..." --body "..."
```

CI runs on the PR (`.github/workflows/ci.yml`):
1. `gofmt -l .` + `go vet ./...` (Linux)
2. `go build ./...` + `go test ./...` (Ubuntu, Windows, macOS — 3 OSes)
3. `commit-lint` (PR only — checks conventional commits + identity)

**ALL checks must pass (green). Do NOT merge if any check fails or is pending.**

### Step 7: Merge
```bash
# Poll mergeStateStatus until CLEAN (avoids the push/PR event race):
for i in $(seq 1 10); do
  S=$(HTTPS_PROXY=http://127.0.0.1:7890 "$GH" pr view <PR_N> --json mergeStateStatus --jq .mergeStateStatus)
  echo "[$i] $S"
  case "$S" in CLEAN|BEHIND) break;; esac
  sleep 10
done
HTTPS_PROXY=http://127.0.0.1:7890 "$GH" pr merge <PR_N> --squash
```

### Step 8: Cleanup
```bash
# Delete remote dev (via API — git push --delete is rejected by the hook):
HTTPS_PROXY=http://127.0.0.1:7890 "$GH" api -X DELETE repos/mose-x/svc/git/refs/heads/dev
# Sync local main:
HTTPS_PROXY=http://127.0.0.1:7890 git fetch origin
git checkout main && git merge --ff-only origin/main
git branch -D dev
```

### Step 9: Tag (only if releasing a new version)
```bash
# Delete old tag if re-tagging (via gh release delete --cleanup-tag, server-side):
HTTPS_PROXY=http://127.0.0.1:7890 "$GH" release delete v<X.Y.Z> --cleanup-tag --yes
# Re-create tag at current main HEAD:
git tag -a v<X.Y.Z> -m "Release vX.Y.Z: ..."
HTTPS_PROXY=http://127.0.0.1:7890 git push origin v<X.Y.Z>
```

Tagging triggers `build.yml` -> builds 6 platform assets (Win x64/arm64, macOS x64/arm64, Linux x64/arm64) + `sha256sums.txt` + creates a GitHub Release.

## Local Environment Setup (Windows)

### Prerequisites
- **Go 1.25+** (via SVC or system)
- **Node.js 18+** (via SVC or system) — for Wails frontend build
- **Wails CLI**: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- **Clash proxy** on port 7890 (github.com is GFW-blocked; all git/gh commands need `HTTPS_PROXY=http://127.0.0.1:7890`)
- **gh CLI** installed via winget (`winget install GitHub.cli`) + `gh auth login` (interactive, browser flow)

### Proxy pattern
All git push/fetch and gh commands need the proxy:
```bash
HTTPS_PROXY=http://127.0.0.1:7890 HTTP_PROXY=http://127.0.0.1:7890 git push origin dev
```
GitHub-hosted CI runners are NOT behind the GFW — they don't need the proxy. Only local commands do.

### Known issues
- **CRLF**: Windows autocrlf converts .go files to CRLF in the working tree. CI (Linux) checks out LF — `gofmt -l` may show false "needs fmt" locally. Use `tr -d '\r' < file | gofmt -l` to verify on LF-normalized content. `.gitattributes` forces `*.sh` to LF (CRLF breaks bash heredocs/case on Linux/macOS).
- **`python3` on Windows**: SVC's Python shim only provides `python` (Windows CPython has no `python3.exe`). The code-hooks `commit-msg` hook uses `python3 -c` for message-length counting. SVC now creates the `python3` alias itself (PR #69/#71); if the hook still fails, a Go-compiled `python3.exe` wrapper can be placed at `~/.svc/shims/python3.exe` (delegates to the `python` shim).
- **Merge race**: after CI completes, `mergeStateStatus` may show `BLOCKED` for 10-30s before flipping to `CLEAN`. Poll before merging (see Step 7).
- **`--no-verify`**: never use for normal commits/pushes. Only for ref deletions/force-pushes that the hook can't process. Prefer the GitHub API (`gh api -X DELETE`) for branch/tag deletion.
- **Dev branch stale commits**: if `dev` accumulates stale commits from prior PRs, the merge-base goes stale -> add/add conflicts. Always recreate `dev` fresh from `main` each cycle (Step 1).
