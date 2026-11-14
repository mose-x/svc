# Scripts / 打包脚本

> Bilingual documentation (English / 中文) for local packaging and building.
> 双语文档(English / 中文),指导本地打包构建。

---

## Overview / 概览

These scripts build and package svc for each platform. They are shared between CI (`build.yml`) and local builds — the same script produces identical artifacts in both environments.

这些脚本为各平台构建和打包 svc。CI(`build.yml`)和本地构建共用同一套脚本——同一脚本在两边产出逐字节一致的产物。

| Script / 脚本 | Platform / 平台 | Output / 产物 |
|---|---|---|
| `build-windows.sh` | Windows (amd64/arm64) | bare `.exe` + NSIS installer `.exe` |
| `build-macos.sh` | macOS (amd64/arm64) | `.dmg` + `.bin` (self-update) |
| `build-macos-local.sh` | macOS (local MDM build) | same as above, with `-skipbindings` |
| `build-linux.sh` | Linux (amd64/arm64) | bare binary + `.deb` + `.rpm` |
| `run-first-installed-local-mac.sh` | macOS (post-install fix) | fixes ad-hoc signing + quarantine |

---

## Prerequisites / 前置条件

### All platforms / 所有平台

- **Go 1.25+** — `go version`
- **Node.js 18+** — `node --version`
- **Wails CLI** — `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- **jq** — for version bumping in `about.json`

### Windows

- **NSIS** — for the installer (`makensis`). Download from https://nsis.sourceforge.io. Add to PATH.
- **go-winres** — `go install github.com/tc-hib/go-winres@latest` (for Windows icon/manifest resources)

### macOS

- **create-dmg** — `brew install create-dmg` (the script auto-installs if missing)
- **Pillow** — `pip3 install Pillow` (for DMG background image; the script auto-installs if missing)
- **Xcode Command Line Tools** — for `codesign`, `xattr`, `sips`, `iconutil`

### Linux

- **GTK + WebKit2GTK** — `sudo apt-get install libgtk-3-dev libwebkit2gtk-4.0-dev libayatana-appindicator3-dev librsvg2-dev`
- **fpm** — `sudo gem install fpm` (for .deb/.rpm packaging; the script auto-installs if missing)
- **Pillow** — `sudo apt-get install python3-pil` (for icon resizing)

---

## Usage / 用法

### Windows / Windows 打包

```bash
# From Git Bash on Windows (or CI windows-latest):
./scripts/build-windows.sh <version> [arch]
# Example:
./scripts/build-windows.sh 1.1.0 amd64
```

**Produces / 产出:**
- `build/bin/svc-<version>-windows-x64.exe` — bare binary (for self-update)
- `build/bin/svc-<version>-windows-x64-installer.exe` — NSIS installer (for first-time install)

**Notes / 注意:**
- `arch` defaults to `$(go env GOARCH)` (usually `amd64`).
- Cross-compile arm64 on an amd64 machine: `./scripts/build-windows.sh 1.1.0 arm64` (CGO-free, no extra toolchain needed).
- The script builds the `svc-shim` console binary, generates Windows resources (icon/manifest via `go-winres`), builds the app + NSIS installer, and normalizes asset names.
- 脚本会构建 `svc-shim` 控制台二进制、生成 Windows 资源(图标/清单)、构建应用 + NSIS 安装器,并标准化产物名。

### macOS / macOS 打包

```bash
# Standard build (regenerates Wails bindings — for CI or non-MDM Macs):
./scripts/build-macos.sh <version> [arch]
# Example:
./scripts/build-macos.sh 1.1.0 arm64

# Local MDM Mac build (skips binding generation — see below):
./scripts/build-macos-local.sh <version> [arch]
```

**Produces / 产出:**
- `build/bin/svc-<version>-macos-<arch>.dmg` — DMG installer
- `build/bin/svc-<version>-macos-<arch>.bin` — bare inner binary (for self-update)

**Notes / 注意:**
- `arch` defaults to `$(go env GOARCH)`.
- The script strips ad-hoc signatures from both the `.app` bundle and the inner binary (`codesign --remove-signature`). This is required for the app to launch under MDM (ad-hoc + quarantine = "damaged", no right-click bypass).
- 脚本会扒掉 `.app` bundle 和内层二进制的 ad-hoc 签名。这在 MDM 下是必须的(ad-hoc + quarantine = "已损坏",无右键旁路)。
- The DMG background image shows "Right-click -> Open" in red (English) as the launch instruction.
- DMG 背景图以红色英文显示 "Right-click -> Open" 作为启动指引。

#### `build-macos-local.sh` vs `build-macos.sh` / 两个 macOS 脚本的区别

| | `build-macos.sh` (CI) | `build-macos-local.sh` (local MDM) |
|---|---|---|
| Wails bindings | Regenerated each build (catches drift) | Skipped (`-skipbindings`) |
| Why / 原因 | CI runner has no MDM, binding-gen runs fine | MDM Mac: binding-gen runs an ad-hoc binary -> amfid kills it |

**Caveat / 注意:** `build-macos-local.sh` relies on the committed `frontend/wailsjs/` bindings being current. If you change Go `App` methods exposed to the frontend, run a non-skip `wails build` (on a non-MDM machine or CI) to regenerate + commit the bindings first.
**注意:** `build-macos-local.sh` 依赖仓库中已提交的 `frontend/wailsjs/` bindings 是最新的。如果改了暴露给前端的 Go `App` 方法,先在非 MDM 环境(或 CI)跑一次不带 `-skipbindings` 的 `wails build` 重新生成 + 提交 bindings。

### Linux / Linux 打包

```bash
./scripts/build-linux.sh <version> [arch]
# Example:
./scripts/build-linux.sh 1.1.0 amd64
```

**Produces / 产出:**
- `build/bin/svc-<version>-linux-x64` — bare binary (for self-update)
- `build/bin/svc-<version>-linux-x64.deb` — Debian package
- `build/bin/svc-<version>-linux-x64.rpm` — RPM package

**Notes / 注意:**
- `arch` defaults to `$(go env GOARCH)`.
- Linux ARM64 cannot be cross-compiled (Wails links webkit2gtk via CGO). Run on a native arm64 machine.
- Linux ARM64 无法交叉编译(Wails 通过 CGO 链接 webkit2gtk)。需在原生 arm64 机器上运行。
- The script auto-installs system dependencies (idempotent) + `fpm` if missing.
- 脚本会自动安装系统依赖(幂等)+ `fpm`(如果没装)。

---

## macOS first-launch fix / macOS 首次启动修复

### When to use / 什么时候用

After installing from a **locally-built** DMG on macOS (especially under MDM), the app may be blocked ("damaged" or SIGKILL 137). This happens because:
1. Wails/Go ad-hoc signs the `.app` bundle + inner binary.
2. macOS adds `com.apple.quarantine` on download.
3. Ad-hoc + quarantine = "damaged", no right-click bypass (worse than truly unsigned).

从**本地构建**的 DMG 安装到 macOS 后(尤其 MDM 下),应用可能被拦("已损坏"或 SIGKILL 137)。原因:
1. Wails/Go 给 `.app` bundle + 内层二进制打了 ad-hoc 签名。
2. macOS 下载时加了 `com.apple.quarantine`。
3. Ad-hoc + quarantine = "已损坏",无右键旁路(比完全未签名更糟)。

The build scripts already strip ad-hoc signatures (`codesign --remove-signature`). But the browser download re-adds quarantine. The `run-first-installed-local-mac.sh` script strips + re-stamps a clean quarantine so the app launches via Gatekeeper's right-click -> Open bypass.

构建脚本已经扒掉了 ad-hoc 签名。但浏览器下载会重新加 quarantine。`run-first-installed-local-mac.sh` 脚本清除 + 重新打一个干净的 quarantine,让应用走 Gatekeeper 的右键 -> 打开旁路。

### Usage / 用法

```bash
# After dragging the .app to /Applications from the DMG:
chmod +x scripts/run-first-installed-local-mac.sh
./scripts/run-first-installed-local-mac.sh
```

- Only run once on first install. After that, double-click the `.app` directly.
- 只需首次安装运行一次。之后直接双击 `.app` 即可。
- If installing from a CI release DMG downloaded via browser, this script is usually not needed (the browser adds correct quarantine automatically).
- 如果从 CI release 浏览器下载的 DMG 安装,通常不需要此脚本(浏览器会自动加正确的 quarantine)。

---

## CI vs Local / CI 与本地构建的区别

| | CI (`build.yml`) | Local (these scripts) |
|---|---|---|
| Runner | GitHub-hosted (Win/macOS/Linux) | Your machine |
| Proxy | Not needed (runners are outside GFW) | Required on Windows behind GFW (`HTTPS_PROXY=http://127.0.0.1:7890`) |
| Script | Same (`scripts/build-*.sh`) | Same |
| Output | Identical artifacts | Identical artifacts |

CI 和本地构建用**同一套脚本**,产物逐字节一致。唯一区别:CI runner 不需要代理(在 GFW 外),本地 Windows 需要 Clash 代理。

---

## Troubleshooting / 常见问题

### `wails: command not found`
Install the Wails CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
安装 Wails CLI。

### `makensis not found` (Windows)
Install NSIS from https://nsis.sourceforge.io and add to PATH.
安装 NSIS 并加到 PATH。

### `create-dmg: command not found` (macOS)
The script auto-installs it via `brew install create-dmg`. If that fails, run it manually.
脚本会通过 `brew install create-dmg` 自动安装。如果失败,手动执行。

### Python-related hook failures (Windows)
The code-hooks `commit-msg` hook uses `python3 -c`. SVC's Python shim only provides `python` (Windows CPython has no `python3.exe`). SVC now creates the `python3` alias itself (PR #69/#71). If the hook still fails, compile a `python3.exe` wrapper (see `note/` or AGENTS.md known issues).
code-hooks 的 `commit-msg` 钩子用 `python3 -c`。SVC 的 Python shim 只有 `python`(Windows CPython 没有 `python3.exe`)。SVC 现在自己创建 `python3` alias(PR #69/#71)。如果钩子还失败,编译一个 `python3.exe` wrapper(见 `note/` 或 AGENTS.md)。

### `cargo build` fails after installing Rust (sysroot issue)
Rust's sysroot (`lib/rustlib`) is in a sibling directory (`rust-std-{target}/`), not under `cargo/` or `rustc/`. SVC's `MergeComponents` merges it automatically, per `{target}/lib` — the `rustc` component ships its own `lib/rustlib/` (holding `etc/` and `{target}/bin/`), which must not be mistaken for an already-merged std. The layout is re-checked at every startup and after every version switch, so an install left broken by an older build repairs itself.
Rust 的 sysroot(`lib/rustlib`)在兄弟目录(`rust-std-{target}/`),不在 `cargo/` 或 `rustc/` 下。SVC 的 `MergeComponents` 会自动合并,粒度是 `{target}/lib`——`rustc` 组件自带 `lib/rustlib/`(里面是 `etc/` 和 `{target}/bin/`),不能当成 std 已合并。每次启动和每次切换版本后都会重新检查布局,旧版本装坏的会自己修好。

### `rustfmt` / `clippy-driver` fail to start (missing `librustc_driver`)
The Rust tarball keeps one directory per component, so the tools outside `rustc/` cannot resolve rustc's shared runtime. SVC bridges it during install (`LinkComponentRuntime`: symlinks into `<component>/lib` on Unix, hardlinks of `rustc/bin/*.dll` on Windows), and re-checks the layout at every startup and after every version switch — versions installed before the fix are repaired instead of needing a reinstall.
Rust 压缩包按组件分目录,`rustc/` 之外的工具找不到 rustc 的共享运行时。SVC 在安装时就会桥接(`LinkComponentRuntime`:Unix 软链到 `<component>/lib`,Windows 把 `rustc/bin/*.dll` 硬链过去),并在每次启动和每次切换版本后重新检查布局——修复前安装的版本会自动修好,无需重装。

### macOS app "damaged" after install
See the [macOS first-launch fix](#macos-first-launch-fix--macos-首次启动修复) section above.
见上面的 [macOS 首次启动修复](#macos-first-launch-fix--macos-首次启动修复) 章节。

### Git push fails (connection reset)
You're behind the GFW without a proxy. Set `HTTPS_PROXY=http://127.0.0.1:7890` for all git/gh commands.
你在 GFW 后没开代理。所有 git/gh 命令加 `HTTPS_PROXY=http://127.0.0.1:7890`。
