-- CmdPilot - Windows 命令行智能补全助手 (Clink / CMD adapter)
--
-- Clink 的能力边界（与 PowerShell PSReadLine 适配层对比，见 ADR-002）：
--   * Clink 的 Lua 没有 HTTP 客户端，也没有 os.popen/io.popen —— 因此本插件
--     通过 cmdpilot-clink.exe 伴侣二进制与本地守护进程通信（行格式文件模式）。
--   * Clink 不支持第三方插件注册"鱼式幽灵文本"（autosuggest）；最佳替代是
--     Tab 触发补全列表（Clink 原生菜单），Enter/Tab 接受、Esc 取消 —— 本插件
--     即注册一个自定义补全生成器，用 CmdPilot 引擎（本地库 + AI + 收藏 +
--     频率推荐）替换 Clink 默认的简单补全。
--   * 本插件每次 Tab 调用一次伴侣二进制（<20ms），完全同步且不阻塞输入。
--
-- 最低版本：Clink 1.4.0（使用 clink.register_generator / clink.gethistory）。

local companion = os.getenv("CMDPILOT_BIN")
if companion == nil or companion == "" then
    local candidates = {
        clink.script_dir .. "\\bin\\cmdpilot-clink.exe",
        clink.script_dir .. "\\cmdpilot-clink.exe",
    }
    for _, c in ipairs(candidates) do
        local f = io.open(c, "r")
        if f then
            f:close()
            companion = c
            break
        end
    end
end
if companion == nil or companion == "" then
    companion = "cmdpilot-clink" -- rely on PATH
end

local tempdir = os.getenv("TEMP") or os.getenv("TMP") or "."

-- --- byte-stuffing for the line protocol (see cmdpilot-clink) ------------
local function stuff(s)
    s = string.gsub(s, "\29", "\29\29") -- \x1e -> \x1e\x1e
    s = string.gsub(s, "\31", "\29\31") -- \x1f -> \x1e\x1f
    return s
end

local function writefile(path, content)
    local f = io.open(path, "w")
    if not f then
        return false
    end
    f:write(content)
    f:close()
    return true
end

local function readfile(path)
    local f = io.open(path, "r")
    if not f then
        return nil
    end
    local content = f:read("*a")
    f:close()
    return content
end

-- --- request building -----------------------------------------------------
local function recent_history()
    local ok, hist = pcall(function() return clink.gethistory() end)
    if not ok or type(hist) ~= "table" then
        return {}
    end
    local out = {}
    for i = math.max(1, #hist - 9), #hist do
        out[#out + 1] = hist[i]
    end
    return out
end

local function build_request(input, cwd)
    local histval = ""
    local hist = recent_history()
    for i, h in ipairs(hist) do
        local clean = string.gsub(h, "[\r\n]+", " ")
        if i > 1 then
            histval = histval .. "\31"
        end
        histval = histval .. clean
    end
    local req = "cmdpilot-req-v1\n"
    req = req .. "input=" .. stuff(string.gsub(input, "[\r\n]+", " ")) .. "\n"
    req = req .. "shell=cmd\n"
    req = req .. "cwd=" .. stuff(cwd) .. "\n"
    req = req .. "trigger=auto\n"
    req = req .. "history=" .. stuff(histval) .. "\n"
    return req
end

local function run_companion(reqfile, outfile)
    local cmdline = string.format('"%s" --request "%s" --output "%s" --plain',
        companion, reqfile, outfile)
    local ok = os.execute(cmdline)
    return ok == true
end

-- --- completion generator (Tab) ------------------------------------------
local function addmatch(matches, word)
    if type(matches.add) == "function" then
        matches:add(word)
    else
        matches[#matches + 1] = word
    end
end

clink.register_generator(function()
    return function(matches)
        local line = clink.getline()
        local point = clink.getpoint()
        local cwd = clink.getcwd()
        if line == nil then
            return
        end
        local input = string.sub(line, 1, point)
        if string.match(input, "^%s*$") then
            return
        end

        local id = os.time() .. math.random(1000, 9999)
        local reqfile = tempdir .. "\\cmdpilot-" .. id .. ".req"
        local outfile = tempdir .. "\\cmdpilot-" .. id .. ".out"
        if not writefile(reqfile, build_request(input, cwd)) then
            return
        end
        if run_companion(reqfile, outfile) then
            local content = readfile(outfile)
            if content and string.sub(content, 1, 16) == "cmdpilot-resp-v1" then
                local ln = 0
                for matchline in string.gmatch(content, "[^\n]+") do
                    ln = ln + 1
                    if ln <= 2 then
                        goto continue -- magic + top line (top is in the list too)
                    end
                    local full = string.match(matchline, "^([^\31]*)\31")
                    if full ~= nil and full ~= "" then
                        addmatch(matches, full)
                    end
                    ::continue::
                end
            end
        end
        os.remove(reqfile)
        os.remove(outfile)
    end
end, "cmdpilot")

-- --- usage recording (M5): sync new history entries on each new edit ------
local last_history_count = 0
local syncing = false

clink.onbeginedit(function()
    if syncing then
        return
    end
    syncing = true
    local ok, hist = pcall(function() return clink.gethistory() end)
    if ok and type(hist) == "table" and #hist > last_history_count then
        local new_lines = {}
        for i = last_history_count + 1, #hist do
            new_lines[#new_lines + 1] = hist[i]
        end
        last_history_count = #hist
        if #new_lines > 0 then
            local id = os.time() .. math.random(1000, 9999)
            local batchfile = tempdir .. "\\cmdpilot-batch-" .. id .. ".txt"
            local f = io.open(batchfile, "w")
            if f then
                for _, l in ipairs(new_lines) do
                    local clean = string.gsub(l, "[\r\n]+", " ")
                    f:write(clean, "\n")
                end
                f:close()
                os.execute(string.format('"%s" --report-batch "%s"', companion, batchfile))
                os.remove(batchfile)
            end
        end
    end
    syncing = false
end)

-- --- startup line (non-intrusive, config.enable_prompt_line) --------------
local function read_config()
    local la = os.getenv("LOCALAPPDATA")
    if la == nil or la == "" then
        return nil
    end
    local path = la .. "\\CmdPilot\\config.json"
    local f = io.open(path, "r")
    if not f then
        return nil
    end
    local raw = f:read("*a")
    f:close()
    return {
        engine = string.match(raw, '"engine"%s*:%s*"([^"]+)"'),
        trigger = string.match(raw, '"trigger"%s*:%s*"([^"]+)"'),
        prompt = string.match(raw, '"enable_prompt_line"%s*:%s*([a-z]+)'),
    }
end

local cfg = read_config()
if cfg and cfg.prompt ~= "false" then
    local mode = cfg.engine or "hybrid"
    local trig = cfg.trigger or "auto"
    return string.format("CmdPilot 已启用 [%s/%s] — Tab 补全 (本地库+AI+收藏+频率推荐), 输入 cmdpilot help 查看命令",
        mode, trig)
end
return nil
