# 模型组与模型管理重构：实现报告

> 本文记录最初 0.7.0 的实现者报告，其“已通过”不代表后续独立审查无问题。0.7.1 的修复与重新验证单独记录在 [审查修复报告](model-management-review-fixes.md)，不要把本文历史测试数当成当前验收结果。

状态：已实现并通过本轮验证。配置格式 `version 5`，服务版本 `0.7.0`。
执行依据：`docs/model-management-implementation-prompt.md`；交互依据：`docs/model-management-design.md`。

## 1. 主要文件变化

### 重写（新数据结构与新流程）

| 文件 | 变化原因 |
|---|---|
| `internal/adroute/types.go` | 用 `State{Groups,Models,DefaultModelID}` 取代 `Providers/Keys/Active`；新增 `Group`、`Model`、`GroupInput`、`ModelInput`、`DiscoverInput`、`Route`（含 `RouteID/GroupName/ModelName`）；删除 `Provider/Key/Entry/Active/ProviderInput/KeyInput/Tokens.PublicState` 等旧结构；配置版本常量为 `ConfigVersion = 5`。 |
| `internal/adroute/validation.go` | 组名 `^[a-z0-9]+(?:-[a-z0-9]+)*$`、模型名称 `^[A-Za-z0-9._-]+$`、真实 ID、Base URL、凭据、组内唯一性、默认模型引用等校验；保留原有适配包（v1/v2/v3）严格字段校验。 |
| `internal/adroute/store.go` | 配置文件名 `providers.json` → `config.json`；组/模型为唯一权威数据；`ResolveState` 只接受 `adroute/<组>/<名称>` 与 `adroute/default`；目录与 `/v1/models` 的 `id` 即对外 ID；删除 `PublicState`、`GetProvider`、`Activate`、`EntryRoute`、`KeyRoute`、`AddSubscriptionModel` 等旧入口。 |
| `internal/adroute/manage.go` | 组与模型 CRUD、开放状态、默认、修复绑定的单事务实现；重名/非法/不存在返回稳定错误码；`UpdateModel` 在 `disabled` 缺省时保留服务器当前状态。 |
| `internal/adroute/routes.go` | 只有 `PublicModelID` / `ParsePublicModelID` / `DefaultRouteID`；不再有 `ModelEntry`、`ClientRouteID` 或按 provider 拼接。 |
| `internal/adroute/http_management.go` | 面向组/模型的管理 API、`/api/default`、`/api/models/discover`、`/api/models/:id/discover`、`/api/models/:id/quick-test`、`/api/models/:id/tool-structure-test`、诊断与选择接口改用 `modelId`。 |
| `internal/adroute/upstreams.go` | 删除订阅分支与 `UpstreamAuth`；新增 `Discover`、`QuickTest`、`ToolStructureTest`、`readSSEStream`、`responseText`、`usageOutputTokens`。 |
| `internal/adroute/http_proxy.go` | 只走条目自己的上游连接；请求记录字段改为 `modelId/routeId/groupName`；删除订阅刷新逻辑。 |
| `internal/adroute/diagnostics.go`、`adapter_directory_state.go` | 诊断与适配绑定身份由 `providerId/entryId/keyId` 改为 `modelId`（+ `routeId`），Prompt 目标与绑定列表同步。 |
| `public/index.html`、`public/controller.js`、`public/compatibility-directory.js`、`public/styles.css` | 首页改为模型管理（紧凑本地接入区、组筛选、每行开放开关 + 编辑/测速/删除、行内测速结果、次级默认与接入）；兼容面板改为按模型绑定 + 工具结构测试；移动端三按钮可见、无横向溢出。 |
| `browser/go-fixture.js`、`browser/go-acceptance.test.js`、`browser/availability.test.js` | seed 与辅助函数改为创建组/模型；断言改为新 UI 与新 API；新增快速测速、单飞、取消、陈旧草稿等覆盖。 |
| `integration/go-codex.test.js` | 使用 `goSavedModel` 与 `/api/default`；`/model` picker 用例改为“同一真实模型 ID 的两个条目”；兼容绑定改用 `modelId`。 |
| `internal/adroute/store_test.go`、`availability_test.go`、`http_test.go`、`http_proxy_test.go`、`final_regression_test.go`、`compatibility_test.go` | 迁到新数据结构；新增组/模型生命周期、唯一性并发、默认与开放、目录、快速测速、工具结构测试等覆盖。 |
| `cmd/adroute/main.go`、`cmd/adroute/main_test.go` | 去掉订阅 helper 的启动/关闭接线；测试配置路径改为 `config.json`。 |

### 新增

| 文件 | 内容 |
|---|---|
| `internal/adroute/quicktest_test.go` | 快速测速与工具结构测试的完整行为覆盖（SSE/JSON、用量、速率、截断、超时、取消、不变量）。 |
| `docs/model-management-implementation-report.md` | 本报告。 |

### 删除

| 文件 / 内容 | 原因 |
|---|---|
| `internal/adroute/slug.go` | 拼音 slug、自动 `-2/-3` 序号、第三别名字段全部取消；`github.com/mozillazg/go-pinyin`、`golang.org/x/text` 依赖随之移出 `go.mod`（`go mod tidy`）。 |
| `internal/adroute/codex_auth.go`、`codex_auth_test.go` | 仅服务“ChatGPT 订阅模型来源”的官方 CLI helper/RPC 链路（`Status/Models/Credentials`）；产品链路已取消。已核对调用关系：除订阅 API 与 `Upstreams` 订阅分支外无其他使用者。 |
| 订阅相关管理接口与 UI | `/api/subscription/status`、`/api/providers/chatgpt/models*`、订阅登录指引/订阅模型弹窗等。 |
| 旧 provider/key API 与前端流程 | 不保留过渡层；仓库内前端、测试、文档一并切换。 |

## 2. 最终合同

### 数据结构（`ADROUTE_HOME/config.json`，version 5）

```json
{
  "version": 5,
  "revision": 12,
  "groups": [{ "id": "<uuid>", "name": "work" }],
  "models": [{
    "id": "<uuid>", "groupId": "<uuid>", "name": "coding",
    "model": "vendor/model", "baseUrl": "https://example.invalid/v1",
    "apiKey": "<上游密钥>", "disabled": false, "profile": "none"
  }],
  "defaultModelId": "<uuid|null 省略>",
  "adapters": []
}
```

- 组只保存身份与组名；模型拥有自己的地址、凭据与真实 ID；`profile` 是逐条目修复绑定。
- 上限：组 ≤ 100，模型 ≤ 2000，组名 ≤ 48 字节，模型名称 ≤ 100 字节，真实 ID ≤ 256 UTF-16 单位，密钥 ≤ 16384 UTF-16 单位。
- 未知字段、`null` 布尔、未知版本一律拒绝；不认识的文件原样保留并报错。

### 管理 API

`GET /api/state`、`GET /api/setup`、`POST|PUT|DELETE /api/groups[/:id]`、`POST /api/models`、`PUT|DELETE /api/models/:id`、`PUT /api/models/:id/availability`、`POST /api/default`、`POST /api/models/discover`、`POST /api/models/:id/discover`、`POST /api/models/:id/quick-test`、`POST /api/models/:id/tool-structure-test`、`POST /api/compatibility/{diagnosis,prompt,replay,refresh,packages,verify,template,remove}`、`PUT /api/compatibility/selection/:modelId`、`POST /api/opencode/sync`。

请求/响应字段与错误码见 [`docs/api-services.md`](api-services.md)。

### 公开路由与模型目录

- 唯一对外 ID：`adroute/<组名>/<模型名称>`；目录 `id` 即该值，`name/display_name` 为 `<组名> / <模型名称>`。
- 只列开放模型；无开放默认时不含 `adroute/default`。
- 真实模型 ID、UUID、旧别名都不会被接受为路由；未知 404，已关闭 403 `MODEL_ROUTE_DISABLED`，均不触发上游请求。

## 3. 被删除的订阅链路与保留的客户端能力

删除：`codex_auth.go` 全文件（设备登录/凭据 RPC/订阅模型列表）、`UpstreamAuth` 接口、`Upstreams` 与 `http_proxy.go` 中的订阅分支、`HTTPOptions.Auth`、main.go 中的 helper 生命周期、订阅相关管理 API 与前端弹窗。

保留且未削弱：

- Codex 配置接入与恢复：`codex_config.go` 全部逻辑、`adroute start` 自动接入、`adroute stop` / `adroute codex restore`、合作式锁、逐字段恢复、备份与最终复核。测试：`task3_test.go` 的 Codex 用例、`cmd/adroute/main_test.go`。
- Codex 模型目录发布（`ADROUTE_HOME/codex/models.json`）与 `adroute/default`。
- OpenCode 显式 JSONC 同步与安全写文件（`opencode_sync.go` + `jsonc.go`）。
- 管理令牌/代理令牌分离、Host/Origin 校验、秘密脱敏、私有权限、请求快照、流式/取消/超时/资源上限、v1/v2/v3 修复与诊断隐私。

## 4. 名称、唯一性、编辑、开放/默认、快照与旧 ID

- 组名：小写字母/数字/单连字符，全局唯一；中文、空格、下划线、斜杠直接 400 `GROUP_NAME_INVALID`，不转换、不裁剪。
- 模型名称：`[A-Za-z0-9._-]+`，同组精确且区分大小写唯一；不同组可同名；已关闭条目仍占名。重名 409 `MODEL_NAME_CONFLICT`，不追加序号。
- 新建时前端把真实 ID 填入名称（未手动修改前）；非法（如 `vendor/model`）就地提示并要求手改；用户改过名称后不再被真实 ID 输入、发现结果或刷新覆盖；编辑已保存模型时改真实 ID 不改名称。
- 真实模型 ID 原样转发（允许 `/` 与大小写），不受名称字符规则限制。
- 只改地址/Key/真实 ID：对外 ID 与内部条目 ID 均不变，后续请求使用新配置；其他条目不受影响（同真实 ID 的不同条目各有凭据）。
- 改名/换组/改组名：对外 ID 改变，保存前需确认；旧 ID 不映射、不回退。移动时若目标组重名 → 409，不自动改名。
- 开放状态：新建默认开放（表单可先关闭）。关闭只影响外部发现与调用；管理页仍可编辑、显式测速、绑定修复。
- 默认：只能选开放条目；设默认不改变其他条目状态；关闭当前默认在一次事务中清空默认；重新开放不恢复默认；删除默认条目清空默认。
- 快照：`proxy()` 在读取 body 前取快照；已接收请求用旧快照完成，新请求立即用新状态。
- 陈旧草稿：`PUT /api/models/:id` 省略 `disabled` 时保留服务器当前状态，因此旧表单不会重新开放已被关闭的条目。

## 5. 快速测速

| 项 | 取值 |
|---|---|
| 提示词 | `Reply with one short sentence confirming you can answer.`，instructions `Answer in plain text, briefly. Do not call tools.` |
| 输出上限 | `max_output_tokens = 192`（`QuickTestMaxOutputTokens`） |
| 总超时 | `18s`（`QuickTestTimeout`），从 HTTP 请求 context 派生，单次调用一个绝对截止时间 |
| 请求数 | 每次确认恰好一次，不重试、不批量、不在页面打开/保存/刷新时触发 |
| 成功判定 | 结构有效的 `response.completed` / `response.incomplete` 终端事件 + 非空文本；200/`[DONE]`/空 output/仅 reasoning/仅工具调用均不算 |
| usage 来源 | 终端快照 `response.usage.output_tokens`，只读一次（不用 delta 或事件数累计） |
| 速率 | `output_tokens / 本次请求总秒数`（后端单调计时，含等待；页面显示“约 XX token/s”） |
| 缺失/非法 usage | `outcome = no-usage`，不输出速度字段；负数/非整数/≤0 视为未报告，绝不产生 NaN/Infinity/负值 |
| 截断 | `status=incomplete` 且 `reason=max_output_tokens` → `outcome=truncated`，文案“已返回文本，达到本次测试输出上限”，不当作完整成功 |
| 取消传播 | 前端 AbortController → 请求断开 → 服务端 `r.Context()` 取消 → 上游请求 context 取消并关闭响应体；返回 499 `TEST_CANCELLED` |
| 状态副作用 | 无：不开放模型、不改默认、不改绑定、不写客户端配置 |

`outcome` 取值：`ok` / `no-usage` / `truncated` / `no-text`。错误：504 `UPSTREAM_TIMEOUT`、499 `TEST_CANCELLED`、502 `RESPONSES_TEST_FAILED`、502 `INVALID_RESPONSES_STREAM`。

## 6. 测试与 RED → GREEN 证据

新增/重写的 Go 测试（仓库共 136 个 `Test` 函数，另有大量子测试）：

- `store_test.go`：组/模型生命周期与非法名称、唯一性（含 16 路并发同名只成功一次）、真实 ID 编辑保持身份、默认与开放规则、目录不含凭据、关闭模型退出目录、适配引用、原子提交失败/目录发布失败可重试、严格解码、权限与符号链接。
- `availability_test.go`：关闭路由与默认不回退、快照不可变、关闭路由在上游前拒绝、严格请求体与令牌隔离、编辑不重新开放、目录文件随开放状态更新与重启、OpenCode 显式同步只写开放模型、`/api/state` 形状与能力位。
- `quicktest_test.go`：SSE/JSON 成功与速率公式、missing usage、非文本与错误结果矩阵、任意分块与 CRLF、`incomplete` 截断、超时/取消/客户端断开到达上游、单次请求无重试、不改状态、非法 usage 不产生 NaN、工具结构测试。
- `http_test.go`：组/模型 CRUD 形状、发现使用未保存密钥且不落盘、错误码与严格字段、默认端点、兼容同意与选择、shutdown 身份、Host/Origin 与令牌分离、压缩请求体。
- `http_proxy_test.go`：原生流字节透传与请求头白名单、默认路由与非法 `model`、控制字符、慢上传快照、错误状态与脱敏、header/idle 超时、压缩体、客户端取消、v1/v2/v3 修复、同真实 ID 不同凭据、自指与重定向。
- `compatibility_test.go` / `http_review_test.go` / `task3_test.go` / `final_regression_test.go`：保留并调整绑定身份、诊断同意与隐私、回放、Codex 配置/恢复、OpenCode 同步与文件安全。

代表性 RED → GREEN（实际输出）：

- 结构迁移后先跑测试：`vet: internal/adroute/http_test.go:21:23: undefined: CodexStatus`、`undefined: itoa`、`route.Type undefined` —— 证明测试先于实现收敛到新结构。
- `TestManagementCompatibilityConsentAndLimits`: `prompt: 500 {"error":{"code":"PROMPT_UNAVAILABLE"...}}` → 修正测试的 `PromptRoot`/`Assets` 后 200。
- `TestStrictDecodeRejectsUnknownFields`: `null default id accepted` → `State.UnmarshalJSON` 显式拒绝 `null`。
- `TestTokenValidationAndUnsafeConfigPath`: `unknown token field accepted` → token 文件改用严格解码。
- `TestEmptyArraysAndPrivateDirectoryModes`: `groups are not an array: {... "groups":null ...}` → `cloneState` 保留非 nil 空切片。
- 浏览器：`a stale edit draft cannot reopen a model the server closed meanwhile` 首次失败于表单仍打开（重命名触发确认）→ 改为“仅改连接配置”的正确用例后通过；`late state refresh cannot overwrite a newer saved edit` 首次失败于重命名未确认 → 补确认后通过；快速测速用例首次因合成上游未发信号而超时 → 修正测试信标后通过。

删除/替代的测试：

- 订阅相关：`TestSubscriptionRoutesLazyInjectedAuth`、`TestClosedSubscriptionActivationDoesNotRequestCredentials`、`TestDisabledSubscriptionFixedRouteRejectsBeforeBroker`、`TestSubscriptionProxyRefreshBoundaryOfficialRouteAndErrorNoRefresh` —— 功能已取消，删除。
- 拼音/别名：`TestSlugOracleCases`、`TestSlugEditingAndCatalogContract` —— 拼音与第三别名功能取消，等效用例为对外 ID 生成/改名失效测试。
- provider/key 用例：`TestProviderAndKeyLifecycle`、`TestPerKeyRouteIdentityAndProviderNamespace`、`TestDuplicateConstraintsAndFrozenRouteIdentity`、`TestKeyEditCanDisableActiveEntryAtomicallyAndPreservesOtherDefault` —— 改写为组/模型生命周期、名称唯一性、同真实 ID 独立凭据、默认与开放规则等用例。
- 其余安全、配置恢复、流式协议、工具修复、默认/开放、官方 Codex 工具往返覆盖全部保留。

## 7. 最终验证

日志与截图目录：`/tmp/opencode/adroute-mm-verify/`、`/tmp/opencode/adroute-mm-screens/`。

| 命令 | 退出码 | 结果 |
|---|---|---|
| `go test ./...` | 0 | 4 个包全部 ok，0 失败，0 skip |
| `go test -race -count=1 ./...` | 0 | 4 个包全部 ok，0 失败 |
| `go vet ./...` | 0 | 无输出 |
| `npm run check` | 0 | “Go-backed acceptance and frontend JavaScript syntax checks passed.” |
| `make build` | 0 | 生成 `bin/adroute`（嵌入 public/examples/prompt） |
| `npm run test:browser` | 0 | 16 通过 / 0 失败 / 0 skip（`availability.test.js` 6 + `go-acceptance.test.js` 10） |
| `npm run test:codex` | 0 | 6 通过 / 0 失败 / 0 skip（官方 `codex-cli 0.156.0`，未跳过） |

交接基线曾为 16 项浏览器 + 6 项 Codex 通过；本轮保持同等入口并新增了快速测速、取消、单飞、陈旧草稿、并发唯一性、工具结构测试等用例。

截图（均为合成数据与虚构地址/密钥）：

- `01-model-list-desktop.png`：模型列表（组筛选、开放开关、三按钮、默认标记）
- `02-quick-test-confirm.png`：测速用量确认
- `03-quick-test-result.png`：行内测速结果（“可用 · 约 188.2 token/s · 24 output tokens”）
- `04-edit-form.png`：编辑表单（密钥留空保留、隐藏开放开关）
- `05-add-form.png`：添加表单（组选择 + 内联建组 + 真实 ID/名称分离）
- `06-model-list-mobile.png`：390×844 手机布局（三按钮可见、无横向溢出）
- `07-quick-test-mobile.png`：手机测速确认

## 8. 边界确认

- 未读取、未展示、未使用任何真实上游密钥；未向真实付费模型发送请求（全部为 loopback 合成上游）。
- 未执行 `adroute stop/start` 于用户运行中的服务，未执行 `./install.sh`、`make install`、全局安装，未修改任何 shell 启动文件。
- 未修改用户真实 `config.json`、`~/.config/adroute`、`CODEX_HOME` 或 OpenCode 配置；所有测试使用 `mktemp` 隔离目录与隔离端口。
- 未做 Git 提交、推送、分支或 reset；仓库仍为“无提交的 main + 未跟踪源码”状态。
- 临时产物仅在 `/tmp/opencode/adroute-mm-*` 与测试自建 `mktemp` 目录内，测试结束自行清理。
- 仓库内一次性截图脚本已在生成截图后删除，未留在源码树。
- 唯一非功能副作用：`go.mod` / `go.sum` 移除了 `go-pinyin` 与 `golang.org/x/text`（`go mod tidy`），`ServiceVersion` 与 `package.json` 版本提升到 `0.7.0`。

## 9. 未完成项、限制与偏差

未验证 / 明确限制：

- 未在真实上游（OpenAI 兼容服务、真实供应商）上验证 Responses 行为；快速测速只在合成 SSE/JSON 上游上验证。
- 未验证真实 Codex/OpenCode 客户端之外的其他客户端刷新目录行为；页面明确只承诺“更新目录并提示刷新”，不承诺自动刷新。
- 未实现、且按本轮要求不实现：Chat Completions/Anthropic 转换、凭据池/轮换/故障转移、多级嵌套组、标签、权限体系、测速历史与排行榜、前端框架迁移。
- 无跨平台（原生 Windows/macOS）守护进程与文件安全实现，仍仅 Linux/WSL。
- 旧的 version 4 配置不会被读取或迁移；启动会报错并保留原文件（这是本轮明确要求，不是缺陷）。

与 Prompt 的偏差（如实说明）：

1. 测速实现为“一次请求 + 结构判定”，未对 `response.incomplete` 的非 `max_output_tokens` 原因单独建模，统一按“未返回完整文本”或“已返回文本但提前结束”呈现。
2. 名称自动填写与“是否已被用户修改”的判定在前端完成；后端只做严格校验与冲突拒绝（Prompt 未要求后端推断用户意图）。
3. 为编辑态“获取模型”新增了 `POST /api/models/:id/discover`：表单密钥留空时使用该条目已保存的凭据，输入新值则优先使用新值。这是新增的便捷路径，未改变“不能用旧凭据冒充新草稿”的语义。
4. 测试驱动顺序并非所有用例都严格“先写测试再写实现”：核心数据/路由/测速按 RED → GREEN 推进（见第 6 节的真实失败输出），前端与部分迁移用例是在实现后补齐并以失败→修复方式收敛。没有删除断言、skip 或用放宽断言换绿灯。
5. `docs/complexity-review.md` 是历史评审文档，本轮只加了时间线说明与少量措辞修正，没有重写其结论。
