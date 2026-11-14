# OpenCode V2 模型目录同步

同步离线完成，由以下两个入口之一触发：管理页“客户端接入”里 OpenCode 那一行的开关与“刷新”按钮（走 `/api/clients/opencode/{enabled,refresh}`），或直接调用 `POST /api/opencode/sync`。两种入口都只在明确操作时写文件；保存模型条目不会自动改 OpenCode。它不连接 OpenCode、不启动/重启服务、不发现模型、不展开变量，也不读取凭据文件。

管理页以管理员认证调用 `POST /api/opencode/sync`，默认请求 `{}`。非标准目标必须显式提供 `{ "configPath": "/absolute/path/to/opencode.jsonc" }`。接口使用当前不含凭据的模型目录快照，不接受调用方提交的模型列表，并执行常规 Host/Origin 和管理令牌校验。

“客户端接入”的开关是页面上的主入口：打开即写入当前对外开放的模型，关闭即清空该模型映射并保留 provider、端点与凭据。刷新只用于修复已接入的客户端，对未接入的客户端返回 `409 CLIENT_NOT_ENABLED`，因此刷新不会绕过关闭状态。

## Go 模块与模型映射

实现位于 `internal/adroute/opencode_sync.go`（JSONC 字节范围解析、模型映射渲染与安全写文件）。HTTP 调用由 `internal/adroute/http_management.go` 接入，客户端开关与状态在 `internal/adroute/clients.go`。契约测试位于 `internal/adroute/opencode_config_test.go` 与 `internal/adroute/clients_test.go`；Go 二进制浏览器测试验证开关的单飞与错误呈现。

同步只替换 `providers.adroute.models`。每个条目包含 `name`、`modelID` 和固定的推理档位菜单：

```json
{
  "adroute/pixel/daily-coder": {
    "name": "Daily Coder",
    "modelID": "adroute/pixel/daily-coder",
    "variants": [
      { "id": "low", "settings": { "reasoningEffort": "low" } },
      { "id": "medium", "settings": { "reasoningEffort": "medium" } },
      { "id": "high", "settings": { "reasoningEffort": "high" } },
      { "id": "xhigh", "settings": { "reasoningEffort": "xhigh" } },
      { "id": "max", "settings": { "reasoningEffort": "max" } }
    ]
  }
}
```

模型来自 Store 当前目录快照。每个 `slug` 必须非空、唯一，且不含空白、控制字符或 `#`；`display_name` 必须是非空白字符串。`slug` 就是对外模型 ID `adroute/<组名>/<模型名称>`。允许重复展示名和空快照；空快照会清空该映射，已关闭模型不会写入。

每个模型可带可选的 `context_window`（token），只有大于 0 时才写成该条目的 `limit.context`；为 0 或缺失时整个 `limit` 都不写，让 OpenCode 用它自己的兜底值。adroute 不发布 `limit.output`，输出预算是 OpenCode 自己的设置。这个值只是客户端提示，adroute 不会据此裁剪历史或拒绝请求。

`variants` 是固定的五档菜单。OpenCode 把选中的档位作为 `reasoning.effort` 发给网关，adroute 原样转给上游。这些档位是**默认菜单，不是能力保证**：adroute 不知道每个上游模型实际接受哪些值，所以固定发布这五个通用档位。选到上游不认的档位会得到上游的错误，届时直接编辑该条目的 `variants` 即可。条目里已有的自定义档位会在下次同步时被这份菜单替换。

这份档位表与 Codex 模型目录的 `supported_reasoning_levels` 是**同一份**（`internal/adroute/reasoning.go` 的 `ReasoningLevels`），两个客户端渲染成各自的形状但内容永远一致，不会出现某个档位只对一个客户端可见。默认档位保持 `none`（不发送 `reasoning.effort`），发布菜单不会自动提高成本或延迟。

仅替换或添加 `providers.adroute.models`：

```json
{
  "adroute/pixel/daily-coder": {
    "name": "Daily Coder",
    "modelID": "adroute/pixel/daily-coder",
    "limit": { "context": 200000 }
  }
}
```

The slug is **not** stripped of its `adroute/` prefix: it is the ID sent to the gateway. OpenCode's fully qualified selector includes its own provider prefix, e.g. `adroute/adroute/work/coding`.

其余配置仍由用户拥有：运行时包、provider 名称、端点、令牌、headers、其他 provider、默认值、权限及无关设置都保留。被替换模型映射中的旧模型选项/注释会移除，provider 级设置保留。不修改默认模型或现有会话选择。

Successful calls return:

```js
{
  changed: true,          // False if the map already matches semantically.
  configPath: '/absolute/path/to/opencode.jsonc',
  backupPath: '/absolute/path/to/opencode.jsonc.adroute-before-<uuid>.bak',
  modelCount: 1,
  restartRequired: true,
}
```

An unchanged call returns `backupPath: null` and does not replace the config or create another backup. `restartRequired` is conservative guidance, including for unchanged calls: V2 documents automatic configuration reload, but the module cannot confirm what a running client/server has loaded. Restart/reload OpenCode manually if needed to guarantee fresh model selection; this module never does so.

## Existing provider required

Before syncing, the **target file itself** must contain an appropriately configured V2 `providers.adroute` object. The module does not merge configuration from other scopes or inspect accounts. Required:

- Explicit `package` naming a Responses runtime: `aisdk:@ai-sdk/openai`, `@opencode/ai/providers/openai`, `@opencode/ai/providers/openai/responses`, or `@opencode/ai/providers/openai-compatible/responses`. These are the packages whose requests reach the gateway as `POST /v1/responses`.
- `settings.baseURL`: a complete HTTP(S) endpoint pointing at the gateway's `/v1`, or a whole `{env:NAME}` / `{file:path}` reference. URLs containing credentials, query strings, or fragments are refused. References are preserved without resolving or verifying them.
- An existing nonblank `settings.apiKey`, nonempty `env` array of valid environment-variable names, or nonblank case-insensitive `Authorization` header. Actual credential availability/validity is not checked, and references are never expanded.
- `models`, when present, must be an object.

The gateway serves Responses, not Chat Completions. Chat packages such as `aisdk:@ai-sdk/openai-compatible` and `@opencode/ai/providers/openai/chat` are refused rather than silently publishing models that would fail at request time. Chat-only runtimes, implicit/default runtimes, arbitrary custom packages, missing providers, and credentials stored solely in an uninspected account connection fail with guidance rather than being guessed or replaced. A structurally valid configuration is not proof that its endpoint or token works.

## Target file

Without `configPath`, sync selects the single existing `opencode.json` **or** `opencode.jsonc` in `$HOME/.config/opencode`. `$XDG_CONFIG_HOME/opencode` is supported as an explicit configuration-directory convention.

When both default files exist, the module inspects them and writes the one that already declares `providers.adroute`. If both declare it, it writes `opencode.jsonc`, because OpenCode loads that file last and its value is the one actually applied. If neither declares the provider, or if the ownership cannot be read safely, it refuses with `OPENCODE_CONFIG_AMBIGUOUS` instead of creating a provider in an arbitrary file. Relative paths and `.`/`..` path segments are refused so normalization cannot hide a symlink. Missing configuration files/directories are never created.

The V2 server-config guide documents `~/.config/opencode/opencode.json(c)`; the CLI-settings guide explicitly documents the XDG global directory convention for `cli.json`. For an installation whose server-config path differs, pass `configPath`; the module does not inspect a running server to infer its directory.

When `OPENCODE_CONFIG`, `OPENCODE_CONFIG_DIR`, or `OPENCODE_CONFIG_CONTENT` is set, implicit lookup is refused; use `configPath` to select an actual file deliberately. This does not alter or neutralize those external overrides. No project, ancestor, managed/system, inline, or remote configuration is read. Such configuration may override the file after sync, and models from another scope may remain visible after V2 merges files.

## Preservation and write safety

- JSON and JSONC support comments, trailing commas, and an optional UTF-8 BOM. Only the models value range is replaced; all bytes outside it survive, including CRLF, whitespace, escaped keys, comments, and large numeric literals. Adding an absent map inserts a property without rewriting existing fields or their comments.
- Duplicate decoded keys anywhere, malformed syntax/UTF-8, more than 100 nested levels, files/output exceeding 2 MiB, and non-object roots are rejected. The Go implementation uses a bounded range parser rather than stripping comments and reserializing the whole document.
- Symlinks in the target or its ancestors, hardlinked targets, and non-regular files are refused. The implementation requires a local POSIX-style filesystem with `O_NOFOLLOW`, exclusive create, directory fsync, and atomic same-directory rename semantics.
- A per-target `*.adroute-sync.lock` is exclusively created. Existing locks (including symlinks) are never followed or automatically reclaimed. After a crash, inspect that no sync is active before manually removing the lock.
- Every changed call creates its own exclusive, mode-`0600`, exact original-byte `*.adroute-before-<uuid>.bak` beside the config. The backup is fsynced and its directory entry flushed **before** config replacement. Later syncs never overwrite earlier backups. Backups contain secrets; retain/protect them and remove them deliberately when no longer needed.
- Output is staged in a unique mode-`0600` file, fsynced, then renamed atomically over the config. Config output is private (`0600`); original ownership/ACLs and special metadata are not reproduced. The original backup is retained if staging or rename fails.
- Parent-directory identities, lock ownership, and the original file's identity, metadata, and bytes are rechecked before rename. Detected external edits abort without overwriting them. Staging files and owned locks are cleaned up on normal failures; a crash can leave them behind.
- Locks serialize cooperating sync calls only; they do not provide portable atomic compare-and-swap against arbitrary uncooperative writers. An external writer can still race the final checks. This is not a defense against hostile users able to rename ancestor directories. Keep the configuration directory trusted and avoid editing it during sync. A failure after rename but during the final directory fsync means the new file may already be installed; no automatic rollback risks overwriting a later edit.

## Errors

Errors are `AppError` instances with HTTP status `409`, sanitized messages, and stable codes:

- `OPENCODE_MODELS_INVALID`: bad snapshot.
- `OPENCODE_CONFIG_PATH`: invalid/unknown path or implicit override ambiguity.
- `OPENCODE_CONFIG_MISSING`: no existing configuration.
- `OPENCODE_CONFIG_AMBIGUOUS`: both default files exist and neither declares `providers.adroute`.
- `OPENCODE_PROVIDER_INCOMPATIBLE`: provider needs manual V2 Responses setup.
- `OPENCODE_CONFIG_INVALID`: unsupported/unsafe JSONC or size/depth/encoding limit.
- `OPENCODE_CONFIG_UNSAFE`: unsafe file or path.
- `OPENCODE_CONFIG_BUSY`: another operation or retained lock.
- `OPENCODE_CONFIG_CHANGED`: detected intervening file/directory/lock change.
- `OPENCODE_CONFIG_WRITE_FAILED`: I/O or durability failure; inspect the existing file/backups before retrying.

No parse snippets, tokens, or underlying filesystem error messages are exposed. There is no automatic restore API: restoring a whole backup can overwrite later edits, so review and restore it explicitly.

## V2 references

Verified against public V2 documentation (not the potentially V1-shaped published config schema):

- [Config: locations, JSONC, variables, and providers](https://opencode.ai/v2/docs/config)
- [Providers: packages, credentials, settings, and modelID](https://opencode.ai/v2/docs/providers)
- [Models: selector prefixes, defaults, and reload caveats](https://opencode.ai/v2/docs/models)
- [CLI settings: explicit XDG directory convention](https://opencode.ai/v2/docs/cli/config)

Tests use disposable homes beneath `/tmp/opencode`; no real OpenCode configuration or running service is used.
