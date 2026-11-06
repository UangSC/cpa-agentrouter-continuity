# 开发、测试与发布

[返回项目首页](../README.md)

所有命令均在仓库根目录执行。Go 插件入口为 `./cmd/ar-continuity`，网页由 `web` 包嵌入，HTTP 资源路径保持兼容。

Go 标准库与随附接口类型，无额外 Go 模块依赖。构建需要 Go 和 C 工具链：

```sh
make test
node --test tests/web/ui.test.cjs tests/web/dashboard.test.cjs
make build
python3 scripts/check_release.py --git .
```

浏览器验收运行 `node tests/web/dashboard.browser.cjs` 与 `node tests/web/monitoring.browser.cjs`，需要可用的 Playwright；可通过 `PLAYWRIGHT_PATH` 指定已有包路径、`BROWSER_PATH` 指定浏览器程序。测试使用独立的模拟接口，不访问生产凭证或上游，截图写入 `dist/preview/`，并标注为演示数据。

跨平台构建必须提供目标平台 C 编译器。Linux amd64 分发目标为 glibc 2.28，Windows 为 amd64 MinGW ABI。使用 Zig 时可在 Linux shell 中指定 `CC="zig cc -target x86_64-linux-gnu.2.28"` 后执行 `make build`；原生 GCC 默认链接构建机 glibc，不保证兼容更旧的 glibc。

隔离集成测试需要 Python 3、PyYAML、OpenSSL、curl 和 CPA 可执行文件。测试自建临时目录、mock 凭证与 TLS 上游，不读取生产配置或调用真实模型：

```sh
python3 tests/integration/run.py --cpa-binary /path/to/cli-proxy-api --plugin /path/to/cpa-agentrouter-continuity-v0.5.2.so
```

该测试在 Linux 上实际加载共享库，核对源码版本，覆盖限流/断流恢复、内容过滤终止、桌面工具往返、凭证管理、会话来源、并发名额、配置热加载和取消。`--expected-version` 可指定其他待验版本；`--previous-plugin` 可追加旧库热升级验证。测试结果与平台边界见 [验证记录](validation.md)。

独立 AR 探针可用 `go build -tags probe -o cpa-agentrouter-probe ./cmd/ar-continuity` 构建；默认插件不包含探针入口。真实探针需自行提供运行时配置及调用授权。

## 打包 Release

先提交需要分发的源码，并保持工作区干净，然后分别构建目标平台插件：

```sh
make build
python3 scripts/package_release.py \
  --binary dist/linux/amd64/cpa-agentrouter-continuity-v0.5.2.so \
  --output-dir dist/release

python3 scripts/package_release.py \
  --platform windows-amd64 \
  --binary dist/windows/amd64/cpa-agentrouter-continuity-v0.5.2.dll \
  --output-dir dist/release
```

打包脚本要求已提交的干净工作区，只从当前提交读取源码，拒绝运行文件与构建缓存，并核对插件文件名、平台格式和内嵌网页。ZIP 包含 `SOURCE_COMMIT.txt`、内部文件校验清单和外部 `.sha256` 文件。Release 不包含运行时凭证、会话来源状态、请求日志或部署主机信息。

## 仓库审计

```sh
python3 scripts/check_release.py --git .
python3 scripts/audit_repository.py --history --report build/audit.json
python3 scripts/audit_repository.py --history --releases-dir dist/release --report build/release-audit.json
```

历史范围为本地已获取的全部分支与标签，扫描每个可达提交中的路径和文件内容；检查 GitHub 前先获取远端 refs。Release 审计在内存中读取 ZIP 成员，验证外部 SHA-256，不解压不可信路径。报告只包含路径和规则，不包含疑似密钥值。模式扫描不能证明任意格式的秘密都不存在。

GitHub Actions 在主分支推送、版本标签和 PR 上执行 Go / Node / Python 检查、源码和历史扫描及共享库构建。CI 构建只用于验证；兼容 glibc 2.28 的发布库仍需使用指定目标 C 工具链。真实环境脚本和生成请求不在自动检查中执行。
