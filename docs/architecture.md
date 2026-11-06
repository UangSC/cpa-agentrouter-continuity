# 代码结构

[返回项目首页](../README.md)

## Go 插件

`cmd/ar-continuity` 是 `-buildmode=c-shared` 的唯一构建入口。C ABI 导出、请求回调和内部状态使用同一个 `main` 包；相关 Go 测试与实现放在一起，可验证内部状态而不扩大公开接口。

| 文件组 | 职责 |
| --- | --- |
| `main.go`、`abi.go`、`plugin.go` | 默认入口、C ABI 内存边界、插件注册与宿主调用分发 |
| `credential.go`、`models.go`、`management.go` | AR 凭证范围、模型配置、鉴权管理接口及资源响应 |
| `transport.go`、`stream.go`、`retry.go` | 宿主 HTTP 回调、JSON/SSE 生命周期、重试预算和交付边界 |
| `error_details.go`、`pricing_error.go` | 上游错误分类、历史校验与计费故障诊断 |
| `routing.go`、`routing_store.go` | 会话来源、名额准入和持久化状态 |
| `routing_lock_*.go`、`process_identity_*.go` | 各平台文件锁与进程身份 |
| `monitor.go`、`analytics.go` | 脱敏请求元数据、Key / 模型时间分桶与数字用量 |
| `channel_pricing.go` | 固定公开渠道接口、默认分组 Token 单价换算 |
| `tool_alias.go` | 桌面工具命名空间转换与还原 |
| `probe.go` | `probe` 构建标签下的独立诊断入口，正常插件不启用 |

`internal/sdk` 只保留宿主接口类型与许可。网页资源通过独立的 `web` Go 包读取，后端不依赖启动目录查找静态文件。

## 网页

| 文件 | 职责 |
| --- | --- |
| `index.html` | 登录、四个页面及编辑弹窗结构 |
| `app.js` | CPA 连接、主题、Key 编辑、刷新与公共显示函数 |
| `dashboard.js` | 趋势、时间线、结果分布与成本排行 |
| `monitoring.js` | 完成率状态条、日志筛选 / 分页 / 详情、连接策略 |
| `pricing.js` | 默认单价、公开价格获取、别名匹配、表单与本地保存 |
| `style.css`、`monitoring.css` | 仪表盘、编辑器及监控页面的样式与响应式布局 |
| `embed.go` | 按扩展名显式嵌入 HTML / JS / CSS / PNG，排除 Go 源码和本地工具 |

脚本按 HTML 中的 `defer` 顺序加载，共享轻量运行状态；不引入打包器或 Node 生产依赖。资源 URL 仍为 `/v0/resource/plugins/cpa-agentrouter-continuity/*`。

## 验证和分发

- `cmd/ar-continuity/*_test.go`：请求、协议、错误、路由、管理与聚合的单元验证。
- `tests/web`：Node 逻辑测试；Playwright 使用模拟接口，不访问生产凭证。
- `tests/integration/run.py`：在临时目录启动 Linux CPA 和本地 TLS 上游，验证共享库及完整调用链。
- `tests/manual`：需要真实服务配置的人工诊断，不进入默认测试或自动发布流程。
- `tests/scripts`：验证密钥识别、忽略边界与历史 / 安装包审计。
- `scripts`：只从明确的源码或提交构建分发内容。构建产物和运行数据不放入 Git 源码树。

本次目录整理保持 0.5.2 功能与协议兼容；根目录主要保留模块、构建、许可证和入口文档。运行中的 CPA 无需因源码位置调整而重新配置。
