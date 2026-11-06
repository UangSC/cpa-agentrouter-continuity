# 跨平台验证记录

## 0.4.7 每个 Key 的并发会话配置（2026-11-06）

- `max_concurrent_sessions` 默认 2，接受 1–100 的整数；旧凭证默认值、省略字段时保留已有配置、非法值拒绝均通过测试。
- Windows / Linux 全量 Go 竞态测试和静态检查通过；并发压力用例核对 1、3、7 三种上限，相同会话复用及重复 Key 共享计数通过。
- Linux CPA 8.0.34 隔离测试覆盖管理 API 将上限设为 3、3 个同时在途请求、第四个回退、在途时下调为 1、请求继续完成、后续上限 1、日志历史快照及非法参数拒绝。
- 11 项界面单元测试通过；浏览器实际保存上限 5、再次编辑回读、停用后配置保留、新建默认 2、并发日志列、亮暗主题及手机布局检查通过。
- 本机 Windows CPA 已通过管理 API 热加载 0.4.7；进程 PID 保持不变，原有凭证、其他插件和 CPA 配置的语义比较一致，YAML 解析通过。未使用真实模型生成请求验收。
- “账号授权”由 EasyCLIProxyAPI 主程序的 `PluginsPage.tsx` 决定，当前宿主将凭证解析能力同时标记为 OAuth。插件保留必要的凭证解析能力；这次没有修改或重编译主程序，因此此按钮仍存在。

| 0.4.7 文件 | SHA-256 |
| --- | --- |
| `cpa-agentrouter-continuity-v0.4.7.so` | `a55a3b56040a116af74b9e172067ba3f378d69fd16fa46671dab3dfb28b5f5aa` |
| `cpa-agentrouter-continuity-v0.4.7.dll` | `23d66d7f0b43e0c6447523b045766055e23dd12d9dc17561450e8a174532e027` |

## 0.4.6 展示更新（2026-11-06）

- 安装元数据与菜单显示 `AR Continuity`，插件页面及浏览器标题显示 `AgentRouter Continuity`；原插件 ID 保持不变。
- 使用项目维护者提供的透明 ARC 图片，裁去外围空白并缩小为 512×381 PNG；图标嵌入动态库，在安装元数据、页面、凭证弹窗和浏览器标签中复用。
- Windows 与 Linux 的 `go test ./...`、`go vet ./...` 和 c-shared 构建通过；10 项界面测试通过。
- Linux CPA 8.0.34 实际加载 0.4.6，完整隔离集成测试通过。管理接口确认安装名称、版本、原插件 ID 和 Logo 路径；PNG 返回 `image/png`，响应字节与打包图片一致。
- 使用 Edge 无头浏览器检查亮色、暗色、1280 像素桌面宽度和 390 像素手机宽度；标题、两处 Logo 加载、凭证弹窗均正常，无页面脚本错误或整页横向溢出。
- 未修改生产配置、凭证或部署中的动态库。

| 0.4.6 文件 | SHA-256 |
| --- | --- |
| `cpa-agentrouter-continuity-v0.4.6.so` | `326d57ce6842c23b139fc34e754d8cefa74361ab2bf61f419b2b98519afff1a1` |
| `cpa-agentrouter-continuity-v0.4.6.dll` | `d768cd392d50194c28333f2a9a5bf55bb5dcfc556e3c5ea84e4365b0d59975b4` |

## 0.4.5 跨平台验证

验证日期：2026-11-06（北京时间）。

### 环境与结果

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

### Linux 隔离集成覆盖

通过 `tests/integration.py` 启动独立 CPA、临时配置、模拟凭证与本地 TLS 上游。最终完整运行退出码为 0，覆盖：

- 成功请求、429 退避、输出前断流恢复、输出后不重放、认证失败、加密历史保留。
- `content_filter`：输出前、已有输出后以及非流式三种场景，各自只有一次上游调用，终止错误和失败日志正确，已发送文本保留。
- `max_output_tokens`：流式和非流式继续保留正常未完成响应。
- 桌面 `additional_tools` 首轮准入，SSE 和 JSON 工具别名往返还原；调用 ID、参数与正文保留。只校验模拟工具调用，不执行工具。
- 管理鉴权、模型列表、凭证编辑及新增、健康检查、可选模型前缀。
- 外部会话与未知历史排除、AR 会话续接、同一 Key 两个活跃会话上限、溢出回退与名额释放。
- 配置重载后来源保留、在途流配置热加载、下游取消后不重放。

CPA 8.0.34 会把插件的 `response.failed` 转为流式 `error` 事件，保留 `ar_content_filter`。非流式保留 HTTP 400、`invalid_request_error` 和消息中的 `content_filter`，但省略独立的 `code` 字段；插件自身日志仍记录 `ar_content_filter`。集成测试分别验证这些宿主行为，单元测试验证插件原始错误协议。

### 可复现命令

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

### 产物与边界

| 文件 | SHA-256 |
| --- | --- |
| `cpa-agentrouter-continuity-v0.4.5.so` | `d292080efbb6ce8171664e4056632f80e133bc3e10954a17c816e459bc8cc785` |
| `cpa-agentrouter-continuity-v0.4.5.dll` | `07d9d74ddd6d06bd007afd6ea22b75e66cf795e068a043e623ebe2f2c4f7d35b` |

Linux ELF 和动态依赖检查通过，未引用高于构建目标 glibc 2.28 的版本符号。实际运行验证在 glibc 2.39 完成，未逐一测试其他发行版；未验证 ARM64、musl/Alpine、macOS、WebSocket 或真实上游压缩。此次 Windows 验证限于测试与构建，没有重新部署生产 DLL。

本次不请求真实模型、不重放生产会话，也不修改运行中的 CPA 配置。`tests/windows_live_tools.py` 是需人工指定本机 CPA 和错误日志的真实上游验证脚本，可能消耗额度，不属于本次自动测试范围。
