# CmdPilot 配置说明

配置文件：`config.json`，位于数据目录（Windows `%LOCALAPPDATA%\CmdPilot`；
Linux `$XDG_DATA_HOME/cmdpilot` 或 `~/.local/share/cmdpilot`）。
单文件 JSON + 环境变量覆盖（前缀 `CMDPILOT_`）。

## 配置项

| 路径 | 默认 | 说明 |
|---|---|---|
| `engine` | `hybrid` | 引擎模式：`local` / `ai` / `hybrid` |
| `trigger` | `auto` | AI 触发：`auto`（自动） / `tab`（仅 Tab 触发 AI） |
| `ai.base_url` | 空 | OpenAI 兼容 API 根地址（POST {base_url}/v1/chat/completions） |
| `ai.api_key` | 空 | API key，**经 DPAPI 加密**后存 `ai.api_key_encrypted` |
| `ai.model` | 空 | 模型名，如 `gpt-4o-mini` |
| `ai.temperature` | `0.2` | 采样温度 [0,2] |
| `ai.max_tokens` | `64` | 生成补全后缀的最大 token 数 |
| `ai.timeout_ms` | `5000` | AI 请求超时（ms），超时静默降级本地 |
| `debounce_ms` | `300` | AI debounce 窗口（ms） |
| `ai_cache_ttl_minutes` | `5` | 同前缀 AI 结果缓存 TTL（分钟） |
| `history_lines` | `10` | 送入 AI prompt 的最近历史行数（先脱敏） |
| `recommend_weighting` | `true` | 频率/上下文加权推荐开关 |
| `enable_prompt_line` | `true` | 插件首次加载时打印一行启用提示（模式：本地/AI/混合） |
| `log_enabled` | `true` | 本地日志开关 |
| `log_level` | `info` | `debug`/`info`/`warn`/`error` |

## 环境变量覆盖

- `CMDPILOT_AI_API_KEY`：仅内存覆盖 key，**不落盘**（优先级高于文件）。
- 其他 `CMDPILOT_*` 环境变量按配置路径映射（如 `CMDPILOT_ENGINE`）。

## 常用命令

```powershell
cmdpilot config get engine
cmdpilot config set engine local
cmdpilot config set ai.api_key sk-...     # 自动加密
cmdpilot config set ai.model gpt-4o-mini
cmdpilot config show
```

## 损坏恢复

配置文件 JSON 损坏时：自动备份为 `config.json.bak-<时间戳>`，回退默认
配置并打印警告，**绝不启动失败**（config 包 80.3% 覆盖率含此路径测试）。

## 日志

- 位置：数据目录 `logs/cmdpilot-*.log`（`log_enabled=true`）
- 旋转：单文件 ≤10MB，保留 3 个
- 内容：启动、AI 请求结果元数据（不含 key、不含历史原文）
- 无网络遥测；唯一出网请求为用户配置的 AI base_url（`cmdpilot self-check` 验证）
