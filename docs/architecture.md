# Go 架构与代码地图

AdRoute 的生产实现是一个 Go 程序包：`go build ./cmd/adroute` 生成单个二进制，静态网页、适配示例和修复 Prompt 由 `go:embed` 打包。运行时不调用 Node/npm，不需要源码目录。

## 请求与进程路径

```text
cmd/adroute/main.go
  ├─ CLI：start / restart / status / stop / codex restore
  ├─ 服务监督：后台 exec 同一二进制、ready/identity、有限等待关闭
  └─ 内嵌资源 -> internal/adroute.NewHTTPApp
       ├─ HTTP 管理面与 public/*.js 控制器
       ├─ internal/adroute/store.go：私有配置（config.json）、验证、串行变更、路由快照
       ├─ internal/adroute/http_proxy.go：Responses 路由、SSE、超时/取消
       │    ├─ upstreams.go：模型发现、快速测速、工具结构测试、上游传输
       │    └─ adapter_*.go + compatibility.go：绑定引擎、事件整流、最终快照校验
       ├─ diagnostics.go + adapter_directory_state.go：取样、脱敏、回放、文件工作流
       ├─ codex_config.go：Codex TOML 配置接入与恢复
       └─ opencode_sync.go：显式 OpenCode V2 JSONC 模型映射同步

public/
  ├─ index.html：模型管理、请求记录、兼容修复、客户端接入
  ├─ controller.js：模型/组表单、开放状态、快速测速、异步状态
  ├─ compatibility-directory.js：修复包、诊断与 Prompt 控制器
  └─ styles.css：桌面与移动布局
```

## 模块职责

| 责任 | Go 实现 | 主要测试 |
|---|---|---|
| 命令行、服务启动/停止、就绪和进程身份 | `cmd/adroute/main.go` | `cmd/adroute/main_test.go` |
| 内嵌前端、示例和 Prompt | `assets.go` | `assets_test.go` |
| 状态、配置验证、原子持久化、组/模型 CRUD、默认与目录 | `internal/adroute/store.go`, `manage.go`, `validation.go`, `routes.go`, `types.go` | `store_test.go`, `availability_test.go` |
| HTTP 管理 API、认证、Host/Origin、安全响应与资源服务 | `internal/adroute/http.go`, `http_management.go` | `http_test.go`, `http_replay_assets_test.go`, `http_proxy_test.go` |
| Responses 上游传输、模型发现、快速测速、工具结构测试、压缩和响应流 | `internal/adroute/upstreams.go`, `http_proxy.go` | `quicktest_test.go`, `http_proxy_test.go` |
| v1/v2/v3 修复器及包校验 | `internal/adroute/compatibility.go`, `adapter_channels.go`, `adapter_stream.go`, `adapter_snapshots.go` | `compatibility_test.go` |
| 适配包目录与诊断隐私/Prompt | `internal/adroute/adapter_directory_state.go`, `diagnostics.go` | `compatibility_test.go`, `http_replay_assets_test.go` |
| Codex TOML 配置接入/恢复 | `internal/adroute/codex_config.go` | `codex_config_connect_test.go`, `cmd/adroute/main_test.go` |
| OpenCode V2 显式 JSONC 同步 | `internal/adroute/opencode_sync.go` | `opencode_config_test.go` |
| 共享推理档位（两个客户端同一份表） | `internal/adroute/reasoning.go` | `reasoning_test.go` |
| 客户端开关与状态（Codex/OpenCode） | `internal/adroute/clients.go` | `clients_test.go` |
| 模型收纳与公开暴露 | `internal/adroute/organization.go` | `organization_test.go`, `exposure_test.go` |
| 实际二进制网页与官方 CLI 合成端到端验收 | `browser/go-fixture.js`, `browser/go-acceptance.test.js`, `browser/availability.test.js`, `integration/go-codex.test.js` | `npm test` |

HTTP 层提供直接面向组/模型的管理 API 和 Responses 合同。管理路由是一张「方法 + 路径模式 → 处理函数」的表（`internal/adroute/http_management.go` 的 `managementRoutes`），模式里 `*` 捕获一段路径、`a|b` 表示二选一；每个 handler 拿到已解析的请求对象，不再共用一个跨越多分支的长函数。管理令牌与代理令牌分离；配置变更先验证，再在 Store 锁内提交；请求接收时取得路由快照，避免请求途中被后续编辑改道。没有开放默认时省略 `model` 会得到明确无默认错误，只有字段缺失（不是 `null`、数字或空字符串）才代表跟随 `adroute/default`。快速测速从 HTTP 请求 context 派生 18 秒总截止时间，取消会关闭上游响应体。

以下几条是 0.7.1 时期的修复记录，行为保留至今，保留原文以便追溯：模型目录同步复用公开 ID 校验；探测在验证终态后立即结束，不等 EOF；诊断以私有目标快照核对样本归属。管理状态报告目录发布 pending，并以独立的 `/api/catalog/retry` 重试文件副作用。前端保存／默认／组操作的 pending 状态与草稿版本独立于 DOM，刷新不会解除单飞；测速结果身份覆盖凭据变更，但不将凭据签名显示到页面或持久化。

## 生产边界与平台限制

- 当前发布支持 **Linux/WSL**；进程锁和安全文件操作依赖 POSIX 行为，不宣称原生 Windows/macOS 支持。
- 只接受当前 version 6 配置，不迁移旧配置。不得让新二进制覆盖无法识别的配置。
- Codex 与 OpenCode 配置更新采用合作式锁、复核及原子替换；不保证与任意不合作编辑器之间的原子 CAS。最终替换窗口内避免外部并发编辑。
- `package.json`、`package-lock.json` 和 Node 脚本仅供 Playwright/官方 Codex 开发验收；没有 npm runtime dependencies，也没有 npm launcher。

## 功能对照与真实差异

本次切换没有主动省略原来面向用户的管理、代理、诊断、适配、Codex 或 OpenCode 同步功能。原 JS 源码在验收后删除，行为参照保存在外部基线 `/tmp/opencode/adroute-go-rewrite/reference`，本仓库行为由 Go 单测及实际 Go 二进制黑盒测试维护。

明确限制（不是隐藏的等价承诺）：

- 只支持 Linux/WSL；没有跨平台的 Windows/macOS 守护进程/文件安全实现。
- 不迁移旧配置或旧 Codex 恢复日志，也不支持旧路由别名、旧模型 ID 或旧供应商/Key 结构。
- Codex 客户端配置接入/恢复按官方 CLI 0.156.0 的配置形态设计；其他版本不承诺兼容。没有订阅/设备登录链路。
- OpenCode 同步要求现存且结构兼容的 V2 Responses `providers.adroute`；不会猜测、创建或重启用户配置/服务。
- Responses 兼容包只处理明确绑定的 v1/v2/v3 已声明流形态；Chat Completions 转换、未观测协议异常、真实服务端全量行为不在保证范围。
- 不对任意外部编辑器提供最后写入竞争窗口的原子 compare-and-swap；遇到可检测变化时关闭写入并保留备份。
