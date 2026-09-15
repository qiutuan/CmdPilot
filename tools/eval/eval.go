package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/completion"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
)

// evalc is one scenario: the input line, the terminal, and the set of
// acceptable full-command answers (Top-5 hit means ANY of them appears).
type evalc struct {
	Input    string   `json:"input"`
	Shell    string   `json:"shell"`
	Expected []string `json:"expected"`
	Note     string   `json:"note,omitempty"`
}

var evalSet = []evalc{
	// ---- CMD 内建命令 ----
	{Input: "ipconfig", Shell: "cmd", Expected: []string{"ipconfig"}},
	{Input: "dir", Shell: "cmd", Expected: []string{"dir"}},
	{Input: "chkdsk", Shell: "cmd", Expected: []string{"chkdsk"}},
	{Input: "tasklist", Shell: "cmd", Expected: []string{"tasklist"}},
	{Input: "netstat", Shell: "cmd", Expected: []string{"netstat"}},
	{Input: "ping", Shell: "cmd", Expected: []string{"ping"}},
	{Input: "cls", Shell: "cmd", Expected: []string{"cls"}},
	{Input: "tree", Shell: "cmd", Expected: []string{"tree"}},
	{Input: "attrib", Shell: "cmd", Expected: []string{"attrib"}},
	{Input: "fc", Shell: "cmd", Expected: []string{"fc"}},
	{Input: "findstr", Shell: "cmd", Expected: []string{"findstr"}},
	{Input: "sort", Shell: "cmd", Expected: []string{"sort"}},
	{Input: "more", Shell: "cmd", Expected: []string{"more"}},
	{Input: "whoami", Shell: "cmd", Expected: []string{"whoami"}},
	{Input: "systeminfo", Shell: "cmd", Expected: []string{"systeminfo"}},
	{Input: "driverquery", Shell: "cmd", Expected: []string{"driverquery"}},
	{Input: "schtasks", Shell: "cmd", Expected: []string{"schtasks"}},
	{Input: "taskkill", Shell: "cmd", Expected: []string{"taskkill"}},
	{Input: "shutdown", Shell: "cmd", Expected: []string{"shutdown"}},
	{Input: "timeout", Shell: "cmd", Expected: []string{"timeout"}},
	{Input: "vol", Shell: "cmd", Expected: []string{"vol"}},
	{Input: "ver", Shell: "cmd", Expected: []string{"ver"}},
	{Input: "where", Shell: "cmd", Expected: []string{"where"}},
	{Input: "xcopy", Shell: "cmd", Expected: []string{"xcopy"}},
	{Input: "compact", Shell: "cmd", Expected: []string{"compact"}},
	{Input: "cipher", Shell: "cmd", Expected: []string{"cipher"}},
	{Input: "certutil", Shell: "cmd", Expected: []string{"certutil"}},
	{Input: "bcdedit", Shell: "cmd", Expected: []string{"bcdedit"}},
	{Input: "diskpart", Shell: "cmd", Expected: []string{"diskpart"}},
	{Input: "reg", Shell: "cmd", Expected: []string{"reg"}},
	{Input: "sc", Shell: "cmd", Expected: []string{"sc"}},
	{Input: "copy", Shell: "cmd", Expected: []string{"copy"}},
	{Input: "move", Shell: "cmd", Expected: []string{"move"}},
	{Input: "ren", Shell: "cmd", Expected: []string{"ren"}},
	{Input: "pushd", Shell: "cmd", Expected: []string{"pushd"}},
	{Input: "popd", Shell: "cmd", Expected: []string{"popd"}},
	{Input: "start", Shell: "cmd", Expected: []string{"start"}},
	{Input: "title", Shell: "cmd", Expected: []string{"title"}},
	{Input: "chcp", Shell: "cmd", Expected: []string{"chcp"}},
	{Input: "clip", Shell: "cmd", Expected: []string{"clip"}},
	{Input: "msconfig", Shell: "cmd", Expected: []string{"msconfig"}},
	{Input: "dxdiag", Shell: "cmd", Expected: []string{"dxdiag"}},
	{Input: "compmgmt", Shell: "cmd", Expected: []string{"compmgmt.msc"}},
	{Input: "devmgmt", Shell: "cmd", Expected: []string{"devmgmt.msc"}},
	{Input: "diskmgmt", Shell: "cmd", Expected: []string{"diskmgmt.msc"}},
	{Input: "appwiz", Shell: "cmd", Expected: []string{"appwiz.cpl"}},
	{Input: "telnet", Shell: "cmd", Expected: []string{"telnet"}},
	{Input: "tracert", Shell: "cmd", Expected: []string{"tracert"}},
	{Input: "pathping", Shell: "cmd", Expected: []string{"pathping"}},
	{Input: "nslookup", Shell: "cmd", Expected: []string{"nslookup"}},
	{Input: "subst", Shell: "cmd", Expected: []string{"subst"}},
	{Input: "typeperf", Shell: "cmd", Expected: []string{"typeperf"}},
	{Input: "wmic", Shell: "cmd", Expected: []string{"wmic"}},
	{Input: "gpupdate", Shell: "cmd", Expected: []string{"gpupdate"}},
	{Input: "powercfg", Shell: "cmd", Expected: []string{"powercfg"}},
	{Input: "fsutil", Shell: "cmd", Expected: []string{"fsutil"}},
	{Input: "defrag", Shell: "cmd", Expected: []string{"defrag"}},
	{Input: "robocopy", Shell: "cmd", Expected: []string{"robocopy"}},
	{Input: "cacls", Shell: "cmd", Expected: []string{"cacls"}},
	{Input: "takeown", Shell: "cmd", Expected: []string{"takeown"}},
	{Input: "icacls", Shell: "cmd", Expected: []string{"icacls"}},
	{Input: "doskey", Shell: "cmd", Expected: []string{"doskey"}},
	{Input: "setx", Shell: "cmd", Expected: []string{"setx"}},
	{Input: "choice", Shell: "cmd", Expected: []string{"choice"}},
	{Input: "eventvwr", Shell: "cmd", Expected: []string{"eventvwr"}},
	{Input: "services", Shell: "cmd", Expected: []string{"services.msc"}},
	{Input: "taskmgr", Shell: "cmd", Expected: []string{"taskmgr"}},
	{Input: "notepad", Shell: "cmd", Expected: []string{"notepad"}},
	{Input: "mstsc", Shell: "cmd", Expected: []string{"mstsc"}},
	{Input: "explorer", Shell: "cmd", Expected: []string{"explorer"}},
	{Input: "control", Shell: "cmd", Expected: []string{"control"}},
	{Input: "certmgr", Shell: "cmd", Expected: []string{"certmgr.msc"}},
	{Input: "wf", Shell: "cmd", Expected: []string{"wf.msc"}},
	{Input: "lusrmgr", Shell: "cmd", Expected: []string{"lusrmgr.msc"}},
	{Input: "perfmon", Shell: "cmd", Expected: []string{"perfmon"}},
	{Input: "resmon", Shell: "cmd", Expected: []string{"resmon"}},
	{Input: "winver", Shell: "cmd", Expected: []string{"winver"}},
	{Input: "cscript", Shell: "cmd", Expected: []string{"cscript"}},
	{Input: "wscript", Shell: "cmd", Expected: []string{"wscript"}},
	{Input: "bitsadmin", Shell: "cmd", Expected: []string{"bitsadmin"}},
	{Input: "auditpol", Shell: "cmd", Expected: []string{"auditpol"}},
	{Input: "secpol", Shell: "cmd", Expected: []string{"secpol.msc"}},
	{Input: "gpedit", Shell: "cmd", Expected: []string{"gpedit.msc"}},
	{Input: "dism", Shell: "cmd", Expected: []string{"dism"}},
	{Input: "sfc", Shell: "cmd", Expected: []string{"sfc"}},
	{Input: "mklink", Shell: "cmd", Expected: []string{"mklink"}},
	{Input: "assoc", Shell: "cmd", Expected: []string{"assoc"}},
	{Input: "ftype", Shell: "cmd", Expected: []string{"ftype"}},
	{Input: "prompt", Shell: "cmd", Expected: []string{"prompt"}},
	{Input: "setlocal", Shell: "cmd", Expected: []string{"setlocal"}},
	{Input: "endlocal", Shell: "cmd", Expected: []string{"endlocal"}},
	{Input: "shift", Shell: "cmd", Expected: []string{"shift"}},
	{Input: "call", Shell: "cmd", Expected: []string{"call"}},
	{Input: "exit", Shell: "cmd", Expected: []string{"exit"}},
	{Input: "echo", Shell: "cmd", Expected: []string{"echo"}},
	{Input: "set", Shell: "cmd", Expected: []string{"set"}},
	{Input: "type", Shell: "cmd", Expected: []string{"type"}},
	{Input: "cd", Shell: "cmd", Expected: []string{"cd"}},
	{Input: "md", Shell: "cmd", Expected: []string{"md"}},
	{Input: "rd", Shell: "cmd", Expected: []string{"rd"}},
	{Input: "del", Shell: "cmd", Expected: []string{"del"}},
	{Input: "erase", Shell: "cmd", Expected: []string{"erase"}},
	// 模糊/编辑距离容错
	{Input: "tassklist", Shell: "cmd", Expected: []string{"tasklist"}, Note: "编辑距离 1 容错"},
	{Input: "netstat -a", Shell: "cmd", Expected: []string{"netstat -a"}, Note: "带参数"},
	// ---- PowerShell Cmdlet ----
	{Input: "Get-Process", Shell: "ps", Expected: []string{"Get-Process"}},
	{Input: "Get-Service", Shell: "ps", Expected: []string{"Get-Service"}},
	{Input: "Get-ChildItem", Shell: "ps", Expected: []string{"Get-ChildItem"}},
	{Input: "Get-Content", Shell: "ps", Expected: []string{"Get-Content"}},
	{Input: "Get-Item", Shell: "ps", Expected: []string{"Get-Item"}},
	{Input: "Remove-Item", Shell: "ps", Expected: []string{"Remove-Item"}},
	{Input: "Copy-Item", Shell: "ps", Expected: []string{"Copy-Item"}},
	{Input: "Move-Item", Shell: "ps", Expected: []string{"Move-Item"}},
	{Input: "New-Item", Shell: "ps", Expected: []string{"New-Item"}},
	{Input: "Set-Content", Shell: "ps", Expected: []string{"Set-Content"}},
	{Input: "Add-Content", Shell: "ps", Expected: []string{"Add-Content"}},
	{Input: "Clear-Host", Shell: "ps", Expected: []string{"Clear-Host"}},
	{Input: "Get-Help", Shell: "ps", Expected: []string{"Get-Help"}},
	{Input: "Get-Command", Shell: "ps", Expected: []string{"Get-Command"}},
	{Input: "Get-Alias", Shell: "ps", Expected: []string{"Get-Alias"}},
	{Input: "Get-Date", Shell: "ps", Expected: []string{"Get-Date"}},
	{Input: "Get-Location", Shell: "ps", Expected: []string{"Get-Location"}},
	{Input: "Set-Location", Shell: "ps", Expected: []string{"Set-Location"}},
	{Input: "Get-ComputerInfo", Shell: "ps", Expected: []string{"Get-ComputerInfo"}},
	{Input: "Get-NetIPAddress", Shell: "ps", Expected: []string{"Get-NetIPAddress"}},
	{Input: "Test-Connection", Shell: "ps", Expected: []string{"Test-Connection"}},
	{Input: "Invoke-WebRequest", Shell: "ps", Expected: []string{"Invoke-WebRequest"}},
	{Input: "ConvertTo-Json", Shell: "ps", Expected: []string{"ConvertTo-Json"}},
	{Input: "Select-Object", Shell: "ps", Expected: []string{"Select-Object"}},
	{Input: "Where-Object", Shell: "ps", Expected: []string{"Where-Object"}},
	{Input: "ForEach-Object", Shell: "ps", Expected: []string{"ForEach-Object"}},
	{Input: "Sort-Object", Shell: "ps", Expected: []string{"Sort-Object"}},
	{Input: "Measure-Object", Shell: "ps", Expected: []string{"Measure-Object"}},
	{Input: "Import-Csv", Shell: "ps", Expected: []string{"Import-Csv"}},
	{Input: "Export-Csv", Shell: "ps", Expected: []string{"Export-Csv"}},
	{Input: "Compress-Archive", Shell: "ps", Expected: []string{"Compress-Archive"}},
	{Input: "Expand-Archive", Shell: "ps", Expected: []string{"Expand-Archive"}},
	{Input: "Set-ExecutionPolicy", Shell: "ps", Expected: []string{"Set-ExecutionPolicy"}},
	{Input: "Get-ExecutionPolicy", Shell: "ps", Expected: []string{"Get-ExecutionPolicy"}},
	{Input: "Start-Service", Shell: "ps", Expected: []string{"Start-Service"}},
	{Input: "Stop-Service", Shell: "ps", Expected: []string{"Stop-Service"}},
	{Input: "Restart-Service", Shell: "ps", Expected: []string{"Restart-Service"}},
	{Input: "Stop-Process", Shell: "ps", Expected: []string{"Stop-Process"}},
	{Input: "Start-Process", Shell: "ps", Expected: []string{"Start-Process"}},
	{Input: "Get-FileHash", Shell: "ps", Expected: []string{"Get-FileHash"}},
	{Input: "Get-EventLog", Shell: "ps", Expected: []string{"Get-EventLog"}},
	{Input: "Clear-History", Shell: "ps", Expected: []string{"Clear-History"}},
	{Input: "Get-Process -Name", Shell: "ps", Expected: []string{"Get-Process -Name"}},
	// 小写/大小写不敏感
	{Input: "get-process", Shell: "ps", Expected: []string{"Get-Process"}, Note: "大小写不敏感"},
	{Input: "get-servi", Shell: "ps", Expected: []string{"Get-Service"}, Note: "前缀"},
	{Input: "restart-servic", Shell: "ps", Expected: []string{"Restart-Service"}},
	// ---- 外部工具（双终端通用） ----
	{Input: "git st", Shell: "cmd", Expected: []string{"git stash", "git status"}},
	{Input: "git st", Shell: "ps", Expected: []string{"git stash", "git status"}},
	{Input: "git co", Shell: "cmd", Expected: []string{"git checkout"}},
	{Input: "git com", Shell: "cmd", Expected: []string{"git commit"}},
	{Input: "git br", Shell: "ps", Expected: []string{"git branch"}},
	{Input: "git lo", Shell: "cmd", Expected: []string{"git log"}},
	{Input: "git di", Shell: "ps", Expected: []string{"git diff"}},
	{Input: "git pu", Shell: "cmd", Expected: []string{"git push"}},
	{Input: "git pl", Shell: "ps", Expected: []string{"git pull"}},
	{Input: "git fe", Shell: "cmd", Expected: []string{"git fetch"}},
	{Input: "git me", Shell: "ps", Expected: []string{"git merge"}},
	{Input: "git cl", Shell: "cmd", Expected: []string{"git clone"}},
	{Input: "git re", Shell: "ps", Expected: []string{"git rebase", "git reset", "git reflog", "git remote"}},
	{Input: "git ta", Shell: "cmd", Expected: []string{"git tag"}},
	{Input: "git sh", Shell: "ps", Expected: []string{"git show", "git stash"}},
	{Input: "git sw", Shell: "cmd", Expected: []string{"git switch"}},
	{Input: "git in", Shell: "ps", Expected: []string{"git init"}},
	{Input: "git ad", Shell: "cmd", Expected: []string{"git add"}},
	{Input: "git rm", Shell: "ps", Expected: []string{"git rm"}},
	{Input: "git mv", Shell: "cmd", Expected: []string{"git mv"}},
	{Input: "git bl", Shell: "ps", Expected: []string{"git blame"}},
	{Input: "git ci", Shell: "cmd", Expected: []string{"git cherry-pick"}},
	{Input: "git gr", Shell: "ps", Expected: []string{"git grep"}},
	{Input: "git ar", Shell: "cmd", Expected: []string{"git archive"}},
	{Input: "git work", Shell: "ps", Expected: []string{"git worktree"}},
	{Input: "git sub", Shell: "cmd", Expected: []string{"git submodule"}},
	{Input: "git status", Shell: "cmd", Expected: []string{"git status"}, Note: "精确子命令"},
	{Input: "npm i", Shell: "ps", Expected: []string{"npm install"}},
	{Input: "npm in", Shell: "cmd", Expected: []string{"npm init", "npm install"}},
	{Input: "npm ru", Shell: "ps", Expected: []string{"npm run"}},
	{Input: "npm ls", Shell: "cmd", Expected: []string{"npm ls"}},
	{Input: "npm un", Shell: "ps", Expected: []string{"npm uninstall"}},
	{Input: "npm au", Shell: "cmd", Expected: []string{"npm audit"}},
	{Input: "npm pu", Shell: "ps", Expected: []string{"npm publish"}},
	{Input: "npm up", Shell: "cmd", Expected: []string{"npm update"}},
	{Input: "pip in", Shell: "ps", Expected: []string{"pip install"}},
	{Input: "pip li", Shell: "cmd", Expected: []string{"pip list"}},
	{Input: "pip fr", Shell: "ps", Expected: []string{"pip freeze"}},
	{Input: "pip sh", Shell: "cmd", Expected: []string{"pip show"}},
	{Input: "pip un", Shell: "ps", Expected: []string{"pip uninstall"}},
	{Input: "pip ch", Shell: "cmd", Expected: []string{"pip check"}},
	{Input: "pip do", Shell: "ps", Expected: []string{"pip download"}},
	{Input: "docker ps", Shell: "cmd", Expected: []string{"docker ps"}},
	{Input: "docker ru", Shell: "ps", Expected: []string{"docker run"}},
	{Input: "docker bu", Shell: "cmd", Expected: []string{"docker build"}},
	{Input: "docker im", Shell: "ps", Expected: []string{"docker images"}},
	{Input: "docker pu", Shell: "cmd", Expected: []string{"docker pull", "docker push"}},
	{Input: "docker lo", Shell: "ps", Expected: []string{"docker logs"}},
	{Input: "docker st", Shell: "cmd", Expected: []string{"docker stop", "docker start"}},
	{Input: "docker rm", Shell: "ps", Expected: []string{"docker rm"}},
	{Input: "docker ex", Shell: "cmd", Expected: []string{"docker exec"}},
	{Input: "docker sy", Shell: "ps", Expected: []string{"docker system"}},
	{Input: "docker co", Shell: "cmd", Expected: []string{"docker compose"}},
	{Input: "go run", Shell: "cmd", Expected: []string{"go run"}},
	{Input: "go tes", Shell: "ps", Expected: []string{"go test"}},
	{Input: "go bui", Shell: "cmd", Expected: []string{"go build"}},
	{Input: "go mod", Shell: "ps", Expected: []string{"go mod"}},
	{Input: "go get", Shell: "cmd", Expected: []string{"go get"}},
	{Input: "go vet", Shell: "ps", Expected: []string{"go vet"}},
	{Input: "go env", Shell: "cmd", Expected: []string{"go env"}},
	{Input: "go ins", Shell: "ps", Expected: []string{"go install"}},
	{Input: "kubectl ge", Shell: "cmd", Expected: []string{"kubectl get"}},
	{Input: "kubectl ap", Shell: "ps", Expected: []string{"kubectl apply"}},
	{Input: "kubectl de", Shell: "cmd", Expected: []string{"kubectl delete", "kubectl describe"}},
	{Input: "kubectl lo", Shell: "ps", Expected: []string{"kubectl logs"}},
	{Input: "kubectl ex", Shell: "cmd", Expected: []string{"kubectl exec"}},
	{Input: "ssh-keygen", Shell: "cmd", Expected: []string{"ssh-keygen"}},
	{Input: "ssh-copy-id", Shell: "ps", Expected: []string{"ssh-copy-id"}},
	{Input: "terraform", Shell: "cmd", Expected: []string{"terraform"}},
	{Input: "mvn", Shell: "ps", Expected: []string{"mvn"}},
	{Input: "gradle", Shell: "cmd", Expected: []string{"gradle"}},
	{Input: "make", Shell: "ps", Expected: []string{"make"}},
	{Input: "jq", Shell: "cmd", Expected: []string{"jq"}},
	{Input: "gh", Shell: "ps", Expected: []string{"gh"}},
	{Input: "uv", Shell: "cmd", Expected: []string{"uv"}},
	{Input: "conda", Shell: "ps", Expected: []string{"conda"}},
	{Input: "aws", Shell: "cmd", Expected: []string{"aws"}},
	{Input: "az", Shell: "ps", Expected: []string{"az"}},
	{Input: "helm", Shell: "cmd", Expected: []string{"helm"}},
	{Input: "rg", Shell: "ps", Expected: []string{"rg"}},
	{Input: "fd", Shell: "cmd", Expected: []string{"fd"}},
}

func runEval() error {
	kb, err := knowledge.Load()
	if err != nil {
		return err
	}
	dir, err := tempDir("evaldb")
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
	cfg.Engine = config.EngineLocal
	eng := completion.New(kb, store, cfg, nil, nil)

	type result struct {
		Hit     bool
		Got     []string
		Top1Hit bool
	}
	results := make([]result, len(evalSet))
	pass := 0
	top1 := 0
	detail := make([]string, 0, len(evalSet))
	for i, c := range evalSet {
		resp, err := eng.LocalComplete(completion.Request{Input: c.Input, Shell: c.Shell, CWD: dir})
		if err != nil {
			return err
		}
		got := make([]string, 0, len(resp.List))
		for _, s := range resp.List {
			got = append(got, s.Full)
		}
		hit := false
		for _, want := range c.Expected {
			for _, g := range got {
				// 精确命中时引擎会给命令加尾随空格（便于继续敲参数），比较时归一化。
				if strings.EqualFold(strings.TrimRight(g, " "), want) {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		top1Hit := false
		if len(got) > 0 {
			for _, want := range c.Expected {
				if strings.EqualFold(strings.TrimRight(got[0], " "), want) {
					top1Hit = true
					break
				}
			}
		}
		results[i] = result{Hit: hit, Got: got, Top1Hit: top1Hit}
		if hit {
			pass++
		}
		if top1Hit {
			top1++
		}
		if !hit {
			detail = append(detail, fmt.Sprintf("- `%s` [%s] 期望 %s 实际 Top5=%s",
				c.Input, c.Shell, strings.Join(c.Expected, "/"), strings.Join(got, " | ")))
		}
	}
	total := len(evalSet)
	rate := float64(pass) / float64(total) * 100
	top1Rate := float64(top1) / float64(total) * 100

	md := fmt.Sprintf(`# 引擎质量评测报告（本地引擎 Top-5 命中率）

- 评测集规模：**%d 条**真实使用场景（CMD 内建 / PowerShell Cmdlet / 外部工具 git·npm·pip·docker·go·kubectl 等，含前缀、子串、编辑距离容错）
- 判定标准：期望命令（1~3 个均可）出现在本地引擎 Top-5 即命中
- 执行时间：%s

## 结果

| 指标 | 数值 | 达标线 |
|---|---|---|
| Top-5 命中 | **%d / %d (%.1f%%)** | ≥ 90%% |
| Top-1 命中 | %d / %d (%.1f%%) | — |

## 未命中明细
%s

## 结论
本地引擎 Top-5 命中率 **%.1f%%**，%s 目标线（≥90%%）。
`, total, time.Now().Format("2006-01-02 15:04:05"),
		pass, total, rate, top1, total, top1Rate,
		func() string {
			if len(detail) == 0 {
				return "（无）"
			}
			return strings.Join(detail, "\n")
		}(),
		rate, map[bool]string{true: "达到", false: "未达到"}[rate >= 90])

	jsonData, _ := json.MarshalIndent(map[string]any{
		"total": total, "pass": pass, "top1": top1, "top5_rate": rate,
		"top1_rate": top1Rate, "cases": evalSet, "results": results, "date": time.Now().Format(time.RFC3339),
	}, "", "  ")
	if rate < 90 {
		return fmt.Errorf("Top-5 hit rate %.1f%% < 90%% (%d/%d)", rate, pass, total)
	}
	return writeReport("engine-eval", md, jsonData)
}
