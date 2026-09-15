# AI 降级测试矩阵报告

执行时间：2026-09-15 19:43:26

## 矩阵

| 故障注入 | 实际 Top 建议 | AIUsed | 判定 |
|---|---|---|---|
| 超时（AI 10s 不响应，客户端 1s 超时） | git stash | false | ✅ |
| HTTP 500 | git stash | false | ✅ |
| 断网（连接被拒绝） | git stash | false | ✅ |
| 无效 key（401） | git stash | false | ✅ |
| 畸形响应（非 JSON 垃圾字节） | git stash | false | ✅ |

## 结论
五种 AI 故障（超时 / HTTP 500 / 断网 / 无效 key / 畸形响应）下，本地引擎全部兜底成功：
- 每次 /complete 均返回本地 Top 建议（git 相关），`ai_used=false`；
- 无任何错误冒泡到调用方 —— 终端输入永不因 AI 故障受阻。

通过 **5 / 5**。
