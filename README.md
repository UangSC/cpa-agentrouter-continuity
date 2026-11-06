# AR Continuity

当前版本：**0.4.9**。

AgentRouter 专用的 CPA 插件。提供 API Key 管理、请求日志、连接检测，以及 Responses HTTP/SSE 的有限故障恢复。只处理 `https://agentrouter.org/v1`，不拦截其他供应商，不替换 CPA 内置 Codex 执行器。

## 会话来源与并发保护

- 只允许“从 AR 开始”的会话继续进入 AR。新会话仍按可用候选、既有亲和与优先级选择；不是所有新窗口都强制转入 AR。
- 非 AR 会话不会因为 AR 恢复可用或优先级变高而被自动切入。带历史回复、工具结果、压缩引用但没有已知 AR 来源的请求，保守排除 AR。
- 每个 API Key 的并发会话上限默认 **2**，可在编辑弹窗中设为 **1–100**；统计正在请求、重试或生成的不同会话。同一会话同时发起多个请求按一个会话计数，最后一个请求完成才释放。闲置窗口不占用。
- 相同 API Key 即使被重复配置为多个凭证，也共享活跃会话计数，不会获得独立名额池；各配置按自身上限判断新会话准入，建议同 Key 的重复配置使用相同上限。不同 Key 分别计数。
- 满额或来源不合格时，从其他可用候选选择；如果客户端只指定了 AR 专属模型且没有其他候选，会明确报错，不偷偷超额或转入不支持该模型的供应商。
- 会话先走其他供应商后不会回到 AR。AR 会话若后续转走其他供应商，也不再自动回到 AR，避免混入不兼容的加密历史。
- 服务端无法观察“新建窗口”动作，以会话标识、首次请求和已记录来源判断。无法确认的新会话可能保守绕开 AR。不要复用会话 ID 表示两个不同窗口。
- 首次升级时无法恢复全部历史来源；旧窗口如果没有可验证的 AR 来源，会保守绕开 AR，需要新建会话使用。
- 桌面端首轮 `additional_tools` 初始化定义可正常准入。若宿主明确标记 `history_initialization=new` 且正文没有历史回复、工具结果或加密内容，可替换之前失败首轮留下的外部来源记录；`cold_resume` 不适用。

调度插件仅在候选含 AR 时处理选择；没有 AR 候选时仍委托原调度。混合候选先保留可用的原会话亲和，再按最高可用优先级与权重选择；这部分由插件执行，因为当前宿主接口没有“移除部分候选后再交回原选择器”的接口。若部署了另一个调度插件，需要确认不会覆盖本插件；执行器还会二次验证名额，未获准不会请求 AR。

状态文件为 CPA 工作目录下 `plugins/cpa-agentrouter-continuity-sessions.json` 与 `.lock`。只有会话哈希、来源、凭证标识、Key 哈希和名额，没有聊天正文或密钥。权限 600；热更新与重启保留来源，重启清除旧进程名额。迁移时可私下迁移此状态文件；缺失时已有历史会话不会被视为新会话。不要在服务运行时删除状态或锁文件。若状态不可读/损坏，保守关闭 AR 准入，其他可用候选仍可用。

限制在同一 CPA 工作目录内有效；多个独立 CPA 实例并不共享全局并发配额。当前文件锁实现支持 Linux、macOS、FreeBSD 和 Windows；其他平台需要补充相应的跨进程锁实现，不能直接宣称支持。

## 管理页面

从 CPA 的插件菜单打开 **AR Continuity**：

1. 输入 CPA **管理密钥**，点击「连接」或按回车。
2. 可勾选「记住密钥」，下次自动连接。也兼容 CPAMC / CPAMP 已记住的管理密钥。
3. 页面配色跟随管理面板的明暗主题；单独打开时跟随浏览器系统主题。

管理密钥与 AR API Key 是两个用途不同的密钥。记住管理密钥会将其保存在当前浏览器、当前站点的本地存储中；「断开连接」清除本插件保存的副本，不改变主面板自己的记住设置。AR API Key 不保存在浏览器本地存储中，不回显完整值。

### API Key 管理

支持新增、编辑、启用和停用。每个 Key 独立配置名称、模型与别名、可选前缀、并发会话上限、请求头、重试次数和预算。上限保存后用于后续准入，调低不会中断已在途请求；同一活跃会话的多个请求仍共用名额。编辑时 Key 留空表示保留；新增时必须输入 Key。

模型输入框旁的「获取模型」会直接读取 AR `/v1/models`。编辑现有凭证且 Key 留空时使用已保存 Key；新增或填写新 Key 时使用表单中的值，不会提前保存。弹窗支持搜索、多选、全选/清空当前搜索结果；输入框已有模型默认勾选，别名保留。本次未返回的已有模型也保留为可选项，避免静默删除。确认只填入表单，保存配置后才生效；取消或获取失败不会改动原模型列表。

不需要账号登录或 OAuth 授权。插件沿用 CPA 的凭证存储、文件权限和热加载机制；只允许修改本插件的 AR 凭证。页面不显示其他供应商凭证。

### 请求日志与健康

每 5 秒刷新，展示进行中的请求及最近 200 条完成记录：模型、凭证名称、状态、尝试次数、首段延迟、总耗时、重试原因和错误码。不记录对话正文、工具参数或完整密钥。日志和健康检测结果保存在内存中，进程重启或插件换版后重新开始；不导入旧版本的请求历史。

健康卡片结合最近请求显示状态。手动「检测连接」访问 AR 的模型列表接口，不发送生成请求；列表成功不能保证实际模型生成成功。检测沿用 CPA 全局代理。不会自动定时调用 AR。

## 默认请求头与密钥

内置默认值：

```text
User-Agent: codex_exec/0.159.3 (Windows 10.0.19045; x86_64) dumb (codex_exec; 0.159.3)
Originator: codex_exec
```

请求头未填写或为空时自动使用上述默认值，可覆盖为其他 AR 接受的 Codex/Zcode 客户端标识。认证头始终从当前凭证的 `api_key` 构造，不能被自定义请求头替换。

默认 User-Agent 是上游兼容标识，在 Linux 上同样使用，不用于判断宿主操作系统。AR 请求中的 `collaboration` 工具命名空间会转换为 `cpa_ar_collaboration`，返回时恢复；只转换协议工具标识，不改参数、结果正文或加密历史。客户端已占用相同别名时明确报错。

**源码、示例与二进制不包含可用 API Key 或管理密钥。** 示例 `api_key` 为空。运行时凭证必须由使用者填写或从原机器私下迁移，禁止加入源码和分发包。

## 迁移到另一台机器

分发的 `.so` 适用于 **Linux amd64 / x86-64、glibc 2.28 或更新**；`.dll` 适用于 **Windows amd64 / x86-64**。本轮 Linux 隔离验证使用 CPA **8.0.34（插件 RPC schema 6）**。其他版本需要兼容同一插件 ABI、管理与流式回调接口；仅看版本号更大不能保证兼容。ARM、macOS 和 musl/Alpine 需要对应平台重新编译。

1. 备份目标机配置。确认服务账号、工作目录、`plugins.dir` 和 `oauth.auth-dir`。
2. 将 `cpa-agentrouter-continuity-v0.4.9.so` 放入 `<plugins.dir>/linux/amd64/`。目录与文件必须由 CPA 服务账号可读；权限通常为目录 755、插件 644。
3. 在 CPA 插件管理中启用 **cpa-agentrouter-continuity**，完成热加载。无需为了安装插件修改现有供应商配置。
4. 打开插件页面，输入目标机 CPA 管理密钥，新增 AR API Key；或私下复制已有 AR 凭证到目标机认证目录。凭证应由服务账号拥有、权限 600。不要把认证目录打进公开安装包。
5. 核对目标机代理和域名分流。插件使用宿主全局代理，**不会自动复制原机器的 mihomo 配置，也不会自动保证非 ISP 出口**。若需要普通节点出口，须在目标机为 `agentrouter.org` 单独设置规则。
6. 用可选的临时模型前缀验证路由。确认正常后，自行决定是否保留前缀。没有前缀的模型可能参与同名模型的调度。

Windows 使用同一套配置步骤，将 `cpa-agentrouter-continuity-v0.4.9.dll` 放入 `<plugins.dir>/windows/amd64/`。Windows 版本使用系统 `LockFileEx` 保护会话状态，并以进程创建时间区分 PID 重用。

旧的 AR API 提供商条目可以在插件凭证已完整配置并验证后删除。不要同时启用旧条目和新插件凭证，否则旧条目仍可绕过插件。插件凭证独立保存 Key、模型和请求头，删除旧条目不会删除插件凭证。

### 可选文件方式

`examples/cpa-agentrouter-continuity.example.json` 提供不含密钥的模板。可在源码目录以外安全生成一个默认停用的运行时凭证：

```sh
python3 scripts/create_credential.py --output /secure/runtime/cpa-agentrouter-continuity.json
```

命令在终端隐藏输入 API Key，不接收命令行明文 Key。之后设置正确的服务账号属主，再导入 CPA。启用前核对模型和前缀。

`model_prefix` 默认空；填写 `ar` 时只发布 `ar/模型名`，不额外发布对应裸模型。不要同时叠加宿主通用 `prefix` 字段。`models[].ID` 是基础显示名称，`Name` 为上游模型名。

## 恢复规则与边界

- 默认额外重试 4 次，允许 0–10；等待预算默认 180 秒，最多 600 秒。
- 临时限流使用 10/20/40/60 秒退避；网关或早期断流使用 1/2/4/8/15 秒退避，并添加少量抖动。
- 尊重 Retry-After 及毫秒形式。预算限制后续重试，不中断已在正常生成中的响应。
- 已交付内容、工具事件或未知可能带副作用的事件后，不再自动重放。
- 加密 reasoning 验证失败、跨资源错误提示新建会话；不删除历史、不降级重试。
- 认证失败、余额/配额耗尽、普通参数错误不重试。
- 上游计费规则编译异常（`tiered expr run failed` 同时包含 `expr compile error`）返回 `ar_upstream_pricing_error`，即使上游包装为 HTTP 500 或 SSE 错误也不自动重试；已输出文本保留，及时释放名额。提示中保留未知变量名和 `trace_id`，便于上游维护者排查；客户端仍可自行发起新请求。
- `response.incomplete` 的原因为 `content_filter` 时，流式返回一次 `ar_content_filter` 终止错误（插件发出 `response.failed`，宿主可转换为 `error` 事件），非流式返回 HTTP 400；已输出文本保留，插件不重放请求。`max_output_tokens` 等其他未完成原因继续透传。该规则不能阻止客户端自行发送新请求，也不会解除上游内容过滤。
- 插件凭证的 CPA 外层 `request_retry` 为 0，避免与插件自身的重试轮次叠加；其他凭证不受影响。
- 首版完整验证范围是 HTTP/SSE。没有验证 WebSocket；压缩端点保守透传，未测试真实上游压缩任务。

这是故障恢复层，不是任意流中断后的无损断点续传。请求重试仍可能增加上游用量。

## 源码与构建

```text
abi.go             C ABI 边界
plugin.go          插件注册及执行入口
credential.go      AR 范围、默认请求头与模型配置
retry.go           分类、退避与预算
pricing_error.go   上游计费配置错误识别与脱敏诊断
transport.go       宿主 HTTP 回调
stream.go          SSE 交付与重放边界
management.go      鉴权管理接口、凭证与连接检测
monitor.go         脱敏请求记录
routing.go         会话准入、调度与名额预留
routing_store.go   持久化来源和跨版本名额锁
tool_alias.go      AR 工具命名空间转换与响应还原
web/               登录、API Key、日志与健康页面
internal/sdk/      宿主接口类型
scripts/           凭证生成与发布扫描
examples/          无密钥配置模板
tests/             UI 与隔离实例验证
```

Go 标准库与随附接口类型，无额外 Go 模块依赖。构建需要 Go 和 C 工具链：

```sh
make test
node --test tests/ui.test.cjs
make build
python3 scripts/check_release.py .
```

跨平台构建必须提供目标平台 C 编译器。Linux amd64 分发目标为 glibc 2.28，Windows 为 amd64 MinGW ABI。使用 Zig 时可在 Linux shell 中指定 `CC="zig cc -target x86_64-linux-gnu.2.28"` 后执行 `make build`；原生 GCC 默认链接构建机 glibc，不保证兼容更旧的 glibc。

隔离集成测试需要 Python 3、PyYAML、OpenSSL、curl 和 CPA 可执行文件。测试自建临时目录、mock 凭证与 TLS 上游，不读取生产配置或调用真实模型：

```sh
python3 tests/integration.py --cpa-binary /path/to/cli-proxy-api --plugin /path/to/cpa-agentrouter-continuity-v0.4.9.so
```

该测试在 Linux 上实际加载共享库，核对源码版本，覆盖限流/断流恢复、内容过滤终止、桌面工具往返、凭证管理、会话来源、并发名额、配置热加载和取消。`--expected-version` 可指定其他待验版本；`--previous-plugin` 可追加旧库热升级验证。测试结果与平台边界见 [VALIDATION.md](VALIDATION.md)。

独立 AR 探针可用 `go build -tags probe -o cpa-agentrouter-probe .` 构建；默认插件不包含探针入口。真实探针需自行提供运行时配置及调用授权。

## 更新与回滚

使用新版本文件名放入插件目录，然后通过 CPA 插件管理保存配置触发热加载。不要直接覆盖正在使用的动态库；保留旧文件用于回滚。热加载机制依赖宿主版本，先在隔离实例验证。

旧版本回滚时移走新版本文件，再通过管理面板重新加载旧版本。只改本插件配置，不覆盖整份历史 CPA 配置，不重启其他服务。停用插件前先停用对应 AR 凭证，避免无执行器路由。

第三方版权和许可原文集中保存在 `licenses/`、`internal/sdk/LICENSE` 和 `THIRD_PARTY_NOTICES.md`，不作为界面或产品文档的参考链接展示。

## 从旧插件迁移

本次统一更改了项目名、Go module、插件/provider 标识、管理接口路径、浏览器存储名称及状态文件名。这是一次标识不兼容的更新，不是把新文件直接放在旧插件旁即可替换。

现有运行实例不会被仓库发布自动修改。迁移前备份凭证和会话状态，等待旧插件没有在途请求后再切换；不要让旧、新两个调度插件同时启用。

1. 在隔离实例先验证新包，再停用旧插件的 AR 凭证与旧插件。
2. 将私有凭证的 `type` 改为 `cpa-agentrouter-continuity`，模型 `OwnedBy` 改为相同值；保留原 `id`、API Key、名称、优先级、权重、模型前缀与其他配置。新凭证文件使用新项目前缀，避免重复导入。
3. 如要保留会话来源，将旧来源状态文件复制为 `plugins/cpa-agentrouter-continuity-sessions.json`；将 `origins` 中旧 AR provider 值转换为 `cpa-agentrouter-continuity`，保持非 AR provider 与会话哈希不变。只在确认无在途请求后清空 `leases`，不要删除旧备份。文件由 CPA 服务账号拥有，权限 600。
4. 安装新动态库，在 `plugins.configs.cpa-agentrouter-continuity` 启用它，再按需启用迁移后的凭证。旧库留在插件目录外作为回滚备份。
5. 新管理页面可复用主面板记住的管理密钥；旧插件自身的浏览器记住记录不会自动迁移。确认模型目录、状态与出口正确后再使用。

如果没有迁移会话来源，已有带历史的会话会保守绕开 AR，新建会话可按准入规则使用。迁移材料含私有密钥时必须单独安全传输，不能提交到仓库或上传 Release。

## 打包 Release

先提交或暂存需要分发的源码，然后分别构建目标平台插件：

```sh
make build
python3 scripts/package_release.py \
  --binary dist/linux/amd64/cpa-agentrouter-continuity-v0.4.9.so \
  --output-dir dist/release

python3 scripts/package_release.py \
  --platform windows-amd64 \
  --binary dist/windows/amd64/cpa-agentrouter-continuity-v0.4.9.dll \
  --output-dir dist/release
```

打包脚本只纳入 Git 跟踪的源码与指定二进制，运行密钥检查，并生成 ZIP 内的文件校验清单和 ZIP 的 SHA256 校验文件。Release 不包含运行时凭证、会话来源状态、请求日志或部署主机信息。
