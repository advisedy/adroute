# AdRoute

本地 Responses API 代理，提供网页管理界面，方便给 Codex 等客户端配置和切换模型。

- 管理多个模型的 API 地址、密钥和模型 ID，支持测速、复制、配置导入导出。
- 支持聚合模型，按顺序切换成员，实现故障转移。
- 自动接入 Codex，也支持在网页中同步到已配置好的 OpenCode。
- 支持 Linux / WSL 和 Windows 10/11，运行时不需要 Go 或 Node.js。

## 一键安装

当前发布版：[v0.1.0](https://github.com/advisedy/adroute/releases/tag/v0.1.0)。安装器自动选择架构、校验文件并安装到个人目录，无须管理员权限。

### Linux / WSL

复制到 Bash 或 Zsh 终端运行（需要 curl、Bash 和 sha256sum）：

```bash
curl -fsSL https://github.com/advisedy/adroute/releases/download/v0.1.0/install-release.sh -o /tmp/adroute-install.sh && bash /tmp/adroute-install.sh --configure-path
```

### Windows

复制到 PowerShell 运行：

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; $installer = Join-Path $env:TEMP 'adroute-install.ps1'; Invoke-WebRequest -UseBasicParsing https://github.com/advisedy/adroute/releases/download/v0.1.0/install-release.ps1 -OutFile $installer; if (-not $?) { throw 'Download failed' }; powershell -NoProfile -ExecutionPolicy Bypass -File $installer
```

安装完成后，**重新打开终端**，启动服务：

```bash
adroute start
```

也可以在 [Release 页面](https://github.com/advisedy/adroute/releases/tag/v0.1.0)手动下载。提供 amd64 / arm64；arm64 尚未实机验证，macOS 暂不支持。

## 开始使用

1. 打开启动命令输出的管理链接。
2. 点击“添加模型”，选择或新建模型组，填写上游 Base URL、API Key 和模型 ID，保存。
3. 按需设置默认模型；需要故障转移时，在“聚合”中添加多个成员模型。
4. 使用 Codex 时，直接运行 `codex`，通过 `/model` 选择模型。其他支持 Responses API 的客户端，使用管理页显示的本地地址、代理密钥和对外模型 ID。

默认服务地址为 `http://127.0.0.1:3001`，仅供本机访问。支持 `/v1/responses` 和 `/v1/models`，上游也需要支持 Responses API。

## 常用命令

```bash
adroute start                 # 后台启动
adroute status                # 查看运行状态
adroute restart               # 重启服务
adroute stop                  # 停止服务，恢复 Codex 设置
adroute start --port 3002     # 指定端口启动
adroute start --foreground    # 前台运行，Ctrl+C 退出
adroute codex restore         # 单独恢复 Codex 设置
adroute --version             # 查看版本
```

更新发布版时，先运行 `adroute stop`，再执行对应版本的安装命令，最后运行 `adroute start`。

## 从源码安装

需要 Go 1.23 或更新版本。在源码目录运行，可安装包含最新修复的版本：

Linux / WSL：

```bash
./install.sh --configure-path
```

Windows PowerShell：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

更新前先停止服务，安装后再启动。发布包与最新源码可能包含不同的修复。

## 数据与说明

配置默认保存在 `~/.config/adroute`（设置了 `XDG_CONFIG_HOME` 时为其下的 `adroute`）；Windows 默认为 `%USERPROFILE%\.config\adroute`。可用 `ADROUTE_HOME` 指定数据目录。

管理链接和配置导出文件含凭据，上游 API Key 在配置中以明文保存，请妥善保管。后台运行不会设置开机自启，WSL 重启后需重新启动服务。

- [详细使用、安装选项与排障](docs/usage.md)
- [OpenCode 同步说明](docs/opencode-sync.md)
- [Responses 兼容包与诊断](docs/compatibility.md)
- [架构与代码地图](docs/architecture.md)
