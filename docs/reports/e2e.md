# E2E 关键路径自动化测试报告

执行时间：2026-09-15 19:29:52
通过 **12 / 12**

| 结果 | 路径 | 证据 |
|---|---|---|
| ✅ | daemon ensure → health | CmdPilot 0.1.0 (commit dev, built unknown) |
| ✅ | 输入前缀 'git st' → 建议 | git stash |
| ✅ | 执行上报 → 统计可见 | 1. git stash                                   1 次  最近 09-15 19:29  目录: /tmp/cmdpilot-e2edata-1210318323/cmdpilot |
| ✅ | 空输入 → 频率推荐 | git stash |
| ✅ | 收藏命令参与补全 | git push origin main && npm run build (favorite) |
| ✅ | 配置 set/get | local |
| ✅ | 收藏导出/导入合并 | /tmp/cmdpilot-e2e-2597739162/favorites-export.json |
| ✅ | AI 测试连接（mock） | mock-llm |
| ✅ | 自检输出网络声明 | CmdPilot 自检报告; =================; 版本: CmdPilot 0.1.0 (commit dev, built unknown); 平台: linux/amd64; 配置: 正常; 引擎模式: hybrid(local+AI) / 触发模式: auto; AI 端点: http://127.0.0.1:39553 (model=mock-llm); 知识库: 496 条命令 (cmd=179 ps=224 external=93); 数据库: 完整性 ok, 位置 /tmp/cmdpilot-e2edata-1210318323/cmdpilot/cmdpilot.db; 守护进程: 运行中 CmdPilot 0.1.0 (commit dev, built unknown); 网络: 本工具发起的所有网络请求仅为用户配置的 AI base_url: http://127.0.0.1:39553; |
| ✅ | 重启后统计持久 | 1. git stash                                   1 次  最近 09-15 19:29  目录: /tmp/cmdpilot-e2edata-1210318323/cmdpilot |
| ✅ | 清空统计保留收藏 | #1  deploy-all  [deploy]  git push origin main && npm run build; 共 1 条收藏 |
| ✅ | 无效 AI 端点 → 本地兜底 | git stash |

> 运行方式：`go run ./tools/eval e2e`（真实守护进程二进制 + HTTP 客户端 + CLI 子进程）。
