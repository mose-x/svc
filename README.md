# svc

A cross-platform desktop application for unified management of multiple SDK versions (Node.js, JDK, Go, Python, Rust, etc.), including installation, switching, environment variable configuration, and self-update with integrity verification.

[Chinese Documentation](README.ZH_CN.md)

## Features

### SDK Management
- **14 SDKs Supported**: Node.js, JDK, Go, Python, Rust, Ruby, .NET, PHP, Perl, Maven, Gradle, Flutter, Android, Dart
- **One-Click Install**: Fetch available versions from official sources and install with one click
- **Version Switching**: Quickly switch between installed versions without re-downloading
- **Reinstall**: Overwrite-install existing versions
- **Import**: Import SDKs from local archives or folders, or one-click import SDKs detected in PATH

### Shim Mechanism
- Auto-generates shims for SDKs with multi-bin directories (e.g. Rust `cargo/bin` + `rustc/bin`)
- Transparently routes commands to the active version without polluting PATH
- Cross-platform shim executor (Unix shell script + Windows batch)

### Package Managers
- Auto-detect and install corresponding package managers (npm, yarn, pnpm, pip, gem, cargo, etc.)
- Support package manager version updates

### Environment Configuration
- **Auto PATH Management**: Automatically configure system environment variables after install/switch
- **Custom Install Location**: Customize SDK storage directory with automatic PATH migration and desktop backup
- **PATH Viewer**: Visualize SDK-related PATH entries and detect conflicts with system-level env vars
- **Shell RC Editing**: Auto-edits `.bashrc`/`.zshrc`/`.profile`/PowerShell profile to make shims take effect

### System Settings
- **Theme**: Dark / Light / Follow System, with synchronized window title bar
- **i18n**: Chinese / English
- **Proxy**: System proxy or custom proxy support, with connection testing (Baidu / Google)
- **GitHub Mirror**: Speed up GitHub downloads via configurable mirror (e.g. `https://ghfast.top`)
- **Download Threads**: Multi-threaded segmented download with automatic single-thread fallback
- **Custom Endpoints**: Configure custom download endpoints per SDK
- **Cache Cleanup**: One-click cleanup of temporary download cache and inactive SDK versions
- **Log Management**: View, search, and clean application logs in-app

### Self-Update (since v1.0.0)
- **GitHub Releases**: Detect, download, and apply updates entirely in-app — no manual reinstall needed
- **SHA256 Verification**: Every downloaded binary is verified against the published checksum before being swapped in; corrupt or tampered payloads are rejected and deleted
- **One-Shot Rollback**: Each upgrade backs up the previous binary to `<exe>.bak`; the in-app "Rollback" button restores the immediately previous version
- **Cross-Platform Replace**: Unix uses a shell script (`pgrep` wait → backup → replace → relaunch); Windows uses a batch script (`tasklist` wait → backup → replace → relaunch) to handle running-binary locks
- **Asset Naming**: `svc-{version}-{os}-{arch}.{ext}` (amd64 → x64), with a standalone `sha256sums.txt` shipped per release

### Command-Line Interface (since v3.0.0)
- **One Binary, Two Shells**: `svc` with no arguments opens the window, `svc <command>` runs a command — both call the same services over the same files
- **Full Coverage**: every operation the window offers has a command, from `svc install` to `svc self update`
- **Scriptable**: `--json` puts one machine-readable document on stdout and moves progress to stderr, so `| jq` and `$( )` both work
- **No Surprises**: no command opens a window or a file dialog, and none of them relaunches the app
- **Stable Exit Codes**: 0–7, mapped from the same error codes the GUI translates

See [Command Line](#command-line) for the full reference.

### User Experience
- **Download Progress**: Real-time speed, progress, and size display
- **Copy Download URL**: Copy SDK download links
- **Confirmation Dialogs**: Sensitive operations (version switch, reinstall, migration, rollback) require confirmation
- **Status Indicators**: Colored dots in sidebar (green=configured, yellow=PATH only, red=not configured)

### Screenshots
#### HomePage
![HomePage](./image/home.png)
#### Node.js
![Node.js](./image/nodejs.png)
#### Jdk
![Jdk](./image/jdk.png)
#### download
![download](./image/download.png)
#### svc warning
![svc](./image/sys_path_check.png)
#### setting
![setting](./image/setting.png)

## Command Line

`svc` is one binary with two shells. Run it with no arguments and it launches the graphical application; run it with a command and it behaves like a CLI and exits. Both shells call the same services over the same files, so a version installed from a terminal shows up in the window, and the version the window activated is what `svc current` reports. The command tree arrived in v3.0.0: an older build knows only `init`, `setup`, `version` and `help`, and opens the window for anything else.

```bash
svc list                          # every SDK and its local state
svc versions nodejs --lts         # what can be installed, newest first
svc install nodejs 22.11.0        # download, verify, activate
svc switch nodejs 20.18.0         # point the shims at an installed version
svc current nodejs                # the active version, and nothing else
svc doctor                        # check svc's own setup, repair what it can
svc self update                   # update svc itself
```

Two guarantees hold for every command:

- **Nothing opens a window or a file dialog.** A command that needs a path takes it as an argument (`svc import nodejs ./node-v22.11.0.tar.xz`), and a command that needs a yes takes `--yes`.
- **Nothing is relaunched.** `svc self update` and `svc self rollback` hand the binary to a swap script and exit 0; the new version is there the next time you run `svc`.

`svc <command> --help` prints the full description, arguments and exit codes of any command.

### Legacy Invocations

These predate the CLI and keep the behaviour they have always had. `svc version` in particular is parsed by scripts, so its single output line is frozen. `svc help` is the one that changed: the command tree answers it now, and it prints the real tree rather than the static four-line list it used to.

| Invocation | Behaviour |
|---|---|
| `svc` | Launch the GUI |
| `svc init`, `svc setup` | One-time shim setup: creates `~/.svc/shims` and adds the `.svc.rc` source line |
| `svc version`, `svc -v`, `svc --version` | Print the single version line |
| `svc help`, `svc -h`, `svc --help` | Print the command tree |

### Global Flags

| Flag | Effect |
|---|---|
| `-h`, `--help` | Print the help of the command you asked for |
| `--json` | Machine-readable JSON on stdout; progress and notes move to stderr |
| `-q`, `--quiet` | Results and errors only — no progress, no notes |
| `-y`, `--yes` | Answer yes to every confirmation prompt |
| `--no-color` | Never emit ANSI colour sequences |
| `--no-wait` | Fail with exit 7 instead of waiting when another svc operation holds the lock |
| `--lang zh\|en` | Message language. Default: `$SVC_LANG`, then `language` in `settings.json` |
| `--install-path <dir>` | Operate on another SVC home for this run only; never saved |

### Output

The mode is chosen from the environment rather than from a flag:

| Situation | Output |
|---|---|
| Interactive terminal | Coloured progress bar, tables, confirmation prompts |
| Pipe or CI (stdout is not a tty) | One plain line per stage, decimated to every ten percent; no colour; a command that would ask refuses and tells you to pass `--yes` |
| `--json` | The result on stdout as JSON; progress on stderr as one JSON object per line |

**stdout is the result channel and nothing else.** Progress, warnings, lock notices and hints all go to stderr, which is what makes `svc list | grep`, `NODE_DIR=$(svc dir nodejs)/$(svc current nodejs)` and `svc install --json ... | jq` all safe.

### Exit Codes

Stable by contract: a script keys on the number rather than on a localized sentence.

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unclassified failure |
| 2 | Usage error — missing argument, unknown command or flag, an operation the SDK layout forbids |
| 3 | Not found — unknown SDK, missing path, missing version directory, no backup to roll back to, no release asset for this platform |
| 4 | Integrity — checksum mismatch, incomplete SDK tree, version mismatch |
| 5 | Cancelled — SIGINT, or a declined confirmation prompt |
| 6 | Network — HTTP status, proxy policy, disallowed scheme |
| 7 | Busy — another svc process holds the operation lock and `--no-wait` was given |

### Commands

Aliases are in parentheses.

**Inspect**

| Command | What it prints |
|---|---|
| `svc list` (`ls`) | Every SDK: active version, installed version count, PATH state, conflicts |
| `svc status <sdk>` | One SDK in full: active version, every installed version, install directory, what the copy on PATH reports itself as, system conflicts |
| `svc current [<sdk>]` | The active version, bare, so `$(svc current nodejs)` works. With no argument, every SDK's. An SDK with no active version prints nothing and still exits 0 |
| `svc dir <sdk>` | The install directory |
| `svc url <sdk> <version>` | The download URL, with the configured proxy and GitHub mirror already applied |
| `svc conflicts <sdk>` | System PATH and environment entries that compete with the shims. Exits 0 whether or not any were found |
| `svc path` | Every PATH entry that looks like an SDK, what svc identified it as, and whether svc manages it |
| `svc storage` | Disk usage per SDK, version counts, the active one, and the leftover download cache |
| `svc about` | svc's own version, the Go version it was built with, its license and its repository |

**Install and switch**

| Command | What it does |
|---|---|
| `svc versions <sdk>` | The installable versions, marking the newest, the installed, the active and the LTS ones. `--lts`, `--limit N`, `--offline`, `--refresh` |
| `svc install <sdk> <version>` | Download, verify and activate. Activation is not optional — installing also points the shims at the new version |
| `svc switch <sdk> <version>` (`use`) | Point the shims and environment at an installed version. Reopen the terminal afterwards |
| `svc reinstall <sdk> <version>` | Re-download an installed version over itself. The version must already be installed |
| `svc uninstall <sdk> <version>` (`rm`) | Delete one version's directory. Uninstalling the active version is allowed, warns, and exits 0 |
| `svc import <sdk> [<path>]` | Copy an archive or a directory into the SVC store, detect its version, activate it |
| `svc import <sdk> --from-path` | Import the copy the SDK's command currently resolves to on the system PATH |
| `svc doctor` | Run every self-check, repair what a startup can repair, report the rest. Exits 1 when something is broken |

**Package managers and npm**

| Command | What it does |
|---|---|
| `svc pkg list <sdk>` | The package managers of one SDK and their versions |
| `svc pkg install <name>` | Install a package manager |
| `svc pkg update <name>` | Update a package manager to its latest version |
| `svc pkg registry` | The npm registry of the active Node.js |
| `svc pkg registry set <url\|official\|china>` | Switch it |
| `svc pkg global list` | The globally installed npm packages |
| `svc pkg global install <package>` | Install a package globally into the active Node.js |
| `svc pkg global update <package>` | Update a global package to its latest version |
| `svc pkg global uninstall <package>` | Remove a globally installed npm package |

The group is named `pkg` rather than `npm`: a subcommand may not share a name with a binary an SDK installs, and every Node.js ships `npm`.

**Clean and logs**

| Command | What it does |
|---|---|
| `svc clean tmp` | Empty the download and extraction cache |
| `svc clean logs` | Delete every svc log file and reopen today's empty |
| `svc clean inactive <sdk>` | Delete every version of an SDK except the active one |
| `svc logs list` | List svc's own log files |
| `svc logs show <file>` | Print one of them |
| `svc logs dir` | The log directory |
| `svc logs rm <file>...` | Delete log files |

`svc clean` on its own lists its targets and deletes nothing.

**Settings and endpoints**

| Command | What it does |
|---|---|
| `svc settings show` | The effective settings, with the GitHub token masked |
| `svc settings set <key> <value>` | Change one setting: `theme`, `language`, `downloadThreads`, `githubMirror`, `proxy.enabled`, `proxy.mode`, `proxy.url`, `proxy.protocol`, `installPath` |
| `svc settings token` | Store the GitHub token, read from stdin so it never reaches the shell history. `--clear` removes it |
| `svc settings check-proxy [url...]` | Test whether the configured proxy can reach a site |
| `svc endpoints list` | Every SDK's default and effective download endpoint |
| `svc endpoints set <sdk> <url>` | Override one SDK's download endpoint |
| `svc endpoints unset <sdk>` | Restore that SDK's default endpoint |

`svc settings` also answers to `svc config`. `settings set installPath` does not edit a field: it migrates the whole SDK tree, backs the old one up to the desktop and re-runs the shim setup.

**svc itself**

| Command | What it does |
|---|---|
| `svc self check` | Whether a newer svc release is published |
| `svc self update` | Download it, verify its checksum, and hand the running binary to a swap script |
| `svc self rollback` | Restore the `<exe>.bak` the previous update left behind |

### Concurrency

Every command that changes something svc owns — `install`, `reinstall`, `switch`, `uninstall`, `import`, `clean tmp`, `clean inactive`, the `pkg` mutations, `settings set`, `settings token`, `endpoints set`, `endpoints unset`, `self update`, `self rollback` — takes a cross-process lock on the SVC directory before touching `config.json`, `shims.json` or the shell rc. A terminal running next to an open window is serialized instead of racing it. By default a command waits and says so on stderr; `--no-wait` makes it fail with exit 7.

The log commands are the exception and take no lock: they touch the log directory only, every svc process writes to it independently, and a file deleted under a running process is simply recreated.

### Shell Completion

```bash
svc completion bash        # install into your bash completion dir
svc completion zsh         # install as _svc somewhere on $fpath
svc completion fish        # install into ~/.config/fish/completions
svc completion powershell  # install into your PowerShell profile
```

Candidates come from the command tree, the registered SDK identifiers and the installed versions. Completing an argument never touches the network, so pressing Tab cannot hang the shell.

### Agent Skill

`skills/svc` is a [Codex skill](https://github.com/openai/skills): the brief an AI agent reads before driving this CLI. It carries the rules that are easy to get wrong from `--help` alone -- never run bare `svc`, pass `--yes` to anything that writes, parse `--json` rather than the table, what each exit code means -- and `skills/svc/references/commands.md` holds the full command table. `skill_test.go` walks the live tree against both files, so the skill cannot drift the way a copied list would.

```bash
cp -R skills/svc ~/.codex/skills/svc     # or let Codex's skill-installer fetch it
```

It is documentation only: no binary reads it, and `svc <command> --help` stays the authority.

## Comparison with Alternatives

| Feature | SVC (This Project) | nvm / sdkman / pyenv etc. | VS Code Plugins |
|---|:---:|:---:|:---:|
| Unified SDK management (14 types) | ✅ | ❌ Single SDK per tool | ❌ Scattered |
| Graphical user interface | ✅ | ❌ CLI only | ✅ |
| Command line covering the same operations | ✅ | ✅ | ❌ |
| Auto PATH configuration | ✅ | ⚠️ Requires shell setup | ❌ |
| Cross-platform desktop app | ✅ | ✅ | ✅ |
| Multi-threaded download | ✅ | ❌ | – |
| Custom download endpoints | ✅ | ⚠️ Partial support | – |
| GitHub mirror acceleration | ✅ | ❌ | – |
| Package manager companion | ✅ | ⚠️ Partial support | – |
| PATH visualization | ✅ | ❌ | ❌ |
| One-click SDK import (archive/folder/PATH) | ✅ | ❌ | ❌ |
| Shim mechanism for multi-bin SDKs | ✅ | ❌ | ❌ |
| Install path migration with backup | ✅ | ❌ | ❌ |
| System-wide (works with all IDEs/terminals) | ✅ | ⚠️ Shell-scoped | ❌ VS Code only |
| Conflict detection & cleanup | ✅ | ❌ | ❌ |
| In-app self-update with SHA256 + rollback | ✅ | ⚠️ Partial (no rollback) | ❌ |

### Summary

The core advantage of this project lies in its **"one-stop, graphical, cross-platform"** SDK version management experience. It solves the pain point of developers having to install and manage multiple SDK versions by unifying what previously required multiple command-line tools (nvm, sdkman, pyenv, rustup, etc.) into a single, intuitive desktop application.

No more memorizing a different CLI for each language, no more fiddling with shell config files, no more PATH bloat — one GUI and one command set that work consistently across Windows, macOS, and Linux. Use whichever shell fits the moment: the window for exploring, [the CLI](#command-line) for scripting and CI.

## Tech Stack

- **Backend**: Go 1.25 + Wails v2
- **Frontend**: React + TypeScript + Vite + Ant Design
- **Desktop**: Wails v2 (WebView2 / WebKitGTK)

## Project Structure

```
svc/
├── main.go                    # Entry point (shim mode / CLI / GUI dispatch)
├── app.go                     # Wails App facade + runtime adapter
├── about.json                 # App metadata (version, license, updateUrl)
├── internal/
│   ├── cli/                   # The command-line shell: cobra tree, output, exit codes
│   │   ├── clirt/             # Runtime adapter (signals, quit) behind an interface
│   │   └── i18n/              # Catalog reader shared with the GUI's wording
│   ├── appcore/               # Service bootstrap shared by the GUI and the CLI
│   ├── apperr/                # Typed error codes, translated by both shells
│   ├── svclock/               # Cross-process operation lock (flock / LockFileEx)
│   ├── detach/                # Windowless background spawn
│   ├── installer/             # SDK install/switch/cancel + version status
│   ├── importer/              # SDK import from archive/folder/PATH
│   ├── storage/               # Uninstall, storage info, cache, path migration
│   ├── update/                # Self-update check/download/apply/rollback
│   ├── pkgmgr/                # Package manager detection & install
│   ├── proxy/                 # Proxy config, GitHub mirror, SSRF-guarded check
│   ├── settings/              # Settings policy (token masking, save guards)
│   ├── logmgr/                # Log file management
│   ├── helpers/               # Pure utils: cmd builders, env, version, paths
│   ├── wailsrt/               # Wails runtime interface (decouples services)
│   ├── packaging/             # Build-artifact content tests (NSIS template)
│   ├── config/                # Settings, install path, shell detection
│   ├── downloader/            # Multi-threaded HTTP downloader (proxy support)
│   ├── extractor/             # Archive extraction (zip, tar.gz, 7z, etc.)
│   ├── logger/                # Structured logging
│   ├── pathmgr/               # PATH env management (unix/windows)
│   ├── fsutil/                # Filesystem helpers shared across services
│   ├── migrate/               # On-disk layout migrations
│   ├── shim/                  # Shim executor + arg detection
│   ├── shimmanager/           # Shim install + shell RC editing
│   └── sdk/                   # Version fetching & installation per SDK
├── frontend/
│   ├── src/
│   │   ├── App.tsx            # Main component
│   │   ├── components/        # Sidebar, DetailPanel, Settings, etc.
│   │   ├── i18n/              # en / zh translation files
│   │   └── types/             # TypeScript type definitions
│   └── wailsjs/               # Auto-generated Wails bindings
├── skills/
│   └── svc/                   # Agent skill: how an AI agent should drive the CLI
├── winres/                    # Windows resource (icon, manifest)
└── .github/workflows/
    ├── ci.yml                 # lint + test + commit-lint on PR
    └── build.yml              # 6-platform build + release on tag push
```

## Data Storage

- **SDK Install Directory**: Default `~/.svc/` (customizable), structure: `~/.svc/{sdk-type}/{version}/`
- **Shim Directory**: `~/.svc/shims/` (added to PATH; routes to active versions)
- **App Config**: `~/.svc/settings.json` (theme, language, proxy, endpoints, etc.)
- **Logs**: `~/.svc/logs/`
- **Update Backup**: `<exe>.bak` next to the running binary (created on each upgrade)

## Development

### Prerequisites
- Go 1.25+
- Node.js 18+
- Wails CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Linux only: `libgtk-3-dev libwebkit2gtk-4.0-dev libayatana-appindicator3-dev librsvg2-dev`

### Start Dev Server

```bash
wails dev
```

### Build

```bash
# Current platform
wails build

# Custom output filename
wails build -o svc
```

### Cross-Platform Build Notes

Wails v2 cannot cross-compile Linux (CGO links webkit2gtk/gtk) — use a native runner for Linux ARM64. Windows ARM64 and macOS ARM64 can be cross-compiled on amd64 runners (no CGO). The CI workflow in [.github/workflows/build.yml](.github/workflows/build.yml) handles all six targets automatically on tag push.

## Installation

Download the latest release for your platform from [Releases](https://github.com/mose-x/svc/releases/latest):

| Platform | Asset |
|---|---|
| Windows x64 | `svc-{ver}-windows-x64.exe` |
| Windows arm64 | `svc-{ver}-windows-arm64.exe` |
| macOS x64 | `svc-{ver}-macos-x64.dmg` |
| macOS arm64 (Apple Silicon) | `svc-{ver}-macos-arm64.dmg` |
| Linux x64 | `svc-{ver}-linux-x64` |
| Linux arm64 | `svc-{ver}-linux-arm64` |

Verify integrity with the published checksums:

```bash
sha256sum -c sha256sums.txt --ignore-missing
```

### Homebrew (macOS)

```bash
brew trust mose-x/tap
brew tap mose-x/tap
brew install --cask mose-x/tap/svc
```

Installs the `.dmg` of the latest release into `/Applications`, and links the command line onto your PATH: `/usr/local/bin/svc` points at `/Applications/svc.app/Contents/MacOS/svc`, the very binary the window runs. [Command Line](#command-line) therefore needs no setup of its own on macOS, and `brew uninstall` takes the link back off. The cask lives in [mose-x/homebrew-tap](https://github.com/mose-x/homebrew-tap), where a daily workflow rewrites the version and recomputes every `sha256` from the published release assets. Homebrew is optional: the direct download in [Installation](#installation) needs none of this.

The app is not code signed and the download arrives quarantined, so Gatekeeper kills that same binary before it prints anything: `svc list` exits `137` with no output at all. Approve it once from Finder — right-click `svc` in `/Applications`, choose Open, then Open again in the dialog — and the window and the terminal command both work from then on. `brew info --cask mose-x/tap/svc` prints this ordering as a caveat, plus the rest: uninstalling keeps `~/.svc` and the `source ~/.svc.rc` line in your shell rc.

Homebrew 7 refuses to load *any* entry of an untrusted third-party tap, the fully qualified name above included, which is why `brew trust mose-x/tap` comes first. It is idempotent and does not need the tap to exist yet, so that order is also correct on a machine that has never seen it. Homebrew 6 only required trust for the short form (`brew install --cask svc`); `brew untrust mose-x/tap` undoes it on either.

Trust is recorded per user in `~/.homebrew/trust.json` (`$XDG_CONFIG_HOME/homebrew/trust.json` when that is set), not inside the tap, so it survives `brew untap`/`brew tap` and `brew update` — but a fresh Homebrew install, a new machine or another macOS account has to run it once more. `brew trust` itself needs Homebrew 5.1.15 or newer; older releases neither enforce trust nor ship the command. Until it runs, `brew tap-info mose-x/tap` reports `Untrusted`, which is expected and harmless. Prefer it over `HOMEBREW_NO_REQUIRE_TAP_TRUST`, which upstream has deprecated and slated for removal.

## Self-Update Mechanism

There is no manifest to fetch. The app queries the GitHub Releases API at the `updateUrl` in [about.json](about.json) (`https://api.github.com/repos/mose-x/svc/releases/latest`), dropping the trailing `/latest` so it can read the release **list** newest-first, and takes the version from `tag_name`, the changelog from `body` and the download URL from `assets[]`. Any tag carrying a pre-release suffix (`-rc1`, `-beta`, `-alpha`, `-pre`, `-dev`) is skipped, so only a plain `vX.Y.Z` is ever offered, no matter how GitHub itself flagged the release. Each release also ships a standalone `sha256sums.txt`.

### Update Flow

1. **Check**: In-app "Check for Updates" reads the release list, picks the newest stable tag and compares it with the running version semantically.
2. **Download**: User clicks "Download & Install"; the new binary is downloaded to a temp path with real-time progress, applying the configured GitHub mirror and proxy.
3. **Verify**: the expected digest is looked up by filename in that release's `sha256sums.txt` asset and compared with the SHA256 of what actually landed. On mismatch the temp file is deleted and the update aborts (defense against CDN tampering or partial downloads). A release without `sha256sums.txt` logs a warning and skips verification, so older releases stay updatable.
4. **Apply**: A shell/batch script is spawned that waits for the app to exit, backs up the current binary to `<exe>.bak`, replaces it with the new one, and relaunches. On macOS it also re-stamps `CFBundleShortVersionString` and `CFBundleVersion` in the bundle's `Info.plist`, which swapping the binary alone leaves behind.
5. **Rollback** (optional): If the new version misbehaves, "Rollback" in Settings restores `<exe>.bak` via the same wait-replace-relaunch pattern. One-shot — `.bak` always holds the immediately previous version.

### Asset Selection

`matchPlatformAsset` picks the asset for the running platform out of `assets[]` by filename, following the `svc-<ver>-<os>-<arch><ext>` build convention. Neither Go token matches the asset name literally: `runtime.GOOS` `darwin` maps to `macos` and `runtime.GOARCH` `amd64` maps to `x64`.

| Platform | Self-update asset | Skipped on purpose |
| --- | --- | --- |
| Windows x64 / arm64 | `svc-<ver>-windows-x64.exe`, `svc-<ver>-windows-arm64.exe` | `*-installer.exe` — first-install only, and running it would need UAC |
| macOS x64 / arm64 | `svc-<ver>-macos-x64.bin`, `svc-<ver>-macos-arm64.bin` | `.dmg` |
| Linux x64 / arm64 | `svc-<ver>-linux-x64`, `svc-<ver>-linux-arm64` | `.deb`, `.rpm` |

> macOS uses the `.bin` (bare executable) for self-update because `ApplyUpdate` replaces the binary inside the `.app` bundle, not the bundle itself. The `.dmg` is for manual install only.

### Release a New Version

CI is fully automated — just push a tag:

```bash
git tag v1.1.0
git push origin v1.1.0
```

The [build.yml](.github/workflows/build.yml) workflow builds all six platform assets, generates `sha256sums.txt` and publishes a non-draft GitHub Release. The in-app updater picks it up immediately from the Releases API. Tags carrying a pre-release suffix are published as a GitHub pre-release, which also keeps them out of `releases/latest`.

## License

MIT License
