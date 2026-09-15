# ADR-004：AI Key 安全存储

- 状态：已接受
- 日期：2026-09-15

## 背景
用户提供 OpenAI 兼容 API key；不能明文落盘，也不能依赖第三方服务。

## 决策
- Windows：**DPAPI（CryptProtectData / CryptUnprotectData）**加密后写入
  `config.json` 的 `ai.api_key_encrypted`（current-user scope，无需管理员）。
- 非 Windows（开发/测试）：`dev:` 前缀 + base64 占位（明确标注无真实加密，
  仅用于开发环境，产品仅面向 Windows）。
- 环境变量 `CMDPILOT_AI_API_KEY` 仅内存覆盖（不落盘），优先级高于文件。
- `cmdpilot config get ai.api_key` 只输出 `(set, hidden)` / `(not set)`。

## 理由
- DPAPI 是 Windows 内置、免额外依赖、用户级加密的标准方案；
- 环境变量覆盖便于 CI/临时使用且天然不进配置文件。

## 后果
- 备份/迁移配置时 key 与机器绑定（DPAPI user scope）；
- 卸载 -RemoveData 时一并清除。
