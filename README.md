# AdRoute

本地 Responses API 代理，提供网页管理界面，方便给 Codex 等客户端配置和切换模型。

- 管理多个模型的 API 地址、密钥和模型 ID，支持测速、复制、配置导入导出。
- 支持聚合模型，按顺序切换成员，实现故障转移。
- 模型与聚合提供常用收藏、归档和搜索；支持整组归档与记住分组折叠。
- “客户端接入”页一键开关 Codex / OpenCode：打开即接入 AdRoute，关闭即恢复原配置，可随时显式刷新。
- 支持 Linux / WSL 和 Windows 10/11，运行时不需要 Go 或 Node.js。

## 一键安装

安装最新[正式发布版](https://github.com/advisedy/adroute/releases/latest)。安装器自动选择架构、校验文件并安装到个人目录，无须管理员权限。

### Linux / WSL

复制到 Bash 或 Zsh 终端运行（需要 curl、Bash 和 sha256sum）：

```bash
curl -fsSL https://raw.githubusercontent.com/advisedy/adroute/main/install-online.sh | bash
```

### Windows

复制到 **PowerShell 7 或更新版本**运行（可用 `pwsh` 打开；[下载 PowerShell](https://aka.ms/powershell-release?tag=stable)）：

```powershell
irm https://raw.githubusercontent.com/advisedy/adroute/main/install-online.ps1 | iex
```

安装完成后，**重新打开终端**，启动服务：

```bash
adroute start
```

也可以在 [Release 页面](https://github.com/advisedy/adroute/releases/latest)手动下载。提供 amd64 / arm64；arm64 尚未实机验证，macOS 暂不支持。

下载会自动续传重试；如果所在网络无法直连 GitHub 的下载 CDN，设置代理后重跑同一条命令：Linux 用 `HTTPS_PROXY`，Windows 用 `$env:ADROUTE_INSTALL_PROXY`。详见[安装与排障](docs/usage.md)。

## 开始使用

1. 打开启动命令输出的管理链接。
2. 点击“添加模型”，选择或新建模型组，填写上游 Base URL、API Key 和模型 ID，保存。
3. 按需设置默认模型；需要故障转移时，在“聚合”中添加多个成员模型。
4. 打开“客户端接入”，按需打开 Codex / OpenCode 的开关；之后直接运行客户端并通过模型选择器挑选模型。其他支持 Responses API 的客户端，可在同一页展开“手动接入”复制本地地址与代理密钥，在模型行复制对外模型 ID。

默认服务地址为 `http://127.0.0.1:3001`，仅供本机访问。支持 `/v1/responses` 和 `/v1/models`，上游也需要支持 Responses API。

模型管理和聚合列表中的圆灯控制**对外开放**：绿色表示开放，点击变为红色后，外部目录不再列出该条目，已保存模型 ID 的客户端也不能直接调用。后台编辑、测速、默认选择和作为聚合成员使用不受影响。关闭聚合不改变成员的开放状态；关闭当前默认的对外开放后，外部默认路由同样不可用，重新开放即可恢复。

网络异常时会重新读取服务器状态；若仍无法确认，圆灯显示黄色“状态待确认”，请刷新后再操作，不要假定已关闭。旧配置中已停用的条目可单独“恢复启用”，不会改变对外开放设置。开关影响后续请求，已开始的请求仍按接收时的配置完成。

### 工作台收纳

模型与聚合可标星收藏，在“常用 / 全部 / 已归档”之间切换。有未归档收藏时首次进入默认展示“常用”，否则展示“全部”。搜索可跨分组与收藏，按名称、真实模型 ID、对外 ID 或分组查找；需要时勾选“包含归档”。清空搜索会恢复原有收藏、分组筛选和折叠状态。

收藏与归档随配置保存、可导入导出，**不改变对外开放、默认选择或聚合成员可用性**。整组归档仅让成员随组隐藏，不覆盖各模型原来的归档标记；恢复分组后，单独归档的模型仍保持归档。分组折叠仅保存在当前浏览器。新建或复制后会调整视图以显示新增条目；导入后会清除旧筛选、展开分组，并按需包含归档，但不会修改保存的收纳标记。连接信息可展开“详情”；复制、设为默认、打开网站、归档和删除在“更多”中；组管理与配置导入导出在“管理与备份”中。导出文件含明文密钥，请妥善保管。

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

更新发布版时，先运行 `adroute stop`，再执行上面同一条安装命令，最后运行 `adroute start`。安装入口会自动获取最新正式版本。

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
