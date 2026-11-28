---
name: svc
description: Install, switch, inspect and remove SDK versions (Node.js, Go, JDK, Python, Rust and eleven more) with the svc CLI. Use when a task needs a specific SDK version present or active on this machine, or needs svc's own settings, download endpoints or logs. Not for project dependency managers such as npm, pip or go mod, and not for the svc GUI.
---

# svc

One binary, two shells: bare `svc` launches the desktop application, `svc <command>` runs headless and exits. Both share one store (`~/.svc`, `%USERPROFILE%\.svc` on Windows), so a version installed from a terminal shows up in the window, and the version the window activated is what `svc current` reports.

## Check the build first

```bash
svc version
```

The command tree only exists from **v3.0.0**. An older build knows just `init`, `setup`, `version` and `help`; for anything else it **opens a window and blocks forever**. If `svc version` prints below 3.0.0, stop and tell the user to update rather than running a command that will hang the session.

## Hard rules

- **Never run bare `svc`.** It launches the GUI and does not return.
- **Pass `--yes` to every mutating command.** There is no interactive fallback: a command that would prompt refuses on a pipe (exit 2), and no command ever opens a dialog. A path is always an argument.
- **stdout is results and nothing else.** Progress, warnings, lock notices and hints go to stderr, so `svc current nodejs`, `svc dir nodejs` and `svc list --json` are safe to capture.
- **Parse with `--json`, never by splitting text.** It emits exactly one JSON document on stdout; progress becomes one JSON object per line on stderr.
- **Node.js may be refused.** When nvm, nvm-rust or nvm-windows owns the machine, `svc install nodejs`, `svc reinstall nodejs` and `svc switch nodejs` exit **2** with a "managed by nvm" message. That is deliberate — two managers writing the same `node` shim is a fight the user cannot see. Use nvm for Node.js instead; `svc uninstall nodejs` still works.
- **Never expect a restart.** `svc self update` hands the running binary to a swap script and exits 0; the new version is there the next time `svc` runs.
- **Leave `--install-path` alone** unless the user asks to operate on another SVC home. It applies to that run only and is never saved.
- **A switch does not reach an already-open shell.** The shims change, but a shell that cached its PATH keeps resolving the old binary. Tell the user to reopen the terminal, and do not judge the switch by what the current shell still finds.

## Exit codes

Stable by contract, so key on the number rather than on a localized sentence.

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unclassified failure |
| 2 | Usage error — missing argument, unknown command or flag, an operation the SDK layout forbids (including the Node.js refusal above) |
| 3 | Not found — unknown SDK, missing path, missing version directory, no backup to roll back to |
| 4 | Integrity — checksum mismatch, incomplete SDK tree, version mismatch |
| 5 | Cancelled — SIGINT, or a declined confirmation prompt |
| 6 | Network — HTTP status, proxy policy, disallowed scheme |
| 7 | Busy — another `svc` process holds the operation lock and `--no-wait` was given |

## Everyday commands

```bash
svc list                                   # every SDK and its local state
svc status nodejs                          # one SDK in full
svc current nodejs                         # the active version, bare
svc dir nodejs                             # the install directory
svc versions nodejs --lts --limit 10       # what can be installed
svc install nodejs 22.11.0 --yes           # download, verify, activate
svc switch nodejs 20.18.0 --yes            # point the shims at an installed version
svc uninstall nodejs 20.18.0 --yes         # delete one version's directory
svc import nodejs ./node-v22.11.0.tar.xz --yes   # adopt a local archive or directory
svc doctor                                 # self-check, repair what it can
svc list --json                            # the same table as one JSON document
```

`<sdk>` is the identifier `svc list` prints in its first column — `nodejs`, `go`, `jdk`, `python`, `rust`, `ruby`, `dotnet`, `php`, `perl`, `maven`, `gradle`, `flutter`, `android`, `dart`. Matching is case-insensitive and the display name works too.

Installing also activates: `svc install` is not a download-only command, and there is no flag to make it one.

## Where to look next

- Every command with its one-line purpose: [references/commands.md](references/commands.md)
- Arguments, flags and behaviour of one command: `svc <command> --help`
- Concurrency: mutating commands take a cross-process lock and wait by default; `--no-wait` turns waiting into exit 7.
