// CmdPilot 预测器核心（编译实现）。
//
// 为什么预测器本体必须是 C#，而不是 PowerShell 类（2026-09-16 实测定论）：
//   PSReadLine 在内联视图渲染时同步阻塞取建议
//   （Render.cs GenerateRender -> QueryForSuggestion -> GetPredictionResults ->
//    _predictionTask.Result），引擎则把预测器回调丢到线程池上跑，并只等 20ms
//   （CommandPrediction.PredictInputAsync 默认 millisecondsTimeout: 20），超时即
//   取消、丢弃该预测器的结果（只收 IsCompletedSuccessfully 的任务）。
//   于是"回调能不能在 20ms 内跑完"就是关键，而 PowerShell 类方法做不到：
//     * 同一段 40 次引擎调用（同一进程、同一输入序列、同样阻塞在 .Result 上）：
//         PowerShell 类 GetSuggestion   → 每次 31ms，predictors=0（全部超时丢弃）
//         编译实现 GetSuggestion        → 平均 0.42ms，predictors=1
//       31ms = 20ms 超时 + .NET 定时器 15.6ms 粒度，即"每次都踩满超时"。
//     * 真机控制台按键回声实测（97 字符长命令，每键 90ms 节奏）：
//         模块 + 预测开启 44.5-48.4ms/键（全程平坦，97 键无一例外）
//         无模块 / 模块但 PredictionSource None / 仅历史建议 均为 15.5ms/键
//       差值 ~30ms 与上面的超时路径完全吻合——这就是"长命令后输入/删除延迟极高"
//       的根因：每敲一个键，渲染线程都要白等一次 20ms 超时。
//
//   因此：GetSuggestion 以内只允许编译代码（锁 + 读快照 + 组装 SuggestionPackage），
//   任何 PowerShell 执行都不得出现在引擎回调线程上。后台 worker（debounce、
//   伴侣进程调用）仍由 PowerShell 脚本在自己的专用 runspace 里跑——它不在引擎
//   回调路径上，慢一点无所谓；两侧通过 CmdPilotPredictionState 交换数据。
//
// 另注：PowerShell 只留在 worker 侧后，"预测器建议从未真正显示"这一副作用也一并
// 修掉——旧实现 predictors=0，建议被引擎丢弃，用户看到的幽灵文本其实来自历史
// 建议；换成编译实现后 predictors=1，CmdPilot 自己的建议才真正进入内联视图。
//
// 与模块/worker 的接口约定：后台 worker 跑在自己的 runspace 里，Add-Type 出来的
// 类型名在那里不保证可解析（类型字面量会抛"无法找到类型"），因此 worker 只持有
// 预测器对象引用、只调它的**实例方法**（WorkerAlive / WorkerGen / WorkerPending /
// WorkerIsCurrent / WorkerPublish）——方法绑定按对象类型走，不做类型名解析。
// 两侧的数据交换全部经 CmdPilotPredictionState（static，只碰 .NET 对象）。

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Threading;
using System.Management.Automation.Subsystem;
using System.Management.Automation.Subsystem.Prediction;

namespace CmdPilot
{
    /// <summary>一条建议（full 为插入文本，source 为来源标签）。</summary>
    public sealed class PredictionEntry
    {
        public string Full { get; private set; }
        public string Source { get; private set; }

        public PredictionEntry(string full, string source)
        {
            Full = full ?? string.Empty;
            Source = source ?? string.Empty;
        }
    }

    /// <summary>Register 返回给 GetSuggestion 的快照副本（锁内拷出，锁外只读）。</summary>
    public sealed class PredictionSnapshot
    {
        public bool Current { get; set; }
        public string TopText { get; set; }
        public string TopFull { get; set; }
        public PredictionEntry[] Entries { get; set; }

        public PredictionSnapshot()
        {
            Current = false;
            TopText = string.Empty;
            TopFull = string.Empty;
            Entries = EmptyEntries;
        }

        internal static readonly PredictionEntry[] EmptyEntries = new PredictionEntry[0];
    }

    /// <summary>
    /// 引擎回调线程（GetSuggestion）与后台 worker（PowerShell 脚本）之间的共享状态。
    /// 全部成员只碰 .NET 对象：从 PowerShell 侧调用也是普通静态方法调用，不进解释器。
    /// </summary>
    public static class CmdPilotPredictionState
    {
        private static readonly object Gate = new object();
        private static string _pending = string.Empty;
        private static string _pendingCwd = string.Empty;
        private static long _gen;
        private static bool _tabOnly;
        private static bool _running;
        private static string _snapInput = string.Empty;
        private static string _topText = string.Empty;
        private static string _topFull = string.Empty;
        private static PredictionEntry[] _entries = PredictionSnapshot.EmptyEntries;

        // 诊断计数（Interlocked，无锁）：用于 status 与"建议是否真的送达引擎"的验证。
        private static long _calls;
        private static long _hits;
        private static long _published;

        public static bool Running
        {
            get { lock (Gate) { return _running; } }
            set { lock (Gate) { _running = value; } }
        }

        public static bool TabOnly
        {
            get { lock (Gate) { return _tabOnly; } }
            set { lock (Gate) { _tabOnly = value; } }
        }

        /// <summary>当前输入代号：每代 +1，worker 以它判断"这一代是否已取过"。</summary>
        public static long Gen
        {
            get { lock (Gate) { return _gen; } }
        }

        /// <summary>待取建议的输入（worker 读）。</summary>
        public static string Pending
        {
            get { lock (Gate) { return _pending; } }
        }

        /// <summary>
        /// 待取建议那一代的当前目录（worker 读）。
        /// 必须是控制台侧传来的：worker 在自己的 runspace 里 Get-Location 拿到的是
        /// 进程工作目录，与控制台当前位置无关（用户 cd 过之后就完全是另一个目录），
        /// 路径类建议与会话上报都会用错上下文。
        /// </summary>
        public static string PendingCwd
        {
            get { lock (Gate) { return _pendingCwd; } }
        }

        /// <summary>已发布快照对应的输入（Tab 兜底路径与诊断用）。</summary>
        public static string SnapshotInput
        {
            get { lock (Gate) { return _snapInput; } }
        }

        /// <summary>已发布快照的插入文本（接受建议时上报统计用）。</summary>
        public static string SnapshotFull
        {
            get { lock (Gate) { return _topFull; } }
        }

        /// <summary>
        /// 登记一轮输入并返回当前快照。只做锁内拷贝 + 代次自增，微秒级。
        /// Tab 模式（tabOnly）不登记：那条路径由 Tab 键处理器直接调伴侣进程。
        /// </summary>
        public static PredictionSnapshot Register(string input, string cwd)
        {
            var snap = new PredictionSnapshot();
            lock (Gate)
            {
                snap.TopText = _topText;
                snap.TopFull = _topFull;
                snap.Entries = _entries;
                snap.Current = (_snapInput == input);
                if (!_tabOnly)
                {
                    _pending = input;
                    // 与输入同代存：worker 校验过代次后再读，拿到的就是这一代的目录。
                    _pendingCwd = cwd ?? string.Empty;
                    _gen++;
                }
            }
            Interlocked.Increment(ref _calls);
            return snap;
        }

        /// <summary>worker 取建议前确认这一代输入仍然有效。</summary>
        public static bool IsCurrent(long gen)
        {
            lock (Gate) { return gen == _gen; }
        }

        /// <summary>worker 写回结果；代次已变说明输入已过时，丢弃。</summary>
        public static void Publish(long gen, string input, string topText, string topFull, string[] fulls, string[] sources)
        {
            lock (Gate)
            {
                if (gen != _gen) { return; }
                _snapInput = input ?? string.Empty;
                _topText = topText ?? string.Empty;
                _topFull = topFull ?? string.Empty;
                int n = fulls == null ? 0 : fulls.Length;
                var list = new List<PredictionEntry>(n);
                for (int i = 0; i < n; i++)
                {
                    string full = fulls[i];
                    if (string.IsNullOrEmpty(full)) { continue; }
                    string src = (sources != null && i < sources.Length) ? sources[i] : string.Empty;
                    list.Add(new PredictionEntry(full, src));
                }
                _entries = list.Count == 0 ? PredictionSnapshot.EmptyEntries : list.ToArray();
            }
            Interlocked.Increment(ref _published);
        }

        /// <summary>清空快照与待取输入（禁用/重载模块时用）。</summary>
        public static void Reset()
        {
            lock (Gate)
            {
                _pending = string.Empty;
                _snapInput = string.Empty;
                _topText = string.Empty;
                _topFull = string.Empty;
                _entries = PredictionSnapshot.EmptyEntries;
            }
        }

        /// <summary>一行诊断：调用次数 / 命中次数 / 已发布次数 / 当前代次 / 快照条数。</summary>
        public static string Stats()
        {
            int entries;
            long gen;
            bool current;
            lock (Gate)
            {
                entries = _entries.Length;
                gen = _gen;
                current = _snapInput == _pending && _pending.Length > 0;
            }
            return string.Format("calls={0} hits={1} published={2} gen={3} entries={4} current={5}",
                Interlocked.Read(ref _calls), Interlocked.Read(ref _hits), Interlocked.Read(ref _published),
                gen, entries, current);
        }

        /// <summary>命中计数由 GetSuggestion 在真的给出建议时自增。</summary>
        public static void MarkHit()
        {
            Interlocked.Increment(ref _hits);
        }
    }

    /// <summary>
    /// ICommandPredictor 的编译实现。Id 固定（幂等重注册按 Id 注销），
    /// GetSuggestion 全程编译代码且不分配除结果列表外的对象。
    /// </summary>
    public sealed class CmdPilotPredictorCore : ICommandPredictor
    {
        /// <summary>固定 Id：Enable-CmdPilot 的幂等重注册（先按 Id 注销）依赖它。</summary>
        public static readonly Guid PredictorId = new Guid("7f3a1c9e-2d5b-4a6f-9e8d-1c2b3a4d5e6f");

        // 最近构造的实例。worker 循环以"我还是当前实例吗"作为退出条件之一：模块被
        // Import-Module -Force 重载会构造新实例，旧实例的 worker 因此自行退出，不会
        // 变成读同一份静态状态、为每个输入起两次伴侣进程的僵尸循环。
        private static CmdPilotPredictorCore _current;

        // SuggestionPackage 是值类型且构造器拒绝空列表：以单个零宽空格（U+200B）表示
        // "无建议"（渲染宽度为 0，视觉上完全不可见；PSReadLine 聚合建议时因不以当前
        // 输入开头而被过滤掉）。用 \u200B 转义写，不写字面量——字面量在源码里不可见，
        // 极易被误改成空串，而空串会让 SuggestionPackage 构造器抛异常。
        private const string InvisibleSuggestion = "\u200B";

        private static readonly SuggestionPackage NoSuggestion = CreateNoSuggestion();

        private static readonly Dictionary<string, string> NoFunctions = new Dictionary<string, string>();

        private readonly string _companion;

        public CmdPilotPredictorCore(string companionPath)
        {
            _companion = companionPath ?? string.Empty;
            _current = this;
            CmdPilotPredictionState.Running = true;
        }

        public Guid Id { get { return PredictorId; } }

        public string Name { get { return "CmdPilot"; } }

        public string Description { get { return "CmdPilot 智能补全助手 (本地知识库 + OpenAI 兼容 AI + 频率推荐)"; } }

        public Dictionary<string, string> FunctionsToDefine { get { return NoFunctions; } }

        private static SuggestionPackage CreateNoSuggestion()
        {
            var list = new List<PredictiveSuggestion>(1);
            list.Add(new PredictiveSuggestion(InvisibleSuggestion));
            return new SuggestionPackage(list);
        }

        public SuggestionPackage GetSuggestion(PredictionClient client, PredictionContext context, CancellationToken cancellationToken)
        {
            // 引擎只给 20ms：这里不允许出现 PowerShell 执行、进程创建或文件 I/O。
            if (cancellationToken.IsCancellationRequested) { return NoSuggestion; }
            if (context == null || context.InputAst == null) { return NoSuggestion; }
            string input = context.InputAst.Extent.Text;
            if (string.IsNullOrWhiteSpace(input)) { return NoSuggestion; }

            PredictionSnapshot snap = CmdPilotPredictionState.Register(input, CurrentDirectory(client));
            if (CmdPilotPredictionState.TabOnly || !snap.Current) { return NoSuggestion; }

            var entries = new List<PredictiveSuggestion>(1 + snap.Entries.Length);
            if (!string.IsNullOrEmpty(snap.TopText))
            {
                entries.Add(new PredictiveSuggestion(snap.TopText));
            }
            foreach (PredictionEntry e in snap.Entries)
            {
                if (string.IsNullOrEmpty(e.Full)) { continue; }
                entries.Add(new PredictiveSuggestion(e.Full, e.Full + "  [" + e.Source + "]"));
            }
            if (entries.Count == 0) { return NoSuggestion; }

            CmdPilotPredictionState.MarkHit();
            return new SuggestionPackage(entries);
        }

        public bool CanAcceptFeedback(PredictionClient client, PredictorFeedbackKind feedback)
        {
            return true;
        }

        public void OnSuggestionDisplayed(PredictionClient client, uint session, int countOrIndex)
        {
        }

        /// <summary>接受建议即视为执行一次（M5 统计）——与旧 PS 实现同语义：上报整条命令。</summary>
        public void OnSuggestionAccepted(PredictionClient client, uint session, string acceptedSuggestion)
        {
            string full = CmdPilotPredictionState.SnapshotFull;
            if (string.IsNullOrEmpty(full)) { full = acceptedSuggestion; }
            Report(full, CurrentDirectory(client));
        }

        public void OnCommandLineAccepted(PredictionClient client, IReadOnlyList<string> history)
        {
        }

        /// <summary>每条命令执行后记录（M5 统计，覆盖手输命令）。</summary>
        public void OnCommandLineExecuted(PredictionClient client, string commandLine, bool success)
        {
            if (string.IsNullOrWhiteSpace(commandLine)) { return; }
            Report(commandLine, CurrentDirectory(client));
        }

        public void Dispose()
        {
            // 只置运行标志：worker 的 PowerShell 循环下一轮自行退出，runspace 由
            // 模块的 Stop-CmdPilotWorker 收尾（这里不能碰 PowerShell）。
            CmdPilotPredictionState.Running = false;
        }

        // ------------------------------------------------------------------
        // 供模块与后台 worker 调用的实例接口。
        // 全部是**实例方法**：PowerShell 侧（尤其 worker 的独立 runspace）只持有对象
        // 引用，方法绑定按对象类型走，无需解析 C# 类型名，也就不会踩"新 runspace 里
        // 找不到 Add-Type 类型"的坑。
        // ------------------------------------------------------------------

        /// <summary>Tab 模式：只由 Tab 键处理器补全，预测器不给建议（与旧 PS 基类同名）。</summary>
        public void SetTabOnly(bool value)
        {
            CmdPilotPredictionState.TabOnly = value;
        }

        public bool GetTabOnly()
        {
            return CmdPilotPredictionState.TabOnly;
        }

        /// <summary>worker 是否应当继续运行（仅看运行标志）。</summary>
        public bool IsRunning()
        {
            return CmdPilotPredictionState.Running;
        }

        /// <summary>
        /// worker 循环条件：运行标志为真**且**本实例仍是当前实例。后者用于模块重载时
        /// 让旧实例的 worker 自行退出（见 _current 注释）。
        /// </summary>
        public bool WorkerAlive()
        {
            return CmdPilotPredictionState.Running && ReferenceEquals(_current, this);
        }

        /// <summary>当前输入代号（worker 用它判断"这一代是否已取过"）。</summary>
        public long WorkerGen()
        {
            return CmdPilotPredictionState.Gen;
        }

        /// <summary>待取建议的输入（worker 取建议的入参）。</summary>
        public string WorkerPending()
        {
            return CmdPilotPredictionState.Pending;
        }

        /// <summary>取建议前后各查一次：为假说明用户又敲了键，这一代结果作废。</summary>
        public bool WorkerIsCurrent(long gen)
        {
            return CmdPilotPredictionState.IsCurrent(gen);
        }

        /// <summary>
        /// 待取建议那一代的当前目录（worker 拼请求用）。
        /// 不要用 worker 自己 runspace 的 Get-Location：那是进程工作目录，用户 cd 过
        /// 之后就是错的。查过 WorkerIsCurrent 之后再读，拿到的必然是这一代的目录。
        /// </summary>
        public string WorkerCwd()
        {
            return CmdPilotPredictionState.PendingCwd;
        }

        /// <summary>
        /// worker 发布一代结果（伴侣进程 JSON 已在 PowerShell 侧解析成字段）。
        /// 代次已过时则由 Publish 内部丢弃。
        /// </summary>
        public void WorkerPublish(long gen, string input, string topText, string topFull, string[] fulls, string[] sources)
        {
            CmdPilotPredictionState.Publish(gen, input, topText, topFull, fulls, sources);
        }

        /// <summary>停 worker 循环（Disable-CmdPilot / Dispose 用；不碰 PowerShell）。</summary>
        public void StopWorker()
        {
            CmdPilotPredictionState.Running = false;
        }

        /// <summary>清空快照（Disable-CmdPilot 用，避免残留建议被再次命中）。</summary>
        public void ResetSnapshot()
        {
            CmdPilotPredictionState.Reset();
        }

        /// <summary>一行诊断（Get-CmdPilotStatus 用）：调用/命中/发布次数与快照条数。</summary>
        public string StateStats()
        {
            return CmdPilotPredictionState.Stats();
        }

        private static string CurrentDirectory(PredictionClient client)
        {
            try
            {
                if (client != null && client.CurrentLocation != null) { return client.CurrentLocation.Path ?? string.Empty; }
            }
            catch
            {
            }
            return string.Empty;
        }

        /// <summary>
        /// 上报统计：直接起伴侣进程（与旧 PS 版 Send-CmdPilotReport 等价，含同样的
        /// 1000ms 上限与静默失败），避免为此在引擎回调线程上执行 PowerShell。
        /// </summary>
        private void Report(string command, string dir)
        {
            if (string.IsNullOrEmpty(command) || string.IsNullOrEmpty(_companion)) { return; }
            try
            {
                var psi = new ProcessStartInfo();
                psi.FileName = _companion;
                psi.Arguments = Quote("--report") + " " + Quote(command)
                    + " " + Quote("--report-dir") + " " + Quote(dir)
                    + " " + Quote("--report-shell") + " " + Quote("ps");
                psi.UseShellExecute = false;
                psi.CreateNoWindow = true;
                Process p = Process.Start(psi);
                if (p != null)
                {
                    p.WaitForExit(1000);
                    p.Dispose();
                }
            }
            catch
            {
                // 守护进程不可用：静默降级，绝不把异常抛回宿主。
            }
        }

        // .NET 命令行解析规则：参数用双引号包裹，参数内双引号以 "" 转义（与 PS 侧一致）。
        private static string Quote(string value)
        {
            return "\"" + (value ?? string.Empty).Replace("\"", "\"\"") + "\"";
        }
    }
}
