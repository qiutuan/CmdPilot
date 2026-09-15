# CmdPilot CLI 文档

`cmdpilot` 是核心层的独立命令行入口（脱离终端也可用），二进制由
`go build ./cmd/cmdpilot` 构建。

## 全局

```
cmdpilot <command> [args]
cmdpilot help | version | self-check | privacy
```

## 命令总览

| 命令 | 说明 |
|---|---|
| `cmdpilot` / `help` | 用法帮助 |
| `version` | 版本、commit、构建时间 |
| `self-check` | 自检：数据目录、DB 完整性、守护进程、并输出网络声明 |
| `privacy` | 隐私说明与统计文件位置 |
| `daemon start\|stop\|status\|ensure` | 守护进程生命周期 |
| `config get\|set\|show <path>` | 配置读写（key 走加密存储） |
| `ai test` | 测试 AI 连接并打印模型名 |
| `favorite list\|add\|get\|update\|delete\|search\|export\|import` | 收藏管理 |
| `command add\|list\|delete` | 用户自定义命令覆盖（知识库补充） |
| `stats top [n]\|trend [days]\|distribution\|clear\|location` | 使用统计 |
| `complete --input <s> [--shell ps\|cmd] [--history a\|b] [--json]` | 测试补全 |
| `recommend [--cwd <dir>] [--json]` | 频率推荐（空输入推荐） |
| `history [n]` | 查看最近执行历史 |

## 示例

```powershell
# 配置
cmdpilot config set engine hybrid            # local | hybrid | ai
cmdpilot config set trigger auto             # auto | tab
cmdpilot config set ai.base_url https://api.openai.com/v1
cmdpilot config set ai.api_key sk-...        # DPAPI 加密存储
cmdpilot config set ai.model gpt-4o-mini
cmdpilot config set ai.timeout_ms 5000
cmdpilot config show

# 收藏
cmdpilot favorite add --name deploy --command "git push origin main && npm run build" --tags deploy,ci
cmdpilot favorite list
cmdpilot favorite export favs.json
cmdpilot favorite import favs.json --conflict skip   # skip|overwrite|rename

# 统计
cmdpilot stats top 10
cmdpilot stats trend 14
cmdpilot stats distribution
cmdpilot stats location          # 隐私：查看统计文件位置
cmdpilot stats clear             # 清空统计（保留收藏）

# 补全测试（脱离终端）
cmdpilot complete --input "git st" --shell cmd --json
cmdpilot complete --input "" --shell ps --history "git add .|git status" --json
cmdpilot recommend --json

# 守护进程
cmdpilot daemon ensure
cmdpilot daemon status
cmdpilot daemon stop

# 自检 / AI 连接
cmdpilot self-check
cmdpilot ai test
```

## 退出码

- `0` 成功
- `1` 业务失败（配置错误、连接失败等）
- `2` 用法错误（缺参数、未知子命令/标志）
