# 仓库审计

[返回项目首页](../README.md)

## 2026-11-06：0.5.2 发布前

目标是公开仓库 `UangSC/cpa-agentrouter-continuity`。先通过 GitHub API 列举分支、标签和 Release，再获取远端 refs，检查当前源码、所有可达提交和附件。未改写或删除历史。

| 范围 | 检查结果 |
| --- | --- |
| 远端分支 | `main`，已核对远端最新提交 |
| 既有标签 | `v0.4.0`、`v0.4.1` |
| 既有 Git 历史 | 8 个提交、120 个不同的文件内容版本，扫描每个历史路径 |
| 既有 Release | 2 个 Release，共 3 个 ZIP、137 个归档文件成员 |
| 附件完整性 | 3 个 ZIP 均与 GitHub 同名 `.sha256` 一致 |
| 密钥检查 | 未发现规则命中的 API Key、GitHub Token、私钥、JWT、AWS / Google Key 或非空真实凭证字段 |
| 多余内容 | 已有 Git 历史和 Release 未发现被规则判定为运行配置、缓存、日志、会话状态或本机备份的文件 |

检查保留明确以 `mock-` / `test-` 标识的测试值和空白配置模板。API 域名、公开仓库地址、标准协议字段和模拟上游数据属于项目需要的内容。

## 本次整理

- Go 插件与测试移到 `cmd/ar-continuity/`，网页仍在 `web/`，通过独立 Go 包显式嵌入资源。
- 前端、隔离集成和人工诊断分到 `tests/web/`、`tests/integration/`、`tests/manual/`；README 缩短，详细说明移到 `docs/`。
- `.gitignore` 排除运行配置、认证目录、状态、备份、依赖、截图、构建文件和本地验收资料；本地现有资料保留在忽略目录。
- `check_release.py` 增加 Token、私钥、嵌套 JSON 凭证和字面量检查；`audit_repository.py` 扩展到历史与附件，报告不打印疑似密钥原文。
- 打包要求干净提交，直接读取提交内容，校验内嵌网页、记录源码提交和文件 SHA-256；二进制只作为 Release 附件分发。
- GitHub Actions 持续验证源码、历史、Go / Node / Python 测试和共享库构建。

## 验证边界

本记录是自动模式扫描和文件结构检查的结果，不是任意秘密格式都不存在的证明。范围包括 GitHub 当时公开的分支、标签和 Release 附件；不包括服务器已不可达对象、其他仓库、用户私有配置或未提供的附件。没有发现需要撤销密钥或改写历史的证据。

原始脱敏清单、附件副本与执行报告只保存在本地 `build/repository-audit/`，不进入源码或 Release。
