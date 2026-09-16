# CmdPilot 架构文档

版本：0.1.0 ｜ 更新：2026-09-15

## 1. 总体架构

```
┌─────────────────────────────┐   ┌──────────────────────────────┐
│  PowerShell 终端             │   │  CMD + Clink                 │
│  PSReadLine Predictor 插件   │   │  cmdpilot.lua generator      │
│  (PS 7.4+ Subsystem API;     │   │  (Tab 菜单补全, 无幽灵文本)    │
│   PS 5.1 无插件 API → Tab)   │   │                              │
└──────────────┬──────────────┘   └───────────────┬──────────────┘
               │ JSON request/output file          │ 行协议 (plain)
               ▼                                  ▼
        ┌───────────────────────────────────────────────┐
        │  companion 二进制（cmdpilot-clink / 模块内）    │
        │  --request/--output/--plain/--report[-batch]   │
        └──────────────────────┬────────────────────────┘
                               │ HTTP loopback 127.0.0.1:随机端口
                               │ Bearer token (32B hex) 鉴权
                               ▼
        ┌───────────────────────────────────────────────┐
        │  CmdPilot 守护进程（cmdpilot daemon）          │
        │  internal/server: /complete /report /recommend │
        │  /stats /ai/test /health /shutdown            │
        └──────┬────────────────────────────┬───────────┘
               ▼                            ▼
        ┌───────────────┐          ┌───────────────────┐
        │  SQLite (WAL) │          │  knowledge 库     │
        │  usage_stats  │          │  492 条 (embed)   │
        │  favorites    │          │  cmd=179/ps=224/  │
        │  user_commands│          │  external=93      │
        │  recent_cmds  │          └───────────────────┘
        └───────────────┘
```

## 2. 分层

| 层 | 目录 | 职责 | 依赖 |
|---|---|---|---|
| 终端适配 | `adapters/powershell`、`adapters/clink` | 与 PSReadLine / Clink API 对接 | 仅 HTTP/JSON/行协议 |
| 伴侣进程 | `cmd/cmdpilot-clink` | 终端↔守护进程桥（JSON 或行协议、批量上报） | 无 core 依赖 |
| 守护进程 | `cmd/cmdpilot` + `internal/server` | 常驻 HTTP 服务、补全编排 | core 全部 |
| 核心 | `internal/{completion,ai,match,rank,sanitize,db,config,secrets,knowledge}` | 纯逻辑，零终端依赖 | 无 |
| 独立 CLI | `internal/cli` | 收藏/统计/配置/补全测试/重建索引 | core |
| 工具 | `tools/gendb`、`tools/eval` | 知识库生成、七合一评测 | core |

核心层（internal/*）**不 import 任何终端相关 API**；终端差异全部收敛在
适配层与 companion 的行协议/JSON 契约中。

## 3. 关键数据流

### 3.1 补全（幽灵文本 / 列表）
```
用户输入 → Predictor.GetSuggestion → companion --request file → daemon /complete
→ completion.Engine.Complete：
   ① 上下文分析（命令名/参数/子命令/路径/是否 git 仓库）
   ② 本地候选：knowledge + favorites + user_commands + 历史 + 链式推荐
   ③ 评分排序（rank 公式，见 ADR-003）
   ④ AI（hybrid/ai 模式）：异步 debounce 300ms，1 in-flight 锁，
      同前缀缓存 5min；失败静默降级为本地结果
→ 返回 Top 建议 + List
```

### 3.2 统计上报
```
PowerShell: OnCommandLineExecuted / Clink: onbeginedit → companion --report
→ daemon /report → usage_stats upsert + recent_cmds 追加（事务）
```

### 3.3 AI 请求（OpenAI 兼容协议）
```
POST {base_url}/v1/chat/completions
Body: model / temperature / max_tokens / messages:
  system: 角色 + 终端类型 + 只返回补全后缀
  user:   当前输入 + 目录 + 最近 10 条历史（已脱敏）+ 本地 Top-5 参考
key 经 DPAPI 加密存储（config.ai.api_key_encrypted），仅内存解密。
```

## 4. 降级链（AI 永不成为单点）

```
AI 成功 → AI 后缀 + 本地列表
AI 超时(默认5s)/500/断网/401/畸形响应 → 本地引擎结果（ai_used=false）
本地引擎异常 → 纯历史前缀匹配（history candidates）
任何一环失败 → 终端本身始终可正常使用（插件异步、绝不阻塞输入）
```

## 5. 存储

- SQLite（modernc.org/sqlite 纯 Go，免 CGO）：WAL + synchronous=NORMAL +
  busy_timeout + user_version 迁移（当前 v1）
- 表：usage_stats / recent_cmds / favorites / user_commands / meta
- 位置：Windows `%LOCALAPPDATA%\CmdPilot\cmdpilot.db`；Linux
  `$XDG_DATA_HOME/cmdpilot` 或 `~/.local/share/cmdpilot`
- 配置：同目录 config.json（损坏自动备份 `config.json.bak-<ts>` + 回退默认）
- key：DPAPI（Windows）或 dev: 前缀占位（非 Windows 开发环境）

## 6. 并发与线程模型

- 守护进程：单 HTTP server，/complete 同步返回本地结果，AI 请求 goroutine
  异步执行 + `sync.Mutex` 单 in-flight 锁 + LRU 缓存（TTL 5min）
- PowerShell 插件：worker 独立 runspace（纯脚本，不调类方法），共享
  hashtable + Monitor 锁，150ms×2 debounce，快照式交换
- companion：`--plain` 一次请求一次进程；`--report-batch` 批量合并上报

## 7. 安全

- 守护进程仅监听 127.0.0.1，Bearer token 鉴权（/health 除外）
- key 不落明文；历史脱敏（key/token/password/secret/Bearer 行剔除）
- 无任何网络遥测；唯一出网请求为配置的 AI base_url（`cmdpilot self-check` 可查证）
- 日志不含 key、不含历史原文（只记元数据），单文件 ≤10MB 旋转保留 3 个
