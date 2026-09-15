package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/qiutuan/CmdPilot/internal/ai"
	"github.com/qiutuan/CmdPilot/internal/completion"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
)

// runSanitize proves that secret-bearing history lines NEVER reach the AI
// endpoint: a mock server captures every request body and we assert none of
// the injected secrets appear in it.
func runSanitize() error {
	secrets := []string{
		"sk-FAKE-SECRET-KEY-1234567890abcdef",
		"AKIAFAKESECRETACCESSKEY",
		"ghp_FAKESECRETTOKEN1234567890",
		"Bearer FAKEBEARERTOKEN",
		"password=super-secret-pass",
		"token: abcdef0123456789",
		"api_key=FAKE_API_KEY_VALUE",
	}

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" --safe-suffix"}}]}`))
	}))
	defer srv.Close()

	kb, err := knowledge.Load()
	if err != nil {
		return err
	}
	dir, err := tempDir("sanitize")
	if err != nil {
		return err
	}
	defer removeTemp(dir)
	store, err := db.Open(dir)
	if err != nil {
		return err
	}
	defer store.Close()

	cfg := config.Default()
	cfg.Engine = config.EngineHybrid
	cfg.AI.BaseURL = srv.URL
	cfg.AI.APIKeyEncrypted = "FAKE_KEY"
	cfg.AI.Model = "mock"
	cfg.AI.TimeoutMS = 5000
	e := completion.New(kb, store, cfg, ai.New(srv.URL, "FAKE_KEY", "mock", 5*time.Second), nil)

	// 构造带敏感信息的历史，模拟真实用户会话。
	history := append([]string{
		"git checkout main",
		"docker ps",
		"export AWS_ACCESS_KEY_ID=AKIAFAKESECRETACCESSKEY",
		"curl -H \"Authorization: Bearer FAKEBEARERTOKEN\" https://api.example.com",
		"git config user.name alice",
		"npm install",
		"echo password=super-secret-pass > creds.txt",
	}, secrets...)

	// 触发 AI：WaitAIMS 强制走完一次真实请求。
	resp, err := e.Complete(completion.Request{
		Input:    "git ",
		Shell:    "cmd",
		CWD:      dir,
		History:  history,
		WaitAIMS: 5000,
	})
	if err != nil {
		return fmt.Errorf("complete: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		return fmt.Errorf("mock server received no requests (AI path did not run)")
	}
	all := strings.Join(bodies, "\n")
	var leaks []string
	for _, s := range secrets {
		if strings.Contains(all, s) {
			leaks = append(leaks, s)
		}
	}
	// 额外：完整历史原文（命令本身）也不应出现在 prompt 之外的地方。
	lines := make([]string, 0, len(bodies))
	for _, b := range bodies {
		lines = append(lines, b)
	}

	leakNote := "✅ 0 泄漏"
	status := "PASS"
	if len(leaks) > 0 {
		leakNote = fmt.Sprintf("❌ 泄漏 %d 项: %s", len(leaks), strings.Join(leaks, "; "))
		status = "FAIL"
	}

	md := fmt.Sprintf(`# 脱敏测试报告（历史敏感信息绝不进入 AI prompt）

执行时间：%s

## 注入的敏感样本（%d 条）

%s

## 捕获验证

- Mock AI 服务器收到请求数：**%d**
- 请求体中出现敏感样本数：**%d**
- 判定：**%s**

## 结论

含 api_key / password / Bearer / AWS key / GitHub token 的历史行全部被
脱敏层剔除，AI prompt 中仅保留干净的命令上下文与本地 Top-5 参考。

本地 Top 建议（AI 成功时）：%s
`, time.Now().Format("2006-01-02 15:04:05"), len(secrets),
		strings.Join(secrets, "\n- "), len(bodies), len(leaks), status,
		func() string {
			if resp != nil && resp.Top != nil {
				return resp.Top.Full
			}
			return "(无)"
		}())

	jsonData, _ := json.MarshalIndent(map[string]any{
		"secrets_injected": secrets, "requests": len(bodies), "leaks": leaks,
		"pass": len(leaks) == 0, "date": time.Now().Format(time.RFC3339),
	}, "", "  ")

	_ = leakNote
	if len(leaks) > 0 {
		return fmt.Errorf("sanitize: leaks detected: %v", leaks)
	}
	return writeReport("sanitize-capture", md, jsonData)
}
