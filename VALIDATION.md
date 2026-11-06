# 0.4.5 跨平台验证

验证日期：2026-11-06（北京时间）。

## 环境与结果

| 项目 | 本次结果 |
| --- | --- |
| Linux 环境 | WSL2 Ubuntu 24.04、x86-64、glibc 2.39；Go 1.26.10、Zig 0.15.2 |
| Linux 测试 | `go test -race -count=1 ./...`、`go vet ./...` 通过 |
| Linux 共享库 | `CGO_ENABLED=1`、`-buildmode=c-shared` 构建成功；C 编译目标 `x86_64-linux-gnu.2.28` |
| Linux 宿主 | 独立 CPA 8.0.34 实际加载插件，管理接口确认版本 0.4.5 |
| Windows 环境 | Windows amd64、Go 1.26.9、LLVM MinGW 20261006 UCRT |
| Windows 验证 | 全量 Go 竞态测试、静态检查、DLL 构建通过 |
| 管理界面 | `node --test tests/ui.test.cjs`，10 项通过 |

新增 Linux 专项测试验证跨进程文件锁的互斥与释放、会话状态及锁文件的 0600 权限、包含启动时间的进程标识。测试临时目录使用 WSL 原生 Linux 文件系统；未开启权限元数据的 Windows 磁盘挂载可能将权限显示为 0777，不适合验证 Linux 的 0600 要求。

## Linux 隔离集成覆盖

通过 `tests/integration.py` 启动独立 CPA、临时配置、模拟凭证与本地 TLS 上游。最终完整运行退出码为 0，覆盖：

- 成功请求、429 退避、输出前断流恢复、输出后不重放、认证失败、加密历史保留。
- `content_filter`：输出前、已有输出后以及非流式三种场景，各自只有一次上游调用，终止错误和失败日志正确，已发送文本保留。
- `max_output_tokens`：流式和非流式继续保留正常未完成响应。
- 桌面 `additional_tools` 首轮准入，SSE 和 JSON 工具别名往返还原；调用 ID、参数与正文保留。只校验模拟工具调用，不执行工具。
- 管理鉴权、模型列表、凭证编辑及新增、健康检查、可选模型前缀。
- 外部会话与未知历史排除、AR 会话续接、同一 Key 两个活跃会话上限、溢出回退与名额释放。
- 配置重载后来源保留、在途流配置热加载、下游取消后不重放。

CPA 8.0.34 会把插件的 `response.failed` 转为流式 `error` 事件，保留 `ar_content_filter`。非流式保留 HTTP 400、`invalid_request_error` 和消息中的 `content_filter`，但省略独立的 `code` 字段；插件自身日志仍记录 `ar_content_filter`。集成测试分别验证这些宿主行为，单元测试验证插件原始错误协议。

## 可复现命令

Linux 上配置 Go、C 编译器和上述测试依赖后执行；`CPA_BINARY` 指向 Linux 宿主可执行文件：

```sh
export CGO_ENABLED=1
export CC="zig cc -target x86_64-linux-gnu.2.28"
go test -race -count=1 ./...
go vet ./...
mkdir -p dist/linux/amd64
go build -trimpath -buildmode=c-shared \
  -o dist/linux/amd64/cpa-agentrouter-continuity-v0.4.5.so .
python3 tests/integration.py --cpa-binary "$CPA_BINARY" \
  --plugin "$PWD/dist/linux/amd64/cpa-agentrouter-continuity-v0.4.5.so"
```

## 产物与边界

| 文件 | SHA-256 |
| --- | --- |
| `cpa-agentrouter-continuity-v0.4.5.so` | `d292080efbb6ce8171664e4056632f80e133bc3e10954a17c816e459bc8cc785` |
| `cpa-agentrouter-continuity-v0.4.5.dll` | `07d9d74ddd6d06bd007afd6ea22b75e66cf795e068a043e623ebe2f2c4f7d35b` |

Linux ELF 和动态依赖检查通过，未引用高于构建目标 glibc 2.28 的版本符号。实际运行验证在 glibc 2.39 完成，未逐一测试其他发行版；未验证 ARM64、musl/Alpine、macOS、WebSocket 或真实上游压缩。此次 Windows 验证限于测试与构建，没有重新部署生产 DLL。

本次不请求真实模型、不重放生产会话，也不修改运行中的 CPA 配置。`tests/windows_live_tools.py` 是需人工指定本机 CPA 和错误日志的真实上游验证脚本，可能消耗额度，不属于本次自动测试范围。
