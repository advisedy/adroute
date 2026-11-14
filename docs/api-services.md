# 模型组、模型条目与管理 API

状态：当前配置 version 6，首个公开发布版本 0.1.0。旧“供应商 → Key → 模型”结构、独立 Key 管理、路由别名与订阅模型来源均已移除，没有兼容层。

## 数据结构

`ADROUTE_HOME/config.json` 是唯一权威配置：

```json
{
  "version": 6,
  "revision": 12,
  "groups": [{ "id": "6f1d0b7c-...", "name": "work" }],
  "models": [
    {
      "id": "c0d4f0a2-...",
      "groupId": "6f1d0b7c-...",
      "name": "coding",
      "model": "vendor/model",
      "baseUrl": "https://example.invalid/v1",
      "apiKey": "upstream-secret",
      "disabled": false,
      "profile": "none"
    }
  ],
  "aggregates": [
    {
      "id": "9a7c5e21-...",
      "name": "coding-failover",
      "members": ["c0d4f0a2-..."],
      "maxRounds": 5,
      "disabled": false
    }
  ],
  "defaultModelId": "c0d4f0a2-...",
  "adapters": []
}
```

- `groups[].id` 与 `models[].id` 是稳定内部标识（UUID），永不作为对外模型 ID 暴露。
- 组保存身份、组名与可选 `archived` 收纳标记，不保存 URL 或凭据。
- 模型与聚合支持可选布尔 `favorite`、`archived`，缺省为 false；旧 version 6 配置无需迁移。收纳标记会保留于编辑、复制、重启和配置导入导出，但不参与目录发布筛选、对外权限、默认选择或内部路由。整组归档由管理界面继承显示，不修改子模型的 `archived`。
- 每个模型必须属于且只属于一个组，并拥有自己的上游 Base URL、上游 API Key 与真实模型 ID。
- `profile` 是逐条目的兼容修复绑定（`none` 或 `包ID@版本`），不继承组设置。
- `aggregates[]` 是按顺序尝试的故障转移链；`members` 存模型条目 id，1~8 个且不重复，`maxRounds` 1~10（默认 5）。
- `disabled: true` 表示已停用；`exposureDisabled: true` 表示仅关闭对外开放，二者缺省均为 false。对外关闭不影响内部测速或作为聚合成员使用。`defaultModelId` 可指向未停用的模型条目或聚合，为空表示没有网页默认。
- 只接受 version 6。损坏或不支持的配置会报错并保留原文件，不会写回空配置或自动迁移。

### 推理档位

adroute 对外发布一份固定的推理档位表 `low / medium / high / xhigh / max`（`internal/adroute/reasoning.go` 的 `ReasoningLevels`）。Codex 模型目录把它写成 `supported_reasoning_levels`，OpenCode 同步把它写成每个模型的 `variants`；两个客户端形状不同、内容永远一致。默认档位为 `none`，即不发送 `reasoning.effort`。

档位是默认菜单而非能力声明：adroute 不按上游校验，客户端发来的 `reasoning.effort` 原样透传。上游不接受某个档位时返回自己的错误，不换成员也不重试。

### 长度与数量上限

| 项 | 规则 |
|---|---|
| 组名 | `^[a-z0-9]+(?:-[a-z0-9]+)*$`，≤ 48 字节 |
| 模型名称 | `^[A-Za-z0-9._-]+$`，≤ 100 字节 |
| 真实模型 ID | 非空、无控制字符、无首尾空白，≤ 256 UTF-16 单位 |
| 上游 Base URL | http/https，无凭据/查询/片段，≤ 2048 UTF-16 单位，不能以 `/responses`、`/models`、`/chat/completions` 结尾 |
| 上游 API Key | 1–16384 UTF-16 单位，不含控制字符或空格 |
| 组数量 | ≤ 100 |
| 模型数量 | ≤ 2000（全局） |

### 唯一性

- 组名全局唯一，精确匹配。
- 同一组内模型名称唯一，精确且区分大小写；不同组可以同名。
- 已关闭的模型仍占用组内名称。
- 唯一性在持久化事务内校验；并发创建同名模型只有一个成功。普通新建重名返回 `MODEL_NAME_CONFLICT`，不自动追加序号；复制模型时由服务端在事务内生成不重名的 `-copy`、`-copy-2` 等名称。
- 同一组允许真实模型 ID 相同、模型名称不同的两个条目，它们各有自己的地址与凭据。

### 对外模型 ID

```text
adroute/<组名>/<模型名称>
adroute/agg/<聚合名称>
```

- `GET /v1/models` 的 `id` 就是该值；`name` / `display_name` 是 `<组名> / <模型名称>` 或 `聚合 / <聚合名称>`。
- `agg` 是保留的聚合命名空间；普通组名不会与之冲突。
- 客户端目录文件（`ADROUTE_HOME/codex/models.json`）与 OpenCode 显式同步使用同一规则。
- 原始上游模型 ID、UUID 和任意第三别名都不会被接受为路由。
- 改名／换组／改组名会改变对外 ID；旧 ID 不映射、不跳转、不回退。

## 管理 API

全部要求 `Authorization: Bearer <管理令牌>`、本机 Host/Origin 校验与 `Cache-Control: no-store`。代理令牌只能访问 `/v1/*`，不能读写配置。除注明外，成功响应都是 `200/201` + 完整管理状态（见下节）。

| 方法与路径 | 请求体 | 说明 |
|---|---|---|
| `GET /api/state` | — | 管理状态 |
| `GET /api/setup` | — | 本地代理地址、代理密钥、Codex 配置预览 |
| `GET /api/clients` | — | 支持客户端的接入状态：`id`、`name`、`enabled`、`available`、`stale`、`configPath`、`detail` |
| `POST /api/clients/{codex,opencode}/enabled` | `{enabled: bool}` | 打开即接入、关闭即恢复/清空；返回最新客户端列表 |
| `POST /api/clients/{codex,opencode}/refresh` | `{}` | 用当前模型目录重写该客户端的 AdRoute 部分 |
| `POST /api/clients/refresh` | `{}` | 刷新所有已接入的客户端 |
| `POST /api/catalog/retry` | 空体 | 重试发布 Codex 模型目录，不重新保存模型；失败 503 `CATALOG_PUBLICATION_FAILED` |
| `POST /api/groups` | `{name}` | 新建组，201 |
| `PUT /api/groups/:id` | `{name}` | 改组名 |
| `DELETE /api/groups/:id` | — | 删除空组；非空返回 409 `GROUP_NOT_EMPTY` |
| `POST /api/models` | `{groupId,name,model,baseUrl,apiKey,disabled?}` | 新建模型，201 |
| `POST /api/models/:id/copy` | 空体 | 同组复制模型并自动编号，201；名称超过长度限制时返回 `MODEL_NAME_INVALID` |
| `PUT /api/models/:id` | `{groupId,name,model,baseUrl,apiKey?,disabled?,profile?}` | 编辑模型 |
| `DELETE /api/models/:id` | — | 删除模型；仍被聚合引用返回 409 `MODEL_IN_USE` |
| `PUT /api/models/:id/availability` | `{enabled}` | 原有启用／停用，严格布尔；停用影响聚合成员并清空默认 |
| `PUT /api/models/:id/exposure` | `{enabled}` | 对外开放／关闭，严格布尔；不影响内部可用性与默认选择 |
| `PUT /api/exposure/models` | `{enabled}` | 模型页的整页开关：所有未停用模型一次改完，严格布尔 |
| `PUT /api/exposure/aggregates` | `{enabled}` | 聚合页的整页开关：所有未停用聚合一次改完，严格布尔 |
| `PUT /api/models/:id/params` | `{contextWindow}` | 只写上下文窗口；0 表示不设置。窄接口，不触碰分组、地址、凭据或开放状态 |
| `POST /api/aggregates` | `{name,members,maxRounds?,disabled?}` | 新建聚合，201 |
| `PUT /api/aggregates/:id` | `{name,members,maxRounds?,disabled?}` | 编辑聚合 |
| `DELETE /api/aggregates/:id` | — | 删除聚合 |
| `PUT /api/aggregates/:id/availability` | `{enabled}` | 原有启用／停用，严格布尔 |
| `PUT /api/aggregates/:id/exposure` | `{enabled}` | 对外开放／关闭，严格布尔；不改变成员状态 |
| `PUT /api/groups/:id/organization` | `{archived}` | 仅收纳分组，不批量改写成员标记 |
| `PUT /api/models/:id/organization` | `{favorite?,archived?}` | 至少一个严格布尔字段；未提供字段保持原值 |
| `PUT /api/aggregates/:id/organization` | `{favorite?,archived?}` | 同上；不改变成员、开放状态或默认 |
| `POST /api/default` | `{modelId}`（显式 `null` 或空串清空） | 字段不可省略；可选未停用模型或聚合 |
| `POST /api/models/discover` | `{baseUrl,apiKey}` | 用未保存的地址与密钥获取模型列表，不保存 |
| `POST /api/models/:id/discover` | `{baseUrl?,apiKey?}` | 同上；省略的字段使用该条目已保存值 |
| `POST /api/models/:id/quick-test` | 空体 | 快速测速，见下节 |
| `POST /api/models/:id/tool-structure-test` | 空体 | 高级工具流结构测试（需确认用量） |
| `PUT /api/compatibility/selection/:modelId` | `{profile}` | 必填非空字符串，显式 `none` 关闭；缺失／null 不会解除绑定 |
| `POST /api/compatibility/diagnosis` | `{modelId,raw?,consent?}` | 采集下一次匹配请求 |
| `POST /api/compatibility/prompt` | `{modelId?,includeDiagnosis?,diagnosisId?}` | 生成排查 Prompt |
| `POST /api/compatibility/replay` | `{ref,events}` | 本地回放 |
| `POST /api/opencode/sync` | `{configPath?}` | 显式同步 OpenCode，见 [同步说明](opencode-sync.md) |

模型和聚合的创建/编辑均接受可选严格布尔字段 `exposureDisabled`。`POST /api/models` 要求必要连接字段；`PUT /api/models/:id` 省略 `apiKey` 或传空串表示保留已保存的密钥，省略 `disabled`、`exposureDisabled` 分别保持当前停用、对外开放状态（因此陈旧草稿不会重新开放条目）。`/api/state` 返回这两个字段，`open` 为未停用且对外开放；`capabilities.modelExposure` 表示支持独立对外开放接口，`capabilities.exposureBulk` 表示支持整页批量开关（`/api/exposure/models`、`/api/exposure/aggregates`）。两个批量接口各自只碰自己那一类条目，在 Store 锁内一次提交：要么全部改完，要么一个都不改，只增加一个 revision，也只重发一次客户端目录。已停用的条目在两个方向上都跳过——它们本来就不可达，静默预置「对外关闭」会在日后恢复启用时表现为一个无从解释的关闭状态。目标状态与现状一致时不写盘、不递增 revision。

`contextWindow` 是可选的客户端提示，单位为 token，`0`／省略表示不发布。非零值必须在 1024~10000000 之间，接口返回 400 `CONTEXT_WINDOW_INVALID`，手改配置文件也会在加载时被同一规则拒绝。它只写入 Codex 目录的 `context_window` 与 OpenCode 的 `limit.context`；adroute 不会据此裁剪历史、拒绝请求或校验上游能力，窗口值只影响客户端本地压缩时机与用量百分比。聚合模型只有在所有成员都填了值时才发布，取其中最小值；任一成员为空则整个聚合不发布。`adroute/default` 发布其解析目标的窗口值。窄接口 `PUT /api/models/:id/params` 只接受 `contextWindow`，不接受其他字段。

`capabilities.organization` 表示支持收纳接口。接口拒绝空对象、null、非布尔值和未知字段；分组不接受 `favorite`。收纳写入使用与模型编辑相同的原子持久化与 revision 边界。`/api/state` 在模型、聚合中返回显式 `favorite`、`archived`，在组中返回 `archived`；模型 `archived` 是自身标记，管理界面还需叠加所属组的标记。

复制模型使用服务端保存的最新连接、修复绑定、开放及收纳状态，不改变默认模型；每个副本独立保存凭据与配置，不复制页面内的测速结果。

管理请求顶层字段不接受 null，只有 `/api/default` 的 `modelId` 允许显式 null 作为清空命令。`apiKey:null` 不是“留空保留”，会被拒绝；非法请求不修改配置。名称按用户原输入校验，首尾空白也不裁剪后保存。

稳定的错误码：`GROUP_NAME_INVALID`、`GROUP_NAME_CONFLICT`、`GROUP_NOT_FOUND`、`GROUP_NOT_EMPTY`、`MODEL_NAME_INVALID`、`MODEL_NAME_CONFLICT`、`MODEL_UPSTREAM_ID_INVALID`、`UPSTREAM_KEY_INVALID`、`MODEL_NOT_FOUND`、`MODEL_IN_USE`、`MODEL_ROUTE_DISABLED`、`MODEL_ROUTE_NOT_FOUND`、`AGGREGATE_NAME_INVALID`、`AGGREGATE_NAME_CONFLICT`、`AGGREGATE_NOT_FOUND`、`AGGREGATE_MEMBERS_INVALID`、`AGGREGATE_MEMBER_NOT_FOUND`、`AGGREGATE_ROUNDS_INVALID`、`AGGREGATE_LIMIT_REACHED`、`AGGREGATE_DISABLED`、`AGGREGATE_EMPTY`、`AGGREGATE_EXHAUSTED`、`AGGREGATE_TIMEOUT`、`INVALID_MODEL`、`NO_DEFAULT_MODEL`、`PROXY_LOOP`、`UPSTREAM_TIMEOUT`、`TEST_CANCELLED`、`RESPONSES_TEST_FAILED`、`ADAPTER_NOT_FOUND`。

### 管理状态

```json
{
  "version": "0.1.0",
  "revision": 12,
  "configVersion": 6,
  "url": "http://127.0.0.1:3001",
  "proxyUrl": "http://127.0.0.1:3001/v1",
  "groups": [{ "id": "...", "name": "work", "modelCount": 2, "openCount": 1 }],
  "models": [
    {
      "id": "...",
      "groupId": "...",
      "groupName": "work",
      "name": "coding",
      "model": "vendor/model",
      "baseUrl": "https://example.invalid/v1",
      "apiKey": "upstream-secret",
      "disabled": false,
      "open": true,
      "profile": "none",
      "routeId": "adroute/work/coding"
    }
  ],
  "aggregates": [
    {
      "id": "9a7c5e21-...",
      "name": "coding-failover",
      "routeId": "adroute/agg/coding-failover",
      "members": [
        { "modelId": "c0d4f0a2-...", "groupName": "work", "name": "coding", "routeId": "adroute/work/coding", "disabled": false }
      ],
      "memberCount": 1,
      "openMemberCount": 1,
      "maxRounds": 5,
      "disabled": false,
      "open": true
    }
  ],
  "defaultModelId": "...",
  "defaultRouteId": "adroute/default",
  "capabilities": {
    "groups": true,
    "models": true,
    "aggregates": true,
    "modelAvailability": true,
    "modelExposure": true,
    "organization": true,
    "modelCopy": true,
    "quickTest": true,
    "quickTestTimeoutMs": 18000,
    "compatibility": true,
    "adapterDirectory": true
  },
  "requests": [],
  "catalogPublication": {"pending": false}
}
```

管理状态包含上游凭据（仅管理令牌可读）。公开目录、`/v1/models` 与 OpenCode 同步结果都不含凭据。

配置提交与模型目录文件发布不是跨文件原子事务。发布失败时，配置已经保存，状态的 `catalogPublication.pending` 为 true，附有不包含底层敏感错误的 `message`。网页展示警告及“重试发布目录”；处理目录／权限问题后仅重试发布，不重发保存，也不增加配置 revision。目录重试不会自动写 OpenCode 配置。

诊断采集与交接会核对条目当前上游连接身份（包括真实 ID、地址、凭据和绑定）。更换目标后，旧 capture 不会隐式附到新目标 Prompt；公开 ID 与条目内部 ID 不必为此改变。

## 公共模型目录与代理

- `GET /v1/models`（代理令牌）：`{"object":"list","data":[{"id":"adroute/work/coding","name":"work / coding","display_name":"work / coding","object":"model","owned_by":"work"}]}`。只列开放模型；无开放的默认时不含 `adroute/default`。
- `POST /v1/responses`（代理令牌）：`model` 字段缺失表示跟随 `adroute/default`；`null`、数字、空字符串返回 400 `INVALID_MODEL`；未知 ID 返回 404；已关闭 ID 返回 403 `MODEL_ROUTE_DISABLED`／`AGGREGATE_DISABLED`，且不会请求上游。
- 请求到达时固定路由快照：已接收请求可以完成，后续请求立即使用新状态。
- 关闭默认模型、删除模型或删除其组内唯一模型都会清空默认，不自动替换其他条目；重新开放不恢复默认。
- 上述清空规则仅适用于旧的停用操作。页面圆灯调用独立 `/exposure` 接口：关闭后从外部模型列表、Codex 目录和 OpenCode 显式同步中隐藏；直接请求返回 403 `MODEL_NOT_EXPOSED`／`AGGREGATE_NOT_EXPOSED`，不请求上游。默认选择保留，但其目标未对外开放时，外部 `adroute/default` 也被隐藏和拒绝；重新开放即可恢复默认访问。聚合内部仍可使用对外关闭但未停用的成员。
- 聚合按成员顺序依次尝试：连接失败、超时、429 与 5xx 换下一个成员；其余 4xx（确定性请求错误）立即透传，不换成员。成员全部轮完一轮后回到第一个成员重试，达到 `maxRounds` 轮后返回 `AGGREGATE_EXHAUSTED`（状态码取最后一次失败）。链头阶段总时长上限 13 分钟；一旦某个成员返回 2xx 响应头即提交，此后断流不重试。关闭的成员在请求时自动跳过；全部成员关闭返回 503 `AGGREGATE_EMPTY`。
- 等待上游响应头的超时为 90 秒：推理档位较高的请求本来就慢，响应头迟迟不来不代表成员不可用。超时只影响“连上了但一直不响应”的成员，连接失败仍是毫秒级失败。
- 成员在一次尝试中失败（连接失败、响应头超时、429 与 5xx）后进入 60 秒冷却：冷却期内它排在健康成员之后，但仍会被尝试，因此不会因为冷却返回 `AGGREGATE_EMPTY`。成员一旦成功即清除冷却，全部成员健康时仍严格按配置顺序，第一个成员保持优先。客户端取消（499）、提交后断流、以及 400 这类确定性请求错误都不计为成员不健康。

## 快速测速合同

`POST /api/models/:id/quick-test` 对该条目已保存的地址、密钥与真实模型 ID 发起**一次**短文本 Responses 请求：

- 固定提示词；`max_output_tokens = 192`；总超时 `18s`；同一请求使用同一个绝对截止时间，不随事件延长。
- 支持 SSE 与 JSON 响应；SSE 按行缓冲，任意字节分块与 `\r\n` 都能正确拼接。
- 成功只由“结构有效且状态一致的 Responses 终端事件 + 非空文本”决定；HTTP 200、`[DONE]`、缺状态／仍在运行、纯 reasoning/工具调用或空 output 都不算文本可用。失败事件立即失败，不能被后续完成事件覆盖。
- 有效终态到达立即结束并关闭响应体，不等连接 EOF；工具结构测试遵循相同结束原则。
- 速率 = 终端快照中可信的 `usage.output_tokens` ÷ 请求开始到有效结束的总秒数（含首段等待，不含完成后的连接空等）。缺失、为负、非整数、≤ 0 或超过本次 192-token 预算时不计算速率，也不显示 0/NaN/Infinity。
- `incompleteReason` 只返回已知协议标签（`max_output_tokens`／`content_filter`），不原样回显上游自由文本。达到上限与其他提前结束在界面中区别解释。
- 客户端取消或连接断开会让请求 context 取消，进而关闭上游响应体；不是只停止浏览器等待。
- 结果不改变开放状态、默认模型、修复绑定或客户端配置。

响应示例：

```json
{
  "outcome": "ok",
  "status": 200,
  "durationMs": 1270,
  "textReturned": true,
  "usageReported": true,
  "outputTokens": 24,
  "tokensPerSecond": 18.9,
  "truncated": false,
  "message": "可用。"
}
```

`outcome` 取值：`ok`（有文本且可算速率）、`no-usage`（有文本但无可信用量）、`truncated`（有效 `status=incomplete` 且已有文本，具体以 `truncated` 布尔值区分是否达到输出上限）、`no-text`（结构有效但没有文本）。超时返回 504 `UPSTREAM_TIMEOUT`，取消返回 499 `TEST_CANCELLED`，上游错误返回 502 `RESPONSES_TEST_FAILED`。

## 重新配置

如需重新配置，请先执行 `adroute stop`，把报错路径中的 `config.json` 移到其他位置留存，再运行 `adroute start`。服务会创建空配置，之后在网页重新创建组、添加模型并设置默认与修复包绑定。客户端需刷新列表并重新选择。保留的配置含明文凭据，不要分享。
