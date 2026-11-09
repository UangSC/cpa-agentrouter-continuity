# Changelog

## 0.5.5 - 2026-11-09

- 使用新的透明背景 AR 图标替换旧 Logo，统一插件元数据、浏览器标签页、页头、登录页、Key 编辑弹窗与项目首页。
- 图标展示改为方形尺寸，兼容深浅主题和小屏。

## 0.5.4 - 2026-11-09

- 新 AR 会话在同等优先级的可用 Key 中优先选择活跃会话最少者，数量相同时轮流选择；按真实 API Key 聚合，重复凭证不增加分配份额。同一 Key 内保留凭证权重，混合供应商仍遵循原优先级与权重。
- 已准入 AR 的会话固定使用原 Key；原 Key 满额、不可用或不支持所选模型时明确拒绝，不改用其他 Key 或供应商。原 Key 恢复后可以继续原会话。
- 分配游标仅在新会话准入时更新并随会话状态保存；续聊不影响新会话的轮流分配。
- 新增并发均衡、重复 Key、会话粘性及原 Key 暂时不可用后的恢复测试。

## 0.5.3 - 2026-11-09

- 兼容 DeepSeek Harness 通用 Responses 适配器中的会话标识：识别 `X-Client-Request-Id` 和 `prompt_cache_key`，两者同时存在时要求一致；保留已有会话头的优先级。
- 仅对 `deepseek-harness/` 客户端启用此适配，不将其他客户端的单次请求 ID 当作会话 ID；继续执行首轮、来源和每 Key 并发检查。

## 0.5.2 - 2026-11-06

- 整理仓库目录：插件与同包 Go 测试集中到 `cmd/ar-continuity`，网页通过独立 `web` 包嵌入，测试和文档按职责分区；修正构建与打包入口，保持运行接口兼容。
- 发布检查覆盖当前源码、可达 Git 历史及 Release 安装包；安装包从干净提交读取源码，记录提交与校验清单。新增持续集成检查并排除本地运行资料。
- 请求日志重做为统计卡片、组合筛选、分页表格和请求详情；时间统一为北京时间。
- 连接与策略重做为近期表现、独立连接检测、实际配置表和请求保护规则。
- 监控灯改为 CPAMP 风格的扁平分段状态条，按每个时间段的完成率连续渐变；聚合按 Key 隔离。
- 新增「获取渠道定价」及经公开接口核实的默认单价；支持上游模型别名匹配、明确的缓存估算假设与保存前预览。
- 完成率排除取消和进行中请求；获取失败保留当前价格，手工修改优先，未知或不支持的计费方式不猜价。

## 0.5.1

- 页面总标题统一为「仪表盘」，移除宣传式标语，按功能模块命名各区域。

## 0.5.0

- 重做 CPAMP 风格仪表盘：晶体管状态灯阵、今日概况、可交互流量趋势、请求结果环图、请求监控时间线与模型成本排行。
- 新增最近 24 小时的 5 分钟聚合，不受最近 200 条日志上限影响；保留请求开始时间归属及重载后重新统计的边界。
- 采集最终响应的数字 Token 用量；新增仅存于浏览器的模型单价及费用估算，扣除缓存输入，缺失用量或单价明确显示。
- 支持深浅主题、小屏布局、键盘图表提示、减少动效和断线状态。原有 Key 配置、请求日志和恢复规则保持兼容。


## 0.4.10 - 2026-11-06

- 在现有内部重试循环中加入历史校验恢复：最多额外 2 次、约 1/3 秒退避，计入每个 Key 的总次数与等待预算，完整保留原 Key、模型和请求历史。
- 内部恢复成功仅交付正常结果；预算耗尽后才发送一次最终错误。已输出后继续禁止重放，计费配置错误和内容过滤仍立即结束。
- 用 `ar_history_verification_failed` 和更明确的上游故障诊断替代笼统提示，保留原始原因、HTTP 状态及 trace_id；请求日志结构和客户端配置保持原样。

## 0.4.9 - 2026-11-06

- 将上游 `tiered expr run failed` / `expr compile error` 识别为 `ar_upstream_pricing_error`，统一按不可重试错误终止，覆盖 HTTP 400/500、流式输出前后和宿主读取异常。
- 返回明确的上游计费规则异常提示，保留未知变量名和 `trace_id`，对诊断文本限长并脱敏。
- 已输出内容保留，失败后释放会话名额；现有请求日志使用专用错误码，表格和数据结构保持原样。

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
