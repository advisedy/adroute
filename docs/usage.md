# AdRoute 详细使用说明

AdRoute 是本机运行的 Responses 代理与网页模型管理控制台：每个模型条目保存自己的上游 Base URL、上游 API Key 与真实模型 ID，客户端通过统一的本地地址和本地代理密钥调用。

## 支持范围与要求

- 支持 Linux / WSL 和原生 Windows 10/11；macOS 尚未适配。Windows 使用原生文件锁、访问控制和后台进程，无须 WSL。WSL 重启后需重新启动服务。
- 正式运行只需一个 Go 可执行文件；网页资源、修复包示例和排查 Prompt 已嵌入其中。运行时不需要 Node.js、npm 或源代码目录，也不需要下载运行组件；连接上游 API、获取模型列表和实际推理仍需要相应网络。
- 从源码构建需 Go 1.23 或更新版本。Codex 客户端配置接入与恢复兼容官方 Codex CLI **0.156.0**；模型调用本身不依赖 Codex 登录，也不依赖订阅账号。
- 配置只接受当前 version 6 格式；没有旧格式迁移、`migrate` 命令或旧路由兼容。损坏/不支持的配置会报错并保留原文件。

## 安装

### 一键安装最新正式版本

Linux / WSL（自动配置 Bash / Zsh 的 PATH）：

```bash
curl -fsSL https://raw.githubusercontent.com/advisedy/adroute/main/install-online.sh | bash
```

Windows（要求 PowerShell 7+，用 `pwsh` 打开）：

```powershell
irm https://raw.githubusercontent.com/advisedy/adroute/main/install-online.ps1 | iex
```

没有 PowerShell 7 时，请从 [PowerShell 官方下载页](https://aka.ms/powershell-release?tag=stable)安装；Windows 自带的 Windows PowerShell 5.1 不适用这个入口。入口会先确定最新正式版，再校验并执行该版本的安装脚本；下载或校验失败会停止。安装后重新打开终端运行 `adroute start`。更新前先执行 `adroute stop`，然后重跑同一条安装命令。

需要自定义目录时，可在执行入口前设置 `ADROUTE_INSTALL_DIR`：Linux 中它是安装前缀（程序位于其 `bin` 下），Windows 中它是程序所在目录。需要指定版本或禁止修改 PATH 时，下载下面的版本安装器后按参数执行。

下载失败（例如 `unexpected EOF`、连接被重置、哈希不匹配）时，安装器会保留已下载的部分并续传重试，Linux 最多 5 次、Windows 最多 3 次；仍然失败才报错。如果所在网络无法直连 GitHub 的 Release 下载，可在执行入口前设置代理，两个平台都支持：

```bash
# Linux / WSL：curl 直接读取这两个变量
export HTTPS_PROXY=http://127.0.0.1:7890
curl -fsSL https://raw.githubusercontent.com/advisedy/adroute/main/install-online.sh | bash
```

```powershell
# Windows：PowerShell 7 的 Invoke-WebRequest 不读取 HTTP(S)_PROXY，需显式指定
$env:ADROUTE_INSTALL_PROXY = 'http://127.0.0.1:7890'
irm https://raw.githubusercontent.com/advisedy/adroute/main/install-online.ps1 | iex
```

`ADROUTE_INSTALL_PROXY` 会被传给下载的版本安装器（也接受 `-Proxy` 参数）。只想先拿到可执行文件时，按下一节手动下载并安装。

### 一键安装 v0.1.0（无需 Go）

从 [GitHub Releases](https://github.com/advisedy/adroute/releases/tag/v0.1.0) 安装预编译版本，支持 Linux / WSL、Windows 的 amd64 和 arm64。安装器自动选择架构，下载二进制并核对 SHA-256，无须管理员权限、Go 或 Node.js。

Linux / WSL（需要 curl、Bash 和 sha256sum）：

```bash
curl -fsSL https://github.com/advisedy/adroute/releases/download/v0.1.0/install-release.sh -o /tmp/adroute-install.sh && bash /tmp/adroute-install.sh --configure-path
```

Windows PowerShell：

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; $installer = Join-Path $env:TEMP 'adroute-install.ps1'; Invoke-WebRequest -UseBasicParsing https://github.com/advisedy/adroute/releases/download/v0.1.0/install-release.ps1 -OutFile $installer; if (-not $?) { throw 'Download failed' }; powershell -NoProfile -ExecutionPolicy Bypass -File $installer
```

安装后重新打开终端，运行 `adroute start`。Linux 默认安装到 `~/.local/bin` 并配置 Bash/Zsh PATH；Windows 默认安装到 `%LOCALAPPDATA%\Programs\AdRoute` 并加入用户 PATH。更新前先运行 `adroute stop`，再重跑安装命令；脚本不会自动停止或启动服务。使用 `--no-modify-path`（Linux）或 `-NoModifyPath`（Windows）可禁止修改 PATH；使用 `--prefix` 或 `-InstallDir` 可指定目录。

也可以直接在 Release 页面下载对应平台的单个可执行文件。Linux 下载后执行 `chmod +x`；Windows 下载 `.exe` 后直接运行。`SHA256SUMS` 提供下载文件的校验值。amd64 已通过两平台原生测试；arm64 仅经过交叉编译，尚未实机验证。

### 从源码构建

以下 Bash 命令适用于 Linux / WSL；原生 Windows 见下方 PowerShell 安装。

```bash
make build
./bin/adroute --version
```

构建产物为 `bin/adroute`，已忽略且不纳入源码包。也可以直接使用 Go 的安装机制：

```bash
go install ./cmd/adroute
```

### 安装到个人目录

推荐从源码目录运行一键安装器（只安装到个人目录，不使用 sudo）：

```bash
./install.sh
```

安装器从源码构建当前版本并原子更新 `$HOME/.local/bin/adroute`，不依赖 make/npm，也不会重启正在运行的服务。交互终端会先列出具体目标文件并询问是否将带运行时去重保护的 PATH 块写入用户 shell 配置；拒绝或 EOF 时仍可用完整路径运行，但不会持久配置 PATH。新终端才会读取持久设置；当前 PATH 中即使暂时含有安装目录，也不代表新终端已持久配置。安装器不会改变当前父 shell 的 PATH 或命令缓存，并会提示当前 `adroute` 命令是否被其他路径中的命令遮蔽。

仅支持 Bash 和 Zsh：Bash 管理 `~/.bashrc` 及生效的登录 profile（优先级顺序为 `.bash_profile`、`.bash_login`、`.profile`）；Zsh 管理导出的 `ZDOTDIR`（若有）下的 `.zshrc`。不会修改系统文件或执行启动脚本来探测现有 PATH；只读取所选文件检查自己的标记块。Zsh 若只在 `.zshenv` 设置 `ZDOTDIR`、或使用条件化自定义配置，可能需要手动选择正确文件。其他 shell 不会猜测。

无交互终端默认不改启动文件；可明确同意配置，或禁用修改：

```bash
./install.sh --configure-path
./install.sh --no-modify-path
./install.sh --prefix "$HOME/.local" --shell zsh --configure-path
./install.sh --binary /path/to/adroute --prefix "$HOME/.local" --configure-path
```

`--binary` 安装指定的可执行文件，不需要 Go 或源码。安装器会读取所选启动文件以定位和更新自己的精确标记块，但绝不会执行或 source 这些文件。每次需要改动已有文件前，都会在同目录创建内容精确、权限为 0600 的唯一备份；不会覆盖既有备份。未获同意时可直接运行 `$HOME/.local/bin/adroute`。低层 `make install PREFIX=...` 仍仅负责构建/复制，不持久设置 PATH；`go install` 则按 Go 的 `GOBIN`（默认 `GOPATH/bin`）安装。

### 从发布包安装

将适用于 Linux 的单个 `adroute` 二进制放到个人 PATH 目录并添加可执行权限；无须安装 npm 包。

### 原生 Windows（PowerShell）

安装 Go 1.23 或更新版本，下载并解压完整源码，在项目目录打开 PowerShell：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

脚本编译并安装到 `%LOCALAPPDATA%\Programs\AdRoute\adroute.exe`，加入当前用户 PATH，无须管理员权限。重新打开终端后运行 `adroute start`。也可立即用完整路径启动：

```powershell
& "$env:LOCALAPPDATA\Programs\AdRoute\adroute.exe" start
```

已有适合本机架构的 Windows 可执行文件时，可以使用 `-Binary .\adroute.exe` 安装，无须 Go。`-InstallDir` 指定安装目录，`-NoModifyPath` 禁止修改 PATH。更新前先执行 `adroute stop`，Windows 不允许覆盖正在运行的程序。脚本不会自动停止服务。

安装完成后不需要 Go、Node.js 或源码。后台启动、状态、停止、重启命令与 Linux 一致；前台运行支持 Ctrl+C。后台服务不注册为 Windows 系统服务，也不自动开机启动。在线下载安装使用上面的 `install-release.ps1`，源码安装器 `install.ps1` 本身不负责下载。

## 启动与命令

```bash
adroute start                    # 后台启动；就绪后尝试自动接入 Codex
adroute restart                  # 按当前端口停止并用本二进制重新启动；模型配置与令牌不变
adroute status                   # 检查本机服务身份和健康状态
adroute status --json            # JSON 状态；未运行时退出码 3
adroute stop                     # 停止服务并恢复仍由本实例管理的 Codex 设置
adroute codex restore            # 单独重试恢复 Codex 设置
adroute start --foreground       # 前台调试；Ctrl+C 有界关闭并恢复配置
adroute restart --foreground     # 前台重启（先停止旧实例，再前台运行本二进制）
adroute start --port 3002        # 指定端口
adroute start --port 0           # 让系统分配空闲端口
adroute --help
adroute --version
```

`adroute restart` 等价于“按当前端口 `adroute stop` + 用当前可执行文件 `adroute start`”：它会恢复旧实例写入的 Codex 配置再重新接入，模型组、模型条目、令牌和默认端口都保持不变。它适合在替换二进制或改端口以外的配置后重载服务；服务未运行时等同于 `adroute start`。Codex 恢复失败只报警告并继续重启，不会把服务留在停止状态。

默认绑定 `127.0.0.1:3001`，不提供 `0.0.0.0` 监听选项。启动只在健康检查确认身份后报告成功。Codex 配置接入失败会明确报错；后台服务仍可能在运行，可先检查配置、执行 `adroute codex restore`，再重试。后台运行不等同于开机自启。

启动输出的管理链接含管理员令牌，代理 API Key 是另一枚本机令牌；两者均为凭据，不要分享。管理网页读取链接令牌后会移除 URL 片段，退出会清除页面与会话凭据。

## 配置目录

默认数据目录按以下顺序选择：非空 `ADROUTE_HOME`；否则非空 `$XDG_CONFIG_HOME/adroute`；否则用户主目录下的 `.config/adroute`（Windows 为 `%USERPROFILE%\.config\adroute`）。最终目录会解析为绝对路径；若指定的 `ADROUTE_HOME` 或 `XDG_CONFIG_HOME` 是相对路径，则相对当前工作目录解析。所有同一实例的 `start`、`status`、`stop`、`codex restore` 必须使用一致的环境变量和工作目录。

```bash
ADROUTE_HOME="$HOME/.config/adroute-dev" adroute start --port 3002
```

`CODEX_HOME` 用于选择 Codex 配置目录；`ADROUTE_PORT` 是默认监听端口，`ADROUTE_CODEX_BIN` 可指定 Codex CLI 的可执行文件。Linux 数据目录默认 0700，含凭据的文件默认 0600；Windows 使用仅当前用户和 SYSTEM 可访问的 ACL。`config.json` 保存模型组、模型条目与上游 API Key 明文，因此应保护整个账号和数据目录；这不是加密存储。Windows 使用支持 ACL 的本地文件系统；网络共享和 FAT/exFAT 不在验证范围内。

AdRoute 只接受当前 version 6 配置，不会读取旧 `auth.json`、旧 `providers.json` 或迁移旧格式。若启动提示配置不支持，请先停止服务，将报错提到的配置文件**自行备份到安全位置**，再重新配置；备份也包含凭据。不要覆盖损坏文件。

## 网页与路由

1. 打开 `adroute start` 输出的管理链接；页面顶部显示本地 Base URL 与本地代理密钥（默认遮蔽，可显式显示或复制）。
2. 点击“添加模型”；需要时在表单内先创建模型组（小写英文字母、数字和单个连字符）。
3. 填写该模型自己的上游 Base URL、上游 API Key 和真实模型 ID；可以用“获取模型”从当前填写的地址与密钥里挑选一个，也可直接手填。
4. 填写模型名称并保存。名称只能使用英文字母、数字、点、下划线和连字符；新建时默认填入真实模型 ID，非法或同组重名会要求手动修改，不自动转写或追加序号。
5. 每条模型都有并排的“编辑 / 测速 / 复制 / 删除”和开放开关。测速会发送一次真实短文本请求，只用于确认能否返回文本并附带参考 token/s；它不会开放模型、修改默认或写客户端配置。复制会在同组新建独立条目，保留连接、修复绑定和开放状态，名称依次为 `原名称-copy`、`原名称-copy-2` 等；不会复制默认身份或测速结果。名称加后缀超过 100 字符时需先缩短原名称。

“管理组”旁的“导出配置”可下载 JSON 文件，包含模型组、模型的上游地址和 API Key、开放状态、默认模型及已保存的兼容修复包和绑定。**文件中的 API Key 是明文，不要分享，迁移后也要妥善保管或安全删除文件。**在另一台电脑安装并启动 AdRoute 后，点击“导入配置”选择文件并确认；导入会整体覆盖当前模型配置，建议先导出目标电脑原有配置。文件无效或确认期间配置发生变化时不会覆盖。管理令牌和本地代理密钥不会导出或改变，请使用目标电脑自己的接入凭据。兼容修复包的源文件目录不会迁移；如需在修复包管理器中继续管理源文件，应另行复制这些文件。请求记录、测速结果、客户端配置和诊断文件也不包含在导出中。

对外模型 ID 是 `adroute/<组名>/<模型名称>`，客户端目录的显示名称是“组名 / 模型名称”，实际发给上游的是该条目的真实模型 ID。修改地址、Key 或真实模型 ID 不改变对外 ID，也不会重建条目；改名、换组或改组名会改变对外 ID，旧选择失效且不回退。组只是分类：同组可以混用不同地址与凭据，删除非空组会报错。

每个条目可独立开放或关闭。关闭后从客户端目录和目录文件中消失，缓存了旧 ID 的客户端也会收到 `MODEL_ROUTE_DISABLED`，管理页仍可编辑与显式测速。网页默认模型只是可选设置：只能选开放条目，不改变其他条目状态；关闭当前默认会在一次后端更新中清空默认，重新开放不恢复默认。已接收请求按到达时的配置快照继续，新请求立即使用新状态。

测速完成后，模型下方只显示 `XX.X tokens/s` 和一个小信息标识。鼠标悬停、键盘聚焦或手机点击标识可查看“速度 · 首字 · 总耗时 · 输出 tokens · 返回正文”；点击空白处或按 Escape 关闭。正文是本次实际回复，不是预设数字或模拟文本；最多展示 4096 个字符，先脱敏再截断，不保存到配置、日志或浏览器存储，退出后清除。

测速遇到有效结束事件即返回，不等连接关闭。速度为 `正文 tokens / 第一个到最后一个非空白文本分片的秒数`；正文用量为最终 `output_tokens - output_tokens_details.reasoning_tokens`。不包含首字等待、推理用量和最后文本之后的结束事件等待，200～300 tokens/s 等高速结果没有人为上限。SSE 分片可能包含多个 tokens，首分片也在总用量中，因此该指标仍是分片采样参考，不是精确逐 token 解码基准；说明放在详情中，不占据列表。

有可信总用量与推理明细时，正文 token 数直接取二者之差；缺少用量或推理明细时，对可见正文使用离线内置的单一 `cl100k_base` 分词器估算。估算结果显示“约 XX.X tokens/s”，详情明确说明分词标准、参考正文数量，以及它不是上游计费 token 数、可能与该模型实际分词不同；绝不默认推理用量为 0，也不把推理文本计入正文。估算仅处理不超过 4096 字节的短回复，不在运行时联网下载词表。

输出中包含工具等其他非文本项目、少于两个文本分片、少于 32 个正文 tokens 或输出跨度不足 50 ms 时仍不报告速度；JSON/单分片/过短样本不推算速度，并明确说明原因。API 的 `textTokensSource` 区分 `usage` 与 `cl100k_base-estimate`，`usageReported` 仅表示真实上游用量，不会被估算冒充。旧 `tokensPerSecond` 仍保留端到端口径供兼容使用，页面只使用新的 `textTokensPerSecond`，绝不以旧值回退冒充正文速度。

固定提示词要求连续输出 1～120 的整数，以空格分隔，不加解释，形成稳定的正文输出样本；数字个数不当作 token 数。单次输出上限为 1024 tokens（包含推理），总超时仍为 18 秒，只发一次请求且不自动重试。192 tokens 的旧上限可能在推理期间耗尽、尚未产生正文；此类响应在详情中明确展示额度耗尽及已报告的推理用量，不笼统判定模型不可用。1024 上限也不保证所有推理模型都能在本次短测内产生正文。

更换上游 Key、地址或真实 ID 后会清除旧测速结果。若模型配置已保存但 Codex 目录发布失败，页面会单独提示，可在处理文件／权限问题后点击“重试发布目录”，不必重复保存模型。

代理支持 `POST /v1/responses` 与 `GET /v1/models`，不提供 Chat Completions 或 Anthropic 转换、语音/图像生成。普通模型路由直接转发；聚合路由可按配置顺序和轮次在可重试错误后切换成员。上游是否支持 Responses 取决于该地址和模型本身。

## Codex 接入与恢复

服务启动后会直接以本地语法安全的 TOML 编辑更新 `CODEX_HOME/config.toml`，写入代理配置、`adroute/default` 和模型目录路径；不会为此启动 Codex helper，也不要求 Codex 登录。配置预览可在网页“客户端接入”查看。

普通使用：启动 AdRoute、在网页保存模型（可选设置默认），然后运行 `codex`。Codex `/model` 可以固定一个对外模型 ID，或恢复跟随网页默认。更新模型或开放状态后目录会更新，重开 Codex 让它重新读取。

`adroute stop` 或 `adroute codex restore` 会逐项恢复仍与 AdRoute 写入值一致的字段，尽量保留之后的用户编辑；不会直接用整份旧备份覆盖新配置。原始备份保存在私有数据目录中。

## OpenCode 同步

同步是网页中明确触发的操作，不会自动调用模型，也不会在保存条目时运行。它只更新既有 OpenCode 配置内的 AdRoute 模型映射，并创建私有原文件备份。目标配置必须已有兼容 Responses 的 `providers.adroute` 设置；遇到缺失或不兼容会报错，不会猜配置或创建新配置。同步不启动或重载 OpenCode。详见 [OpenCode 同步说明](opencode-sync.md)。

## Responses 兼容包与诊断

只对用户明确绑定到单个模型条目的修复包运行 `responses-tools-v1/v2/v3`；其余请求原样透传。包刷新只做本地格式与合成样例验证，不代表真实上游验证。诊断只采集下一次匹配的正常请求；保留原始响应需单独同意。导出/交给本地 AI 读取需再次确认。诊断可能包含代码、路径或业务信息，分享前需人工检查。回放不执行任何工具。

详细格式、限制与隐私边界见 [Responses 兼容适配说明](compatibility.md)；Prompt 模板为 [`repair-agent-prompt.md`](repair-agent-prompt.md)。

## 文件更新与并发限制

配置修改采用合作式锁、文件/父目录检查、最终身份与内容复核及同目录原子替换。它**不提供对任意不合作外部编辑器的可移植原子 compare-and-swap**：其他程序若恰好在最后复核与替换间并发写配置，仍可能发生竞争。使用 OpenCode 同步或 Codex 配置编辑时，请勿同时用其他工具编辑目标文件。可检测到的中途变化会失败关闭，备份保留供人工检查。

## 从源码测试（开发者）

正式运行不需要 Node/npm。只有执行新的 Go 后端浏览器/Codex 黑盒验收时才需要 Node.js、npm、Playwright 和 Chromium：

```bash
go test ./...
go test -race ./...
go vet ./...
make build
npm ci
npx playwright install chromium
npm test
```

`npm run test:browser` 运行 `browser/` 下的页面行为测试（`go-acceptance.test.js`、`availability.test.js`），`npm run test:codex` 运行 `integration/go-codex.test.js`；它们都会构建/启动真实 Go 二进制，使用临时目录、模拟 loopback 上游和隔离的 Codex home。官方 Codex CLI 端到端部分仅在 `codex --version` 为 0.156.0 时运行，否则报告清晰的可选条件；不会使用真实账号、上游凭据或推理。Node/npm 不是服务端或生产构建的运行依赖。

## 排障

- **端口占用**：用 `adroute status` 查看已确认的本机实例；使用 `adroute start --port 3002` 切换，不要按陈旧 PID 手工杀进程。
- **服务显示状态不确定**：身份无法确认时 `stop` 会拒绝发送停止信号。先检查 `ADROUTE_HOME/run/service.json`、日志和端口，不要删除不确定实例的状态文件。
- **Codex 配置接入失败**：检查 `CODEX_HOME`、TOML 内容和权限。配置冲突/损坏时 AdRoute 会拒绝覆盖；必要时用 `adroute codex restore` 后人工检查。
- **请求返回无默认模型/路由不存在**：网页需保存模型；省略 `model` 时要求存在开放的默认模型。已删除、已关闭或改名后的对外 ID 不会回退，重新打开 `/model` 选择有效条目。
- **上游连接失败**：确认该条目的 Base URL、上游 API Key、Responses 支持与网络；代理不会跟随携带凭据的重定向。普通模型路由不自动换模型，聚合路由按配置重试。
- **安装时下载中断**：`unexpected EOF`、`0 bytes from the transport stream`、`transfer closed` 表示连接在传完前被关闭，通常发生在 GitHub Release 大文件实际所在的下载 CDN 域名（`github.com` 本身可能是通的）。安装器会续传重试，仍失败时按上一节的代理说明设置 `HTTPS_PROXY`（Linux）或 `ADROUTE_INSTALL_PROXY`（Windows）后重跑。注意 Windows PowerShell 的 `Invoke-WebRequest` 不读取 `HTTP_PROXY`/`HTTPS_PROXY`，只认系统代理或显式参数。
- **安装时报哈希不匹配**：说明传输过程被改写或截断，重新执行即可（会重新下载）；反复出现时先排除企业网关的 TLS 检查、杀软扫描和镜像缓存。安装器校验失败会保留原有安装，不会覆盖成坏文件。

## 架构与功能说明

- [Go 架构与代码地图](architecture.md)：入口、服务生命周期、HTTP、Store、代理、兼容性、Codex 与 OpenCode。
- [复杂度与简化建议](complexity-review.md)：哪些复杂性有必要，哪些外围能力值得收窄。
- [模型组、模型条目与管理 API](api-services.md)
- [模型管理独立审查修复](model-management-review-fixes.md)：测速、异步状态、目录同步与严格输入的修复和验证。
- [Responses 兼容包和诊断](compatibility.md)
- [OpenCode 同步与文件安全](opencode-sync.md)
