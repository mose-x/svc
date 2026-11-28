# Command reference

Every command the tree ships, with what it prints or changes. Aliases are in
parentheses. Arguments, flags and exit behaviour of any one of them:
`svc <command> --help`.

Global flags work on every command: `--json`, `-q/--quiet`, `-y/--yes`,
`--no-color`, `--no-wait`, `--lang zh|en`, `--install-path <dir>`.

Mutating commands are marked **writes**. They take a cross-process lock on the
SVC directory and prompt unless `--yes` is given.

## Inspect

| Command | What it prints |
|---|---|
| `svc list` (`ls`) | Every SDK: active version, installed version count, PATH state, conflicts |
| `svc status <sdk>` | One SDK in full: active version, every installed version, install directory, what the copy on PATH reports itself as, system conflicts |
| `svc current [<sdk>]` | The active version, bare, so `$(svc current nodejs)` works. With no argument, every SDK's. An SDK with no active version prints nothing and still exits 0 |
| `svc dir <sdk>` | The install directory |
| `svc url <sdk> <version>` | The download URL, with the configured proxy and GitHub mirror already applied |
| `svc path` | Every PATH entry that looks like an SDK, with what `svc` identified it as and whether the shims win |
| `svc storage` | Disk usage of every SDK directory, version counts, which version is active |
| `svc conflicts <sdk>` | System PATH and environment entries that compete with the shims |
| `svc versions <sdk>` | The installable versions, marking the newest, the installed, the active and the LTS ones. `--lts`, `--limit N`, `--offline`, `--refresh` |
| `svc about` | What this build says about itself: version, Go version, platform, commit |
| `svc doctor` | Every self-check, repairing what a startup can repair. Exits 1 when something is broken |

## Install and switch

| Command | What it does |
|---|---|
| `svc install <sdk> <version>` | **writes** — download, verify and activate. Activation is not optional |
| `svc switch <sdk> <version>` (`use`) | **writes** — point the shims and environment at an installed version. Reopen the terminal afterwards |
| `svc reinstall <sdk> <version>` | **writes** — re-download an installed version over itself. The version must already be installed |
| `svc uninstall <sdk> <version>` (`rm`) | **writes** — delete one version's directory. Uninstalling the active version is allowed, warns, and exits 0 |
| `svc import <sdk> [<path>]` | **writes** — copy an archive or a directory into the store, detect its version, activate it |
| `svc import <sdk> --from-path` | **writes** — import the copy the SDK's command currently resolves to on the system PATH |

`svc install nodejs`, `svc reinstall nodejs` and `svc switch nodejs` are refused
with exit 2 when nvm, nvm-rust or nvm-windows owns the machine. `svc uninstall
nodejs` and `svc clean inactive nodejs` are not.

## Package managers and npm

| Command | What it does |
|---|---|
| `svc pkg` | The group: manage an SDK's package managers and the npm-scoped operations |
| `svc pkg list <sdk>` | The package managers of one SDK and their versions |
| `svc pkg install <name>` | **writes** — install a package manager |
| `svc pkg update <name>` | **writes** — update a package manager to its latest version |
| `svc pkg registry` | The npm registry of the active Node.js |
| `svc pkg registry set <url\|official\|china>` | **writes** — switch it |
| `svc pkg global` | The npm global-package subgroup |
| `svc pkg global list` | The globally installed npm packages |
| `svc pkg global install <package>` | **writes** — install a package globally into the active Node.js |
| `svc pkg global update <package>` | **writes** — update a global package to its latest version |
| `svc pkg global uninstall <package>` | **writes** — remove a globally installed npm package |

The group is named `pkg` rather than `npm`: a subcommand may not share a name
with a binary an SDK installs, and every Node.js ships `npm`.

## Clean and logs

| Command | What it does |
|---|---|
| `svc clean` | Lists its targets and deletes nothing |
| `svc clean tmp` | **writes** — empty the download and extraction cache |
| `svc clean logs` | **writes** — delete every log file and reopen today's empty |
| `svc clean inactive <sdk>` | **writes** — delete every version of an SDK except the active one |
| `svc logs` | The log group |
| `svc logs list` | svc's own log files |
| `svc logs show <file>` | Print one of them |
| `svc logs dir` | The log directory |
| `svc logs rm <file>...` | **writes** — delete log files |

The log commands take no lock: they touch the log directory only, and a file
deleted under a running process is simply recreated.

## Settings and endpoints

| Command | What it does |
|---|---|
| `svc settings` (`config`) | The settings group |
| `svc settings show` | The effective settings, with the GitHub token masked |
| `svc settings set <key> <value>` | **writes** — change one setting: `theme`, `language`, `downloadThreads`, `githubMirror`, `proxy.enabled`, `proxy.mode`, `proxy.url`, `proxy.protocol`, `installPath` |
| `svc settings token` | **writes** — store the GitHub token, read from stdin so it never reaches the shell history. `--clear` removes it |
| `svc settings check-proxy [url...]` | Test whether the configured proxy can reach a site |
| `svc endpoints` | The endpoint group |
| `svc endpoints list` | Every SDK's default and effective download endpoint |
| `svc endpoints set <sdk> <url>` | **writes** — override one SDK's download endpoint |
| `svc endpoints unset <sdk>` | **writes** — restore that SDK's default endpoint |

`svc settings set installPath` does not edit a field: it migrates the whole SDK
tree, backs the old one up to the desktop and re-runs the shim setup.

## `svc` itself

| Command | What it does |
|---|---|
| `svc self` | The group: inspect the `svc` binary itself |
| `svc self check` | Whether a newer release is published |
| `svc self update` | **writes** — download it, verify its checksum, hand the running binary to a swap script and exit 0 |
| `svc self rollback` | **writes** — restore the `<exe>.bak` the previous update left behind |

Nothing is relaunched. The new version is there the next time `svc` runs.

## Shell and legacy

| Command | What it does |
|---|---|
| `svc completion` | The completion group |
| `svc completion bash` | Generate the bash completion script |
| `svc completion zsh` | Generate the zsh completion script |
| `svc completion fish` | Generate the fish completion script |
| `svc completion powershell` | Generate the PowerShell completion script |
| `svc help [command]` | Print the help of the tree or of one command |
| `svc version` (`-v`, `--version`) | The single version line. Parsed by scripts, so its shape is frozen |
| `svc init` (`svc setup`) | **writes** — one-time shim setup: create `~/.svc/shims` and add the `.svc.rc` source line |
