# ADR-006：内置知识库存储格式（压缩 JSON）

- 状态：已接受
- 日期：2026-09-15

## 背景
内置命令库（≥150 CMD + ≥200 PS + ≥50 external）需要随版本发布、
用户不可改但可覆盖、体积小。

## 决策
**压缩 JSON（gzip）**：`internal/knowledge/data/commands.json.gz`（23.8KB，
492 条），运行时解压加载；`tools/gendb` 从可读源（cmd.json/ps.json/
external.json）重新生成。**不选只读 SQLite 内置表**。

## 理由
- 只读内置表需要随安装包携带 .db 文件或首次启动建库，安装体积与
  启动成本更高；gzip JSON 由 Go embed 打包，启动加载 <1ms；
- 知识库是纯静态数据，无查询索引需求（运行时已在内存索引）；
- 用户覆盖走独立 user_commands 表，与内置库天然隔离。

## 后果
- 更新知识库 = 重新生成 gz + 发版；不破坏用户数据。
