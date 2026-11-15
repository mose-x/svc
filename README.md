# svc

A cross-platform desktop application for unified management of multiple SDK versions (Node.js, JDK, Go, Python, Rust, etc.), including installation, switching, environment variable configuration, and self-update with integrity verification.

[Chinese Documentation](README.ZH_CN.md)

## Features

### SDK Management
- **18 SDKs Supported**: Node.js, JDK, Go, Python, Rust, Ruby, .NET, PHP, Perl, Maven, Gradle, Flutter, Android, Dart, and more
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

## Comparison with Alternatives

| Feature | SVC (This Project) | nvm / sdkman / pyenv etc. | VS Code Plugins |
|---|:---:|:---:|:---:|
| Unified SDK management (18+ types) | ✅ | ❌ Single SDK per tool | ❌ Scattered |
| Graphical user interface | ✅ | ❌ CLI only | ✅ |
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

No more memorizing CLI commands for each language, no more fiddling with shell config files, no more PATH bloat — just a clean GUI that works consistently across Windows, macOS, and Linux.

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

Installs the `.dmg` of the latest release into `/Applications`. The cask lives in [mose-x/homebrew-tap](https://github.com/mose-x/homebrew-tap), where a daily workflow rewrites the version and recomputes every `sha256` from the published release assets. Homebrew is optional: the direct download in [Installation](#installation) needs none of this.

`brew info --cask mose-x/tap/svc` prints the caveats: the app is not code signed (right-click → Open on first launch), and uninstalling keeps `~/.svc` plus the `source ~/.svc.rc` line in your shell rc.

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
