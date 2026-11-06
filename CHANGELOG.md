# Changelog

## 0.4.8 - 2026-11-06

- 按用户选择保持请求日志原有布局和数据字段，移除 0.4.7 新增的并发日志快照。
- 保留每个 API Key 的并发上限配置、凭证列表和健康页的活跃会话数；默认 2，范围 1–100。
- EasyCPA 主程序及其“账号授权”按钮保持原样。

## 0.4.7 - 2026-11-06

- 每个 API Key 可配置 `max_concurrent_sessions`，默认 2，范围 1–100；旧凭证和省略该字段的编辑保持兼容。
- 凭证列表和健康卡片显示共享活跃会话数及当前上限，请求日志记录请求开始时的会话数与上限。
- 上限调整影响后续新会话准入；保留既有请求、同一会话复用和相同 Key 的共享计数。

## 0.4.6 - 2026-11-06

- 安装展示名称和插件菜单改为 `AR Continuity`，页面标题改为 `AgentRouter Continuity`。
- 使用用户提供的透明 ARC 图片替换页面与凭证弹窗的字母标志，并作为插件安装图标和浏览器图标；图片随动态库嵌入，无需外部图片服务。
- 保留插件 ID、配置路径、凭证归属及请求处理逻辑。

## 0.4.5 - 2026-11-06

- 兼容桌面端首轮 `additional_tools` 定义；仅在正文通过新会话检查且显式标记 `history_initialization=new` 时，允许替换旧的外部来源记录。
- 对 AR 请求中的 `collaboration` 工具命名空间使用兼容别名，并在 SSE、JSON 和 compact 响应中还原；保留工具参数、结果、正文与加密历史。
- 将上游 `content_filter` 转为明确的 `ar_content_filter` 终止错误，插件不自动重试；其他未完成原因继续透传。客户端仍可能自行发起新的请求。
- Linux 和 Windows 共用以上修复；构建文件名从源码版本生成，隔离集成测试核实加载版本，并覆盖过滤处理及桌面工具往返。

## 0.4.1

- Add Windows amd64 support for the session ledger's cross-process file lock.
- Use a Windows process creation timestamp when clearing stale leases, avoiding PID-reuse collisions.
- Publish matching Linux and Windows assets without changing routing or credential behavior.

## 0.4.0

- Rename the project, Go module, plugin/provider ID, management routes, browser storage and build artifacts to `cpa-agentrouter-continuity`.
- Publish source and Linux amd64 shared-library distributions under the project repository.
- Preserve model discovery with preselected existing models, runtime-only API keys, default Codex request headers, session-origin protection and the two-active-session limit per AR key.
- Document migration of private credentials and origin state from the earlier plugin identity. Do not enable both scheduler plugins together.

This release changes identifiers; it does not automatically migrate or restart an existing CPA service.
