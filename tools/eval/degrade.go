package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/ai"
	"github.com/qiutuan/CmdPilot/internal/completion"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
)

// degradeCase describes one AI failure injection.
type degradeCase struct {
	Name   string
	Server func() *httptest.Server
	// Overrides applied to the AI client config.
	BaseURL string
	Key     string
	Timeout int
	// MalformedRawBody disables the HTTP server and uses a raw listener stub.
}

func runDegrade() error {
	kb, err := knowledge.Load()
	if err != nil {
		return err
	}

	var stores []*db.DB
	defer func() {
		for _, st := range stores {
			_ = st.Close()
		}
	}()
	newEngine := func(cfg *config.Config, baseURL, key string, timeoutMS int) *completion.Engine {
		dir, _ := tempDir("degrade")
		store, _ := db.Open(dir)
		stores = append(stores, store)
		aiClient := ai.New(baseURL, key, "mock", time.Duration(timeoutMS)*time.Millisecond)
		e := completion.New(kb, store, cfg, aiClient, nil)
		return e
	}

	cases := []struct {
		name    string
		url     string
		key     string
		timeout int
		verify  func(*httptest.Server) // optional post-checks
		server  *httptest.Server
	}{
		{
			name:    "超时（AI 10s 不响应，客户端 1s 超时）",
			timeout: 1000,
			server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(10 * time.Second)
			})),
		},
		{
			name:    "HTTP 500",
			timeout: 3000,
			server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			})),
		},
		{
			name:    "断网（连接被拒绝）",
			timeout: 2000,
			url:     "http://127.0.0.1:1/v1",
		},
		{
			name:    "无效 key（401）",
			timeout: 3000,
			key:     "FAKE_INVALID_KEY",
			server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			})),
		},
		{
			name:    "畸形响应（非 JSON 垃圾字节）",
			timeout: 3000,
			server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("<html>not json at all</html>"))
			})),
		},
	}

	cfg := config.Default()
	cfg.Engine = config.EngineHybrid
	cfg.AI.TimeoutMS = 2000
	cfg.AI.MaxTokens = 64

	rows := make([]string, 0, len(cases))
	pass := 0
	type row struct {
		Case    string `json:"case"`
		TopFull string `json:"top_full"`
		AIUsed  bool   `json:"ai_used"`
		Err     string `json:"error"`
	}
	var jsonRows []row
	for _, c := range cases {
		url := c.url
		key := c.key
		if c.server != nil {
			defer c.server.Close()
			if url == "" {
				url = c.server.URL
			}
		}
		if url == "" {
			url = "http://127.0.0.1:1/v1"
		}
		if key == "" {
			key = "FAKE_KEY"
		}
		e := newEngine(cfg, url, key, c.timeout)
		resp, err := e.Complete(completion.Request{
			Input: "git st", Shell: "cmd", CWD: "", History: []string{"git add ."},
			WaitAIMS: c.timeout, // force the AI path to actually run
		})
		// 降级断言：无论 AI 如何失败，都必须返回本地结果且不报错。
		ok := true
		msg := ""
		switch {
		case err != nil:
			ok, msg = false, err.Error()
		case resp == nil || resp.Top == nil:
			ok, msg = false, "resp nil or top nil"
		case resp.AIUsed:
			ok, msg = false, "AIUsed=true 不应出现在降级结果中"
		case !strings.Contains(resp.Top.Full, "git"):
			ok, msg = false, "本地结果异常: "+resp.Top.Full
		}
		if ok {
			pass++
		}
		topFull := ""
		if resp != nil && resp.Top != nil {
			topFull = resp.Top.Full
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %v | %s |", c.name, topFull, func() bool {
			if resp != nil {
				return resp.AIUsed
			}
			return false
		}(), map[bool]string{true: "✅", false: "❌ " + msg}[ok]))
		jsonRows = append(jsonRows, row{Case: c.name, TopFull: topFull, AIUsed: func() bool {
			if resp != nil {
				return resp.AIUsed
			}
			return false
		}(), Err: msg})
	}

	md := fmt.Sprintf(`# AI 降级测试矩阵报告

执行时间：%s

## 矩阵

| 故障注入 | 实际 Top 建议 | AIUsed | 判定 |
|---|---|---|---|
%s

## 结论
五种 AI 故障（超时 / HTTP 500 / 断网 / 无效 key / 畸形响应）下，本地引擎全部兜底成功：
- 每次 /complete 均返回本地 Top 建议（git 相关），`+"`ai_used=false`"+`；
- 无任何错误冒泡到调用方 —— 终端输入永不因 AI 故障受阻。

通过 **%d / %d**。
`, time.Now().Format("2006-01-02 15:04:05"), strings.Join(rows, "\n"), pass, len(cases))

	jsonData, _ := json.MarshalIndent(map[string]any{
		"pass": pass, "total": len(cases), "rows": jsonRows, "date": time.Now().Format(time.RFC3339),
	}, "", "  ")
	if pass != len(cases) {
		return fmt.Errorf("degradation matrix: %d/%d passed", pass, len(cases))
	}
	return writeReport("degradation", md, jsonData)
}
