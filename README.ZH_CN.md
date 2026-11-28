# svc

一个跨平台桌面应用，用于统一管理多种 SDK（Node.js、JDK、Go、Python、Rust 等）的版本安装、切换、环境变量配置，以及带完整性校验的应用内自更新。

[English Documentation](README.md)

## 功能特性

### SDK 管理
- **支持 14 种 SDK**：Node.js、JDK、Go、Python、Rust、Ruby、.NET、PHP、Perl、Maven、Gradle、Flutter、Android、Dart
- **一键安装**：从官方源获取可用版本列表，一键下载安装
- **版本切换**：已安装版本可快速切换，无需重新下载
- **重新安装**：已安装版本支持覆盖安装
- **导入配置**：支持从本地压缩包、文件夹导入，或一键导入 PATH 中检测到的 SDK

### Shim 机制
- 为多 bin 目录的 SDK 自动生成 shim（如 Rust 的 `cargo/bin` + `rustc/bin`）
- 透明路由命令到当前激活版本，不污染 PATH
- 跨平台 shim 执行器（Unix shell 脚本 + Windows 批处理）

### 包管理器
- 自动检测并安装各 SDK 对应的包管理器（npm、yarn、pnpm、pip、gem、cargo 等）
- 支持包管理器版本更新

### 环境配置
- **自动 PATH 管理**：安装/切换版本后自动配置系统环境变量
- **自定义安装位置**：支持自定义 SDK 存储目录，迁移时自动更新 PATH 并备份到桌面
- **PATH 信息查看**：可视化查看当前 PATH 中的 SDK 相关条目，检测系统级环境变量冲突
- **Shell RC 编辑**：自动编辑 `.bashrc`/`.zshrc`/`.profile`/PowerShell profile 让 shim 生效

### 系统设置
- **主题切换**：暗色/亮色/跟随系统，窗口标题栏同步变色
- **多语言**：中文 / English
- **代理配置**：支持系统代理或自定义代理，可测试连通性（百度 / Google）
- **GitHub 镜像**：通过可配置镜像加速 GitHub 下载（如 `https://ghfast.top`）
- **下载线程**：多线程分段下载，服务器不支持 range 时自动回退单线程
- **Endpoint 自定义**：各 SDK 下载源可配置自定义端点
- **缓存清理**：一键清理临时下载缓存和未激活的 SDK 版本
- **日志管理**：应用内查看、搜索、清理日志

### 应用内自更新（v1.0.0 起）
- **GitHub Releases**：检测、下载、应用更新全程在应用内完成，无需手动重装
- **SHA256 校验**：下载的二进制在替换前会与发布的校验和比对；损坏或被篡改的产物会被拒绝并删除
- **一次性回滚**：每次升级会备份上一版本到 `<exe>.bak`；应用内「回滚」按钮可恢复到紧邻的上一版本
- **跨平台替换**：Unix 用 shell 脚本（`pgrep` 等待 → 备份 → 替换 → 重启）；Windows 用批处理脚本（`tasklist` 等待 → 备份 → 替换 → 重启）处理运行中二进制锁
- **产物命名**：`svc-{version}-{os}-{arch}.{ext}`（amd64 → x64），每个 release 附带独立的 `sha256sums.txt`

### 命令行界面（v3.0.0 起）
- **一个二进制，两个外壳**：不带参数运行 `svc` 打开窗口，`svc <命令>` 执行命令后退出；两者调用同一套服务、读写同一批文件
- **功能全覆盖**：窗口里的每个操作都有对应命令，从 `svc install` 到 `svc self update`
- **可脚本化**：`--json` 让 stdout 只输出一个机器可读文档、进度转到 stderr，`| jq` 和 `$( )` 都能直接用
- **不出意外**：任何命令都不会弹窗、不会打开文件选择框，也不会重新拉起应用
- **稳定的退出码**：0–7，与 GUI 翻译的是同一套错误码

完整参考见[命令行](#命令行)。

### 用户体验
- **下载进度**：实时显示下载速度、进度和剩余大小
- **复制下载链接**：支持复制 SDK 下载 URL
- **二次确认**：敏感操作（切换版本、重装、迁移目录、回滚等）均需确认弹窗
- **状态指示**：侧栏彩色圆点标识各 SDK 配置状态（绿色=已配置，黄色=仅PATH，红色=未配置）

### 使用案例
#### 主页
![主页](./image/home_cn.png)
#### Node.js
![Node.js](./image/nodejs_cn.png)
#### Jdk
![Jdk](./image/jdk_cn.png)
#### 一键导入安装
![download](./image/download_cn.png)
#### 非SVC管理提示
![svc](./image/sys_path_check_cn.png)
#### 设置
![setting](./image/setting_cn.png)

## 命令行

`svc` 是一个二进制、两个外壳。不带参数运行会启动图形界面；带命令运行则像普通 CLI 一样执行完就退出。两个外壳调用同一套服务、读写同一批文件，所以在终端装的版本会出现在窗口里，窗口里激活的版本也正是 `svc current` 报出来的那一个。命令树自 v3.0.0 起提供：更早的版本只认 `init`、`setup`、`version` 与 `help`，其它参数一律打开窗口。

```bash
svc list                          # 每个 SDK 及其本地状态
svc versions nodejs --lts         # 可安装的版本，从新到旧
svc install nodejs 22.11.0        # 下载、校验、激活
svc switch nodejs 20.18.0         # 把 shim 指向已安装的版本
svc current nodejs                # 只输出激活版本
svc doctor                        # 自检 svc 自身的安装，能修的就修
svc self update                   # 更新 svc 自己
```

所有命令都满足两条保证：

- **不弹窗、不打开文件选择框。** 需要路径的命令用参数给（`svc import nodejs ./node-v22.11.0.tar.xz`），需要确认的命令用 `--yes`。
- **不重新拉起。** `svc self update` 与 `svc self rollback` 把二进制交给替换脚本后以 0 退出；下次运行 `svc` 时新版本已经就位。

`svc <命令> --help` 会打印该命令的完整说明、参数与退出码。

### 兼容的旧命令

下面这些早于 CLI 存在，行为一直保持原样。其中 `svc version` 尤其如此：有脚本在解析它，所以那一行输出是冻结的。变过的只有 `svc help`——现在由命令树接管，打印的是真实命令树，而不是过去那份写死的四行列表。

| 调用方式 | 行为 |
|---|---|
| `svc` | 启动图形界面 |
| `svc init`、`svc setup` | 一次性 shim 初始化：创建 `~/.svc/shims` 并写入 `.svc.rc` 的 source 行 |
| `svc version`、`svc -v`、`svc --version` | 打印单行版本号 |
| `svc help`、`svc -h`、`svc --help` | 打印命令树 |

### 全局参数

| 参数 | 作用 |
|---|---|
| `-h`、`--help` | 打印你所询问的那个命令的帮助 |
| `--json` | stdout 输出机器可读 JSON；进度与提示转到 stderr |
| `-q`、`--quiet` | 只输出结果与错误——没有进度，没有提示 |
| `-y`、`--yes` | 对所有确认提示回答「是」 |
| `--no-color` | 绝不输出 ANSI 颜色序列 |
| `--no-wait` | 有其他 svc 操作持锁时直接以退出码 7 失败，而不是等待 |
| `--lang zh\|en` | 消息语言。默认取 `$SVC_LANG`，其次取 `settings.json` 里的 `language` |
| `--install-path <dir>` | 仅本次运行使用另一个 SVC home；不会写盘 |

### 输出

输出模式由环境决定，而不是由参数决定：

| 场景 | 输出 |
|---|---|
| 交互式终端 | 彩色进度条、表格、确认提示 |
| 管道或 CI（stdout 不是 tty） | 每个阶段一行纯文本，按整十百分比打点；无颜色；本该询问的命令直接报错并要求加 `--yes` |
| `--json` | 结果以 JSON 走 stdout；进度以每行一个 JSON 对象走 stderr |

**stdout 只放结果。** 进度、警告、等锁提示、补充说明全部走 stderr——正因如此 `svc list | grep`、`NODE_DIR=$(svc dir nodejs)/$(svc current nodejs)` 和 `svc install --json ... | jq` 才是安全的。

### 退出码

退出码是契约，保持稳定：脚本应当依赖数字，而不是去解析一句会被本地化的文案。

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 未分类失败 |
| 2 | 用法错误——参数缺失、未知命令或参数、SDK 布局不允许的操作 |
| 3 | 找不到——未知 SDK、路径不存在、版本目录缺失、无备份可回滚、该平台的 release 产物不存在 |
| 4 | 完整性——校验和不匹配、SDK 目录不完整、版本不匹配 |
| 5 | 已取消——SIGINT，或确认提示被拒绝 |
| 6 | 网络——HTTP 状态码、代理策略、不允许的协议 |
| 7 | 忙——有其他 svc 进程持有操作锁，且传了 `--no-wait` |

### 命令

括号内是别名。

**查看**

| 命令 | 输出内容 |
|---|---|
| `svc list`（`ls`） | 每个 SDK：激活版本、已装版本数、PATH 状态、冲突 |
| `svc status <sdk>` | 单个 SDK 的全部信息：激活版本、所有已装版本、安装目录、PATH 上那份自报的版本、系统冲突 |
| `svc current [<sdk>]` | 只输出激活版本，因此 `$(svc current nodejs)` 可直接内联使用。不带参数则列出所有 SDK。没有激活版本时输出为空且仍退出 0 |
| `svc dir <sdk>` | 安装目录 |
| `svc url <sdk> <version>` | 下载 URL，已套用配置好的代理与 GitHub 镜像 |
| `svc conflicts <sdk>` | 与 shim 竞争的系统 PATH 与环境变量条目。无论是否发现冲突都退出 0 |
| `svc path` | 所有看起来像 SDK 的 PATH 条目、svc 把它识别成了什么、以及是否已由 svc 接管 |
| `svc storage` | 每个 SDK 的磁盘占用、版本数、激活版本，以及残留的下载缓存 |
| `svc about` | svc 自身的版本、构建所用的 Go 版本、协议与仓库地址 |

**安装与切换**

| 命令 | 作用 |
|---|---|
| `svc versions <sdk>` | 可安装的版本，标注最新、已装、当前激活与 LTS。支持 `--lts`、`--limit N`、`--offline`、`--refresh` |
| `svc install <sdk> <version>` | 下载、校验并激活。激活不是可选项——安装同时会把 shim 指向新版本 |
| `svc switch <sdk> <version>`（`use`） | 把 shim 与环境变量指向已安装的版本。之后需要重开终端 |
| `svc reinstall <sdk> <version>` | 重新下载已安装的版本并覆盖其目录。该版本必须已经安装 |
| `svc uninstall <sdk> <version>`（`rm`） | 删除某个版本的目录。允许卸载当前激活版本，会给出警告并退出 0 |
| `svc import <sdk> [<path>]` | 把压缩包或目录拷进 SVC 存储，检测其版本并激活 |
| `svc import <sdk> --from-path` | 导入该 SDK 命令当前在系统 PATH 上解析到的那一份 |
| `svc doctor` | 跑完所有自检，修好一次启动能修的，其余如实报告。有异常时退出 1 |

**包管理器与 npm**

| 命令 | 作用 |
|---|---|
| `svc pkg list <sdk>` | 某个 SDK 的包管理器及其版本 |
| `svc pkg install <name>` | 安装一个包管理器 |
| `svc pkg update <name>` | 把某个包管理器更新到最新版本 |
| `svc pkg registry` | 当前激活 Node.js 的 npm 源 |
| `svc pkg registry set <url\|official\|china>` | 切换 npm 源 |
| `svc pkg global list` | 全局安装的 npm 包 |
| `svc pkg global install <package>` | 在当前激活的 Node.js 里全局安装一个包 |
| `svc pkg global update <package>` | 把某个全局包更新到最新版本 |
| `svc pkg global uninstall <package>` | 移除一个全局安装的 npm 包 |

这个命令组叫 `pkg` 而不是 `npm`：子命令名不得与任何 SDK 装出来的二进制同名，而每个 Node.js 都自带 `npm`。

**清理与日志**

| 命令 | 作用 |
|---|---|
| `svc clean tmp` | 清空下载与解压缓存 |
| `svc clean logs` | 删除所有 svc 日志文件，并重新打开一个空的当日日志 |
| `svc clean inactive <sdk>` | 删除某个 SDK 除激活版本外的所有版本 |
| `svc logs list` | 列出 svc 自己的日志文件 |
| `svc logs show <file>` | 打印其中一个 |
| `svc logs dir` | 日志目录 |
| `svc logs rm <file>...` | 删除日志文件 |

单独运行 `svc clean` 只会列出可选目标，什么都不删。

**设置与下载端点**

| 命令 | 作用 |
|---|---|
| `svc settings show` | 生效中的设置，GitHub token 以掩码显示 |
| `svc settings set <key> <value>` | 修改一项设置：`theme`、`language`、`downloadThreads`、`githubMirror`、`proxy.enabled`、`proxy.mode`、`proxy.url`、`proxy.protocol`、`installPath` |
| `svc settings token` | 保存 GitHub token，从 stdin 读取，因此不会进入 shell 历史。`--clear` 用于清除 |
| `svc settings check-proxy [url...]` | 测试配置好的代理能否访问某个站点 |
| `svc endpoints list` | 每个 SDK 的默认与生效下载端点 |
| `svc endpoints set <sdk> <url>` | 覆盖某个 SDK 的下载端点 |
| `svc endpoints unset <sdk>` | 恢复该 SDK 的默认端点 |

`svc settings` 也可写作 `svc config`。`settings set installPath` 不是改一个字段：它会迁移整棵 SDK 目录树、把旧目录备份到桌面，并在新位置重跑 shim 初始化。

**svc 自身**

| 命令 | 作用 |
|---|---|
| `svc self check` | 是否已发布更新的 svc 版本 |
| `svc self update` | 下载新版本、校验其校验和，并把运行中的二进制交给替换脚本 |
| `svc self rollback` | 恢复上次升级留下的 `<exe>.bak` |

### 并发

所有会改动 svc 所管内容的命令——`install`、`reinstall`、`switch`、`uninstall`、`import`、`clean tmp`、`clean inactive`、`pkg` 下的各类变更、`settings set`、`settings token`、`endpoints set`、`endpoints unset`、`self update`、`self rollback`——在动 `config.json`、`shims.json` 或 shell rc 之前，都会对 SVC 目录取一把跨进程锁。因此终端与开着的窗口之间是排队执行，而不是互相竞争。默认行为是等待，并在 stderr 上说明；加 `--no-wait` 则直接以退出码 7 失败。

日志类命令是例外，不取锁：它们只动日志目录，每个 svc 进程都独立往里写，而在一个正在运行的进程脚下被删掉的日志文件会被重新创建。

### Shell 补全

```bash
svc completion bash        # 装进你的 bash 补全目录
svc completion zsh         # 作为 _svc 装进 $fpath 上的某个目录
svc completion fish        # 装进 ~/.config/fish/completions
svc completion powershell  # 装进你的 PowerShell profile
```

候选项来自命令树、已注册的 SDK 标识符和已安装的版本。补全参数时绝不联网，所以按 Tab 不会把 shell 卡住。

### Agent 技能

`skills/svc` 是一份 [Codex 技能](https://github.com/openai/skills)：AI agent 在调用这套 CLI 之前先读的说明。它写的是单看 `--help` 容易搞错的那几条——绝不裸跑 `svc`、任何写操作都带 `--yes`、解析用 `--json` 而不是表格、每个退出码的含义——完整的命令表在 `skills/svc/references/commands.md`。`skill_test.go` 会拿真实命令树去比对这两个文件，所以这份技能不会像手抄的清单那样悄悄过期。

```bash
cp -R skills/svc ~/.codex/skills/svc     # 或者让 Codex 的 skill-installer 去仓库里拉
```

它只是文档：没有任何二进制会读它，`svc <command> --help` 始终是权威。

## 与同类工具的对比优势

| 对比项 | SVC（本项目） | nvm / sdkman / pyenv 等 | VS Code 插件 |
|---|:---:|:---:|:---:|
| SDK 统一管理（14 种） | ✅ | ❌ 单一 SDK | ❌ 分散 |
| 图形化界面 | ✅ | ❌ 命令行 | ✅ |
| 覆盖相同操作的命令行 | ✅ | ✅ | ❌ |
| 自动配置 PATH | ✅ | ⚠️ 需配置 shell | ❌ |
| 跨平台桌面应用 | ✅ | ✅ | ✅ |
| 多线程下载 | ✅ | ❌ | – |
| 自定义下载源 | ✅ | ⚠️ 部分支持 | – |
| GitHub 镜像加速 | ✅ | ❌ | – |
| 包管理器配套 | ✅ | ⚠️ 部分支持 | – |
| PATH 可视化 | ✅ | ❌ | ❌ |
| 一键导入 SDK（压缩包/文件夹/PATH） | ✅ | ❌ | ❌ |
| 多 bin 目录 SDK 的 Shim 机制 | ✅ | ❌ | ❌ |
| 安装位置迁移+自动备份 | ✅ | ❌ | ❌ |
| 系统级生效（所有 IDE/终端可用） | ✅ | ⚠️ 仅当前 shell | ❌ 仅 VS Code |
| 冲突检测与清理 | ✅ | ❌ | ❌ |
| 应用内自更新 + SHA256 + 回滚 | ✅ | ⚠️ 部分（无回滚） | ❌ |

### 总结

这个项目的核心优势在于 **「一站式、图形化、跨平台」** 的 SDK 版本管理体验。它解决了开发者需要安装和管理多种 SDK 版本的痛点，将原本需要通过多个命令行工具（nvm、sdkman、pyenv、rustup 等）完成的工作，统一到一个直观的桌面应用中。

不用再为每种语言记一套 CLI 命令，不用再折腾 shell 配置文件，不用再忍受 PATH 越来越长 —— 一套图形界面加一套命令，在 Windows、macOS、Linux 上体验完全一致。按场景挑外壳即可：探索用窗口，脚本与 CI 用[命令行](#命令行)。

## 技术栈

- **后端**：Go 1.25 + Wails v2
- **前端**：React + TypeScript + Vite + Ant Design
- **桌面框架**：Wails v2（WebView2 / WebKitGTK）

## 项目结构

```
svc/
├── main.go                    # 入口（shim 模式 / CLI / GUI 分发）
├── app.go                     # Wails App 门面 + 运行时适配器
├── about.json                 # 应用信息（版本、协议、updateUrl）
├── internal/
│   ├── cli/                   # 命令行外壳：cobra 命令树、输出层、退出码
│   │   ├── clirt/             # 运行时适配器（信号、退出），以接口解耦
│   │   └── i18n/              # 与 GUI 共用文案的 catalog 读取层
│   ├── appcore/               # GUI 与 CLI 共用的服务层引导
│   ├── apperr/                # 带类型的错误码，两个外壳各自翻译
│   ├── svclock/               # 跨进程操作锁（flock / LockFileEx）
│   ├── detach/                # 无窗口后台 spawn
│   ├── installer/             # SDK 安装/切换/取消 + 版本状态
│   ├── importer/              # 从压缩包/文件夹/PATH 导入 SDK
│   ├── storage/               # 卸载、存储统计、缓存、安装位置迁移
│   ├── update/                # 自更新：检查/下载/应用/回滚
│   ├── pkgmgr/                # 包管理器检测与安装
│   ├── proxy/                 # 代理配置、GitHub 镜像、SSRF 防护检查
│   ├── settings/              # 设置策略（token 掩码、保存保护）
│   ├── logmgr/                # 日志文件管理
│   ├── helpers/               # 纯工具：命令构建、env、版本、路径
│   ├── wailsrt/               # Wails 运行时接口（解耦服务层）
│   ├── packaging/             # 构建产物内容测试（NSIS 模板）
│   ├── config/                # 配置（settings、install path、shell 检测）
│   ├── downloader/            # 多线程 HTTP 下载器（支持代理）
│   ├── extractor/             # 压缩包解压（zip、tar.gz、7z 等）
│   ├── logger/                # 结构化日志
│   ├── pathmgr/               # PATH 环境变量管理（unix/windows）
│   ├── fsutil/                # 各服务共用的文件系统工具
│   ├── migrate/               # 磁盘布局迁移
│   ├── shim/                  # Shim 执行器 + 参数检测
│   ├── shimmanager/           # Shim 安装 + shell RC 编辑
│   └── sdk/                   # 各 SDK 的版本获取、安装逻辑
├── frontend/
│   ├── src/
│   │   ├── App.tsx            # 主组件
│   │   ├── components/        # Sidebar、DetailPanel、Settings 等
│   │   ├── i18n/              # en / zh 翻译文件
│   │   └── types/             # TypeScript 类型定义
│   └── wailsjs/               # Wails 自动生成的绑定
├── skills/
│   └── svc/                   # Agent 技能：AI agent 该如何调用这套 CLI
├── winres/                    # Windows 资源（图标、清单）
└── .github/workflows/
    ├── ci.yml                 # PR 上的 lint + test + commit-lint
    └── build.yml              # tag 推送时 6 平台构建 + 发布
```

## 数据存储

- **SDK 安装目录**：默认 `~/.svc/`（可自定义），结构为 `~/.svc/{sdk-type}/{version}/`
- **Shim 目录**：`~/.svc/shims/`（加入 PATH；路由到激活版本）
- **应用配置**：`~/.svc/settings.json`（主题、语言、代理、端点等）
- **日志**：`~/.svc/logs/`
- **更新备份**：运行中二进制旁的 `<exe>.bak`（每次升级生成）

## 开发

### 环境要求
- Go 1.25+
- Node.js 18+
- Wails CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- 仅 Linux 需要：`libgtk-3-dev libwebkit2gtk-4.0-dev libayatana-appindicator3-dev librsvg2-dev`

### 启动开发服务器

```bash
wails dev
```

### 构建

```bash
# 当前平台
wails build

# 指定输出文件名
wails build -o svc
```

### 跨平台打包说明

Wails v2 不支持交叉编译 Linux（CGO 链接 webkit2gtk/gtk），Linux ARM64 需用 native runner。Windows ARM64 和 macOS ARM64 可在 amd64 runner 上交叉编译（无 CGO）。CI workflow [.github/workflows/build.yml](.github/workflows/build.yml) 会在 tag 推送时自动处理全部 6 个目标。

## 安装

从 [Releases](https://github.com/mose-x/svc/releases/latest) 下载对应平台的产物：

| 平台 | 产物名 |
|---|---|
| Windows x64 | `svc-{ver}-windows-x64.exe` |
| Windows arm64 | `svc-{ver}-windows-arm64.exe` |
| macOS x64 | `svc-{ver}-macos-x64.dmg` |
| macOS arm64 (Apple Silicon) | `svc-{ver}-macos-arm64.dmg` |
| Linux x64 | `svc-{ver}-linux-x64` |
| Linux arm64 | `svc-{ver}-linux-arm64` |

用发布的校验文件验证完整性：

```bash
sha256sum -c sha256sums.txt --ignore-missing
```

### Homebrew（macOS）

```bash
brew trust mose-x/tap
brew tap mose-x/tap
brew install --cask mose-x/tap/svc
```

安装的是最新 release 的 `.dmg`，会放进 `/Applications`。cask 位于 [mose-x/homebrew-tap](https://github.com/mose-x/homebrew-tap)，那边的定时任务每天按已发布的 release 资源重写版本号并重算全部 `sha256`。Homebrew 只是可选路径：直接用[安装](#安装)一节的下载链接即可，不需要下面这些。

注意事项见 `brew info --cask mose-x/tap/svc`：应用未签名（首次启动需右键 → 打开），卸载后会保留 `~/.svc` 以及 shell 配置里的 `source ~/.svc.rc` 这一行。

Homebrew 7 会拒绝加载未信任第三方 tap 的**任何**条目，上面的全限定名也不例外，所以 `brew trust mose-x/tap` 要放在最前面。它是幂等的，而且不要求 tap 已经存在，因此这个顺序在从没见过该 tap 的机器上同样成立。Homebrew 6 只对短名（`brew install --cask svc`）要求信任；两者都可以用 `brew untrust mose-x/tap` 撤销。

信任状态按用户记录在 `~/.homebrew/trust.json`（设置了 `$XDG_CONFIG_HOME` 时是 `$XDG_CONFIG_HOME/homebrew/trust.json`），不在 tap 目录里，所以 `brew untap`/`brew tap` 和 `brew update` 都不会清掉它 —— 但重装 Homebrew、换机器或换 macOS 账号都要再执行一次。`brew trust` 命令本身需要 Homebrew 5.1.15 及以上；更老的版本既不强制信任也没有这个命令。执行之前 `brew tap-info mose-x/tap` 会显示 `Untrusted`，这是预期且无害的。请用它而不是 `HOMEBREW_NO_REQUIRE_TAP_TRUST` —— 后者已被上游标记为废弃并将移除。

## 自动更新机制

不需要任何清单文件。应用请求 [about.json](about.json) 里 `updateUrl` 指向的 GitHub Releases API（`https://api.github.com/repos/mose-x/svc/releases/latest`），去掉结尾的 `/latest` 以便按创建时间倒序读取 release **列表**，再从 `tag_name` 取版本、从 `body` 取更新日志、从 `assets[]` 取下载地址。带预发布后缀的 tag（`-rc1`、`-beta`、`-alpha`、`-pre`、`-dev`）会被跳过，因此只会推送纯 `vX.Y.Z`，与 GitHub 自己是否标为 pre-release 无关。每个 release 还会附带独立的 `sha256sums.txt`。

### 更新流程

1. **检查**：应用内「检查更新」读取 release 列表，选出最新的稳定 tag，与当前运行版本按语义化版本比较。
2. **下载**：用户点「下载并安装」；新二进制下载到临时路径，实时显示进度，应用配置的 GitHub 镜像和代理。
3. **校验**：按文件名在该 release 的 `sha256sums.txt` 资源里查出期望摘要，与实际下载到的文件算出的 SHA256 比对。不匹配则删除临时文件并中止更新（防御 CDN 篡改或下载不完整）。如果某个 release 没有 `sha256sums.txt`，则记录一条警告并跳过校验，这样老版本依然可以更新。
4. **应用**：生成 shell/batch 脚本，等待应用退出 → 备份当前二进制到 `<exe>.bak` → 替换为新版本 → 重启。在 macOS 上还会重写 bundle 内 `Info.plist` 的 `CFBundleShortVersionString` 和 `CFBundleVersion` —— 只换二进制的话这两个值会留在旧版本上。
5. **回滚**（可选）：新版本出问题时，设置页的「回滚」通过同样的等待-替换-重启模式恢复 `<exe>.bak`。一次性 —— `.bak` 始终指向紧邻的上一版本。

### 资源选择

`matchPlatformAsset` 按文件名从 `assets[]` 里挑出当前平台的资源，遵循 `svc-<ver>-<os>-<arch><ext>` 的构建约定。Go 的两个取值都不能直接匹配文件名：`runtime.GOOS` 的 `darwin` 对应 `macos`，`runtime.GOARCH` 的 `amd64` 对应 `x64`。

| 平台 | 自更新用的资源 | 有意跳过的 |
| --- | --- | --- |
| Windows x64 / arm64 | `svc-<ver>-windows-x64.exe`、`svc-<ver>-windows-arm64.exe` | `*-installer.exe` —— 仅用于首次安装，且运行它需要 UAC |
| macOS x64 / arm64 | `svc-<ver>-macos-x64.bin`、`svc-<ver>-macos-arm64.bin` | `.dmg` |
| Linux x64 / arm64 | `svc-<ver>-linux-x64`、`svc-<ver>-linux-arm64` | `.deb`、`.rpm` |

> macOS 自更新用 `.bin`（裸二进制），因为 `ApplyUpdate` 替换的是 `.app` bundle 内的二进制，不是 bundle 本身。`.dmg` 仅用于手动安装。

### 发布新版本

CI 全自动 —— 只需推送 tag：

```bash
git tag v1.1.0
git push origin v1.1.0
```

[build.yml](.github/workflows/build.yml) workflow 会构建全部 6 个平台产物，生成 `sha256sums.txt`，并发布非草稿 GitHub Release。应用内更新器通过 Releases API 立即感知。带预发布后缀的 tag 会被发布为 GitHub pre-release，因此同样不会进入 `releases/latest`。

## License

MIT License
