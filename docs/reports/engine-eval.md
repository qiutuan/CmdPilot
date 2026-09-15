# 引擎质量评测报告（本地引擎 Top-5 命中率）

- 评测集规模：**231 条**真实使用场景（CMD 内建 / PowerShell Cmdlet / 外部工具 git·npm·pip·docker·go·kubectl 等，含前缀、子串、编辑距离容错）
- 判定标准：期望命令（1~3 个均可）出现在本地引擎 Top-5 即命中
- 执行时间：2026-09-15 18:55:27

## 结果

| 指标 | 数值 | 达标线 |
|---|---|---|
| Top-5 命中 | **227 / 231 (98.3%)** | ≥ 90% |
| Top-1 命中 | 224 / 231 (97.0%) | — |

## 未命中明细
- `wf` [cmd] 期望 wf.msc 实际 Top5=waitfor | powercfg
- `gpedit` [cmd] 期望 gpedit.msc 实际 Top5=gpedit 
- `git rm` [ps] 期望 git rm 实际 Top5=git remote
- `git mv` [cmd] 期望 git mv 实际 Top5=

## 结论
本地引擎 Top-5 命中率 **98.3%**，达到 目标线（≥90%）。
