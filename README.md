# AR Continuity

当前版本：**0.5.4**，包含 DSH Responses 兼容、Key 均衡与会话粘性。

AgentRouter 专用的 CPA 插件，提供 API Key 管理、请求监控、连接检测和 Responses HTTP/SSE 的有限故障恢复。只处理 `https://agentrouter.org/v1`，不替换 CPA 内置 Codex 执行器。

## 功能

- **仪表盘**：按完成率渐变的监控条、今日概况、流量趋势、请求时间线和模型成本排行，支持深浅主题与小屏。
- **请求日志**：按模型、请求 ID、错误码、状态和 Key 筛选；支持分页、耗时查看和内部恢复详情。
- **连接与策略**：每 Key 独立展示近期请求表现、模型列表检测结果和实际配置。
- **渠道定价**：读取 AgentRouter 公开定价接口并填入表单；默认单价、手动覆盖和费用估算保存在浏览器。
- **来源与并发保护**：新会话按活跃会话数均衡分配，数量相同时轮流选择；已有 AR 会话固定绑定原 Key。每 Key 默认最多 2 个活跃会话，范围 1–100，同一会话复用名额。
- **有限内部重试**：首次内容交付前，在次数和等待预算内恢复临时限流、早期断流和部分历史验证错误。

## 安装与使用

从 [GitHub Releases](https://github.com/UangSC/cpa-agentrouter-continuity/releases) 下载对应平台的 ZIP，并核对同名 `.sha256` 文件。

| 平台 | 插件文件 | 部署位置 |
| --- | --- | --- |
| Windows amd64 | `cpa-agentrouter-continuity-v0.5.4.dll` | `<plugins.dir>/windows/amd64/` |
| Linux amd64 | `cpa-agentrouter-continuity-v0.5.4.so` | `<plugins.dir>/linux/amd64/` |

Linux 分发目标为 glibc 2.28 或更新，隔离测试使用 CPA 8.0.34（插件 RPC schema 6）；Windows amd64 在 CPA 8.0.57 上完成隔离加载、在途热更新及真实 AR 首轮与续聊验证。其他 CPU 架构、musl/Alpine 和不兼容插件 ABI 的宿主需要另行构建和验证。

1. 备份目标机配置，将插件文件放入对应目录。
2. 在 CPA 插件管理中启用 `cpa-agentrouter-continuity`。
3. 打开 **AR Continuity**，使用 CPA 管理密钥连接，再添加 AR API Key。
4. 配置模型、前缀、并发和重试策略。模型列表获取与手动连接检测都不发送生成请求。

管理密钥与 AR API Key 用途不同。页面兼容 CPAMC / CPAMP 已记住的管理密钥；AR Key 仅保存在服务器，不回显完整值。不要将运行时凭证、配置、请求日志或会话状态上传到仓库。

## 使用边界

- 只有请求元数据和最终响应明确返回的数字用量进入统计；不保留对话正文或工具参数。
- 5 分钟聚合保留最近 24 小时，日志保留最近 200 条结束记录；重启或换版后重新累计。
- 完成率排除取消和进行中请求，内部重试后完成仍计为完成。模型列表检测成功不代表实际生成一定可用。
- 渠道默认价格核实于 2026-11-06；公开接口未公布缓存价时，缓存按普通输入价估算。费用不包含账号专属折扣、阶梯规则及失败尝试的额外计费，不代表账单。
- 已交付文字、推理或工具事件后不自动重放。认证、配额、内容过滤和上游计费配置错误立即结束；重试不能保证任意断流的无损续传。
- 非 AR 来源或无法确认来源的历史会话不会自动切入 AR；并发、会话来源、代理与迁移规则详见配置文档。

## 项目结构

```text
cmd/ar-continuity/  Go 插件入口、C ABI、请求引擎与同包单元测试
internal/sdk/      随附宿主接口类型及原始许可
web/               原生 HTML / JS / CSS、Logo 与 Go 嵌入入口
tests/web/         网页逻辑测试与模拟接口浏览器验收
tests/integration/ 隔离 CPA + 本地 TLS 模拟上游的完整回归
tests/manual/      需要操作者显式配置的真实环境检查
tests/scripts/     发布与安全检查脚本的回归测试
scripts/           凭证模板生成、源码审计与 Release 打包
examples/          不含密钥的配置模板
docs/              配置、架构、开发、验证与仓库审计说明
licenses/          第三方原始许可
```

`build/` 存放本地工具和临时资料，`dist/` 存放二进制、截图与安装包；两者均不受 Git 跟踪。源文件位置调整不改变插件标识、管理接口、静态资源 URL 或凭证格式。

## 开发

复用可用的 Go 和 C 编译器，启用 CGO；不需要额外 Go 模块依赖。

```sh
make test
make build
make check
make audit
```

`make test` 运行 Go 竞态检查、静态检查、Node 逻辑测试和 Python 脚本测试。`make browser-test` 需要 Playwright 与浏览器；可通过 `PLAYWRIGHT_PATH`、`BROWSER_PATH` 复用已有安装。

`make build` 将平台插件输出到 `dist/<os>/<arch>/`。构建、跨平台兼容性、完整集成测试和打包命令见 [开发与发布](docs/development.md)。

## 文档

- [使用与配置](docs/configuration.md)：会话保护、Key、定价、恢复规则、迁移及回滚。
- [代码结构](docs/architecture.md)：模块职责、嵌入资源和测试组织。
- [开发与发布](docs/development.md)：构建、测试与可审核的打包过程。
- [验证记录](docs/validation.md)：历史验证范围与平台限制。
- [仓库审计](docs/repository-audit.md)：本次远端文件、历史和附件检查结果。
- [更新日志](CHANGELOG.md) · [许可证](LICENSE) · [第三方许可说明](THIRD_PARTY_NOTICES.md)。
