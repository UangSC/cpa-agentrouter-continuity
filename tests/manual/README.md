# 真实环境诊断

本目录不属于默认测试或 CI。脚本可能读取真实服务配置、请求日志并发送生成请求；只有操作者明确选择目标及允许真实请求后才运行。

`windows_live_tools.py` 接收 `--core-root` 和 `--error-log`，针对既有桌面工具定义验证名称还原与模拟结果往返。脚本不执行模型返回的工具调用。配置、日志、凭证与输出不得加入源码或 Release。

常规验证使用 `tests/integration/run.py` 和 `tests/web/` 的模拟环境。
