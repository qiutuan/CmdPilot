-- CmdPilot - Windows 命令行智能补全助手 (Clink / CMD adapter)
--
-- 能力边界（与 PowerShell PSReadLine 适配层对比，见 ADR-002）：
--   * Clink 的 Lua 没有 HTTP 客户端 —— 本插件通过 cmdpilot-clink.exe 伴侣二进制
--     与本地守护进程通信（cmdpilot-req-v1 / cmdpilot-resp-v1 行格式文件）。
--   * Clink 不支持第三方注册"鱼式幽灵文本"（autosuggest）；最佳替代是 Tab 触发
--     补全列表（Clink 原生菜单）：Enter/Tab 接受唯一候选、Esc 取消。本插件注册一个
--     高优先级补全生成器，用 CmdPilot 引擎（本地库 + AI + 收藏 + 频率推荐）替换
--     Clink 默认补全；引擎无候选时返回 false，交回 Clink 原生补全（不丢能力）。
--   * 每次 Tab 调用一次伴侣二进制（实测 30~90ms，主要开销是 cmd.exe + Clink 注入）。
--
-- 本文件按实测的 Clink 1.9.33.a4bf0e 重写。旧版调用的 clink.script_dir /
-- clink.register_generator / clink.gethistory / clink.getline / clink.getpoint /
-- clink.getcwd 在真实版本上全部不存在（脚本加载阶段即报错，插件从未生效）。
--
-- 实测确认的 API 与坑（勿凭记忆改动）：
--   * 生成器：clink.generator(priority) -> obj，定义 obj:generate(line_state,
--     match_builder)；数字越小越先调用；返回 true 表示不再调用后续生成器。
--   * line_state:getline() / :getcursor()（1 基；光标在 1 时表示行首之前，
--     故"光标前文本" = line:sub(1, cursor-1)）。注意 Clink 默认把"末尾词"截断为
--     0 长度（缓存后按输入过滤），所以补全词必须由整行自行推算。
--   * match_builder:addmatch(str | {match=,type=,display=,description=}) -> boolean；
--     匹配串是"末尾词的替换文本"，不是整行。
--   * 每次 Tab 会按"词"分两趟调用生成器，且交给生成器的 line_state 是裁剪过的：
--     第一趟是命令首词（输入 "git commi" 时实测拿到 "g"，光标在裁剪文本末尾），
--     第二趟是末尾词（实测拿到 "git c"，即"末尾词首字符"之前 + 该字符）。
--     真正生效的是末尾词那一趟；补全词必须由裁剪文本自行推算（见 gen:generate）。
--   * Clink 默认把匹配串按字母表重排序（会覆盖引擎排名），需 setnosort(true) 保留加入顺序。
--   * 历史：rl.gethistorycount() / rl.gethistoryitems(start, end) -> {{line=, time=}}。
--   * 进程：os.execute / io.popen 都经 cmd.exe 启动子进程，而 cmd /c 的引号规则会
--     吃掉"首 token 带引号"的命令行的第一个引号（实测报"文件名、目录名或卷标语法不正确"），
--     因此整个命令行必须再包一层引号：cmd /c ""<exe>" <args>"。
--   * 文件：io.open 文本模式会把 \n 改写成 \r\n，请求文件必须用 "wb" 写，
--     否则伴侣二进制的 cmdpilot-req-v1 魔数校验失败（实测报 bad request）。
--
-- 最低版本：Clink 1.3.18（rl.gethistoryitems；clink.generator/io.popen 更早已有）。

-- --- 伴侣二进制定位 -------------------------------------------------------
-- 旧版用 clink.script_dir（不存在）。Lua 可用 debug.getinfo 在 chunk 层拿到自身路径。
local function script_dir()
    local info = debug.getinfo(1, "S")
    local src = (info and info.source) or ""
    local path = string.gsub(src, "^@", "")
    local dir = string.match(path, "^(.*)[\\/][^\\/]*$")
    return dir or ""
end

local function file_exists(path)
    local f = io.open(path, "rb")
    if f then
        f:close()
        return true
    end
    return false
end

local function resolve_companion()
    local env = os.getenv("CMDPILOT_BIN")
    if env and env ~= "" then
        return env
    end
    local localappdata = os.getenv("LOCALAPPDATA") or ""
    local dir = script_dir()
    local candidates = {}
    if dir ~= "" then
        candidates[#candidates + 1] = dir .. "\\cmdpilot-clink.exe"
        candidates[#candidates + 1] = dir .. "\\bin\\cmdpilot-clink.exe"
    end
    if localappdata ~= "" then
        candidates[#candidates + 1] = localappdata .. "\\clink\\cmdpilot-clink.exe"
        candidates[#candidates + 1] = localappdata .. "\\CmdPilot\\cmdpilot-clink.exe"
    end
    for _, c in ipairs(candidates) do
        if file_exists(c) then
            return c
        end
    end
    return "cmdpilot-clink.exe" -- 交给 PATH
end

local companion = resolve_companion()

-- os.gettemppath() 带尾部反斜杠，直接拼 "\\name" 会出现双反斜杠
local function temp_dir()
    local t = (type(os.gettemppath) == "function" and os.gettemppath()) or os.getenv("TEMP")
        or os.getenv("TMP") or "."
    t = string.gsub(t, "[\\/]+$", "")
    if t == "" then
        t = "."
    end
    return t
end

local tempdir = temp_dir()
local seq = 0

local function temp_name(kind)
    seq = seq + 1
    return string.format("%s\\cmdpilot-%d-%d-%d.%s", tempdir, os.time(),
        math.random(100000, 999999), seq, kind)
end

-- --- 行协议编解码（与 cmd/cmdpilot-clink/main.go 保持一致）----------------
-- \x1e 为转义前导（\x1e\x1e -> 字面 \x1e，\x1e\x1f -> 字面 \x1f），裸 \x1f 为字段分隔符。
local function stuff(s)
    s = string.gsub(s, "\30", "\30\30")
    s = string.gsub(s, "\31", "\30\31")
    return s
end

-- 按裸 \x1f 切分并解转义，返回已解码的字段表（与 Go 侧 unstuffSplit 等价）
local function split_fields(s)
    local fields, cur, i, n = {}, {}, 1, #s
    while i <= n do
        local c = s:sub(i, i)
        if c == "\30" and i < n then
            cur[#cur + 1] = s:sub(i + 1, i + 1)
            i = i + 2
        elseif c == "\31" then
            fields[#fields + 1] = table.concat(cur)
            cur = {}
            i = i + 1
        else
            cur[#cur + 1] = c
            i = i + 1
        end
    end
    fields[#fields + 1] = table.concat(cur)
    return fields
end

local function write_file(path, content)
    -- "wb"：避免 \n 被改写成 \r\n（会让 req-v1 魔数校验失败）
    local f = io.open(path, "wb")
    if not f then
        return false
    end
    f:write(content)
    f:close()
    return true
end

local function read_file(path)
    local f = io.open(path, "rb")
    if not f then
        return nil
    end
    local content = f:read("*a")
    f:close()
    return content
end

-- --- 请求构造 -------------------------------------------------------------
local function clean_one_line(s)
    return (string.gsub(s, "[\r\n]+", " "))
end

local function recent_history(limit)
    local okc, count = pcall(rl.gethistorycount)
    if not okc or type(count) ~= "number" or count < 1 then
        return {}
    end
    local start = math.max(1, count - (limit - 1))
    local oki, items = pcall(rl.gethistoryitems, start, count)
    if not oki or type(items) ~= "table" then
        return {}
    end
    local out = {}
    for _, item in ipairs(items) do
        local line = type(item) == "table" and item.line or nil
        if type(line) == "string" and line ~= "" then
            out[#out + 1] = clean_one_line(line)
        end
    end
    return out
end

local function build_request(input, cwd)
    -- history 是唯一的多值字段：条目之间用裸 \x1f 分隔，条目内容单独 stuff，
    -- 整体不再二次 stuff（否则分隔符会被当成内容转义，多条历史并成一条）。
    local entries = {}
    for _, h in ipairs(recent_history(10)) do
        entries[#entries + 1] = stuff(h)
    end
    local req = "cmdpilot-req-v1\n"
    req = req .. "input=" .. stuff(clean_one_line(input)) .. "\n"
    req = req .. "shell=cmd\n"
    req = req .. "cwd=" .. stuff(cwd) .. "\n"
    req = req .. "trigger=tab\n"
    req = req .. "history=" .. table.concat(entries, "\31") .. "\n"
    return req
end

local function run_companion(reqfile, outfile)
    -- 必须再包一层引号：cmd /c 会吃掉首 token 的引号（见文件头"实测确认的 API 与坑"）
    local cmdline = string.format('cmd /c ""%s" --request "%s" --output "%s" --plain" 2>nul',
        companion, reqfile, outfile)
    local ran = false
    pcall(function()
        local pipe = io.popen(cmdline, "r")
        if not pipe then
            return
        end
        pipe:read("*a") -- 排空管道并等待子进程结束
        pipe:close()
        ran = true
    end)
    return ran
end

-- 调用引擎取候选：返回 { {full=, source=, kind=}, ... }
local function complete(input)
    local reqfile = temp_name("req")
    local outfile = temp_name("out")
    local cands = {}
    if not write_file(reqfile, build_request(input, os.getcwd() or "")) then
        return cands
    end
    if run_companion(reqfile, outfile) then
        local content = read_file(outfile)
        if content and string.sub(content, 1, 16) == "cmdpilot-resp-v1" then
            local ln = 0
            for line in string.gmatch(content, "[^\n]+") do
                ln = ln + 1
                -- 第 1 行魔数，第 2 行 top（top 同时出现在列表里），第 3 行起为候选列表：
                -- full \x1f kind \x1f source
                if ln > 2 then
                    local fields = split_fields(line)
                    if fields[1] and fields[1] ~= "" then
                        cands[#cands + 1] = { full = fields[1], kind = fields[2], source = fields[3] }
                    end
                end
            end
        end
    end
    os.remove(reqfile)
    os.remove(outfile)
    return cands
end

-- --- Tab 补全生成器 -------------------------------------------------------
-- 候选来源 -> 菜单里的说明文字（source 取值见 internal/completion/candidates.go）
local SOURCE_LABEL = {
    ["local"] = "本地库",
    history = "历史",
    favorite = "收藏",
    user = "自定义",
    ai = "AI",
}

local gen = clink.generator(1)

function gen:generate(line_state, match_builder)
    local line = line_state:getline()
    local cursor = line_state:getcursor()
    if type(line) ~= "string" or type(cursor) ~= "number" then
        return false
    end
    if cursor < 1 then
        cursor = 1
    end
    local input = string.sub(line, 1, cursor - 1)
    if string.match(input, "^%s*$") then
        return false
    end

    local cands = complete(input)
    if #cands == 0 then
        return false -- 引擎无候选：交回 Clink 原生补全
    end

    -- Clink 的匹配是"替换末尾词"。候选是整行，需要换算成末尾词的替换文本：
    -- head = 光标前文本里最后一个空白及其之前的部分。
    local head = string.match(input, "^(.*%s)") or ""
    local typed_word = string.sub(input, #head + 1)
    -- 实测：Clink 默认对匹配串按字母表重排序，会把引擎的排名（本地库/频率/上下文）
    -- 彻底冲掉（Tab 先插入的是字母序第一项而不是引擎第一名）。setnosort 保留加入顺序。
    match_builder:setnosort(true)
    local added = 0
    local seen = {}
    for _, cand in ipairs(cands) do
        local word
        if head == "" then
            word = cand.full
        elseif string.sub(cand.full, 1, #head) == head then
            word = string.sub(cand.full, #head + 1)
        end
        if word and word ~= "" and word ~= typed_word and not seen[word] then
            seen[word] = true
            local mtype = "word"
            local tail = string.sub(word, -1)
            if tail == "\\" or tail == "/" then
                mtype = "dir" -- 目录：不追加空格，由 Clink 补路径分隔符
            end
            local match = { match = word, type = mtype }
            local label = SOURCE_LABEL[cand.source]
            if label then
                match.description = label
            end
            if match_builder:addmatch(match) then
                added = added + 1
            end
        end
    end
    -- 有候选就让 CmdPilot 引擎说了算（不再叠加 Clink 默认补全，避免重复项，
    -- 也保证唯一候选能被 Tab 直接插入）；无候选时上面的 return false 已经兜底。
    return added > 0
end

-- --- 使用记录同步（M5）：每次新提示符同步新增的历史命令 -------------------
local last_history_count = nil

local function sync_usage()
    local okc, count = pcall(rl.gethistorycount)
    if not okc or type(count) ~= "number" then
        return
    end
    if last_history_count == nil then
        last_history_count = count -- 首次只记基线，不回灌历史库
        return
    end
    if count <= last_history_count then
        if count < last_history_count then
            last_history_count = count -- 历史被清空/重载
        end
        return
    end
    local start = last_history_count + 1
    if count - last_history_count > 50 then
        start = count - 49 -- 单批上限，避免首次全量灌库
    end
    last_history_count = count

    local oki, items = pcall(rl.gethistoryitems, start, count)
    if not oki or type(items) ~= "table" then
        return
    end
    local cwd = os.getcwd() or ""
    local lines = {}
    for _, item in ipairs(items) do
        local line = type(item) == "table" and item.line or nil
        if type(line) == "string" and line ~= "" then
            -- 批量格式（见 main.go reportBatchUsage）：命令裸写，目录/shell 需 stuff
            lines[#lines + 1] = clean_one_line(line) .. "\31" .. stuff(cwd) .. "\31cmd"
        end
    end
    if #lines == 0 then
        return
    end
    local batchfile = temp_name("batch")
    if not write_file(batchfile, table.concat(lines, "\n") .. "\n") then
        return
    end
    pcall(function()
        local cmdline = string.format('cmd /c ""%s" --report-batch "%s"" 2>nul', companion, batchfile)
        local pipe = io.popen(cmdline, "r")
        if pipe then
            pipe:read("*a")
            pipe:close()
        end
    end)
    os.remove(batchfile)
end

-- --- 启动提示行（非侵入，config.enable_prompt_line）-----------------------
local function read_config()
    local la = os.getenv("LOCALAPPDATA")
    if la == nil or la == "" then
        return nil
    end
    local raw = read_file(la .. "\\CmdPilot\\config.json")
    if not raw then
        return nil
    end
    return {
        engine = string.match(raw, '"engine"%s*:%s*"([^"]+)"'),
        trigger = string.match(raw, '"trigger"%s*:%s*"([^"]+)"'),
        prompt = string.match(raw, '"enable_prompt_line"%s*:%s*([a-z]+)'),
    }
end

local started = false

clink.onbeginedit(function()
    if not started then
        started = true
        pcall(function()
            local cfg = read_config()
            if cfg and cfg.prompt == "false" then
                return
            end
            local mode = (cfg and cfg.engine) or "hybrid"
            local trig = (cfg and cfg.trigger) or "auto"
            clink.print(string.format(
                "CmdPilot 已启用 [%s/%s] — Tab 补全（本地库+AI+收藏+频率推荐），输入 cmdpilot help 查看命令",
                mode, trig))
        end)
    end
    pcall(sync_usage)
end)

return nil
