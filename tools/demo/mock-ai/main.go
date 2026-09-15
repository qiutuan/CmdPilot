// mock-ai 是一个极小的 OpenAI 兼容 mock server，供演示/测试使用，
// 不消耗真实 API 额度。构建：go build ./tools/demo/mock-ai
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// 只返回一个短后缀（演示用；真实 API 返回模型生成的后缀）
		_ = fmt.Sprintf("%+v", req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"mock-gpt","choices":[{"message":{"content":" --tail 100"}}]}`))
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	// 忽略监听错误：演示进程被用户手动结束
	_ = http.ListenAndServe("127.0.0.1:18888", nil)
}
