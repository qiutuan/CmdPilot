# 脱敏测试报告（历史敏感信息绝不进入 AI prompt）

执行时间：2026-09-15 19:29:45

## 注入的敏感样本（7 条）

sk-FAKE-SECRET-KEY-1234567890abcdef
- AKIAFAKESECRETACCESSKEY
- ghp_FAKESECRETTOKEN1234567890
- Bearer FAKEBEARERTOKEN
- password=super-secret-pass
- token: abcdef0123456789
- api_key=FAKE_API_KEY_VALUE

## 捕获验证

- Mock AI 服务器收到请求数：**1**
- 请求体中出现敏感样本数：**0**
- 判定：**PASS**

## 结论

含 api_key / password / Bearer / AWS key / GitHub token 的历史行全部被
脱敏层剔除，AI prompt 中仅保留干净的命令上下文与本地 Top-5 参考。

本地 Top 建议（AI 成功时）：git --safe-suffix
