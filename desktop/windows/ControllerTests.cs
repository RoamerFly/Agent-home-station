using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;

namespace WechatAgent.Desktop {
    internal sealed class FakeCommands : ICommandRunner {
        internal List<string> Calls = new List<string>();
        internal bool Stopped, Busy, ProbeFailure, StopFailure, AuthWarmup;
        public Task<CommandResult> Run(string file, string[] args, CancellationToken unused) {
            var command = String.Join(" ", args.Skip(5)); Calls.Add(command);
            int code = 0; string output = "";
            if (command.StartsWith("cat ")) output = "synthetic-password";
            if (command.Contains("auth status")) { if (AuthWarmup) { AuthWarmup = false; code = 3; } else output = "{\"authenticated\":true}"; }
            if (command.StartsWith("wslpath ")) output = "/mnt/e/synthetic workspace";
            if (command.StartsWith("python3 ")) { output = Busy ? "1" : "0"; if (ProbeFailure) code = 2; }
            if (command.Contains(" stop ")) { if (StopFailure) code = 1; else Stopped = true; }
            if (command.Contains(" is-active ")) { code = Stopped ? 3 : 0; output = Stopped ? "inactive\n" : "active\n"; }
            return Task.FromResult(new CommandResult { Code = code, Output = output });
        }
    }
    internal sealed class FakeWeb : HttpMessageHandler {
        internal List<string> Calls = new List<string>();
        internal bool Ready = true, WrongAccount, Offline, FailDrain;
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken unused) {
            string path = request.RequestUri.PathAndQuery; Calls.Add(path);
            if (Offline && path == "/healthz") throw new HttpRequestException("offline");
            if (FailDrain && path.StartsWith("/api/runtime/drain")) return Task.FromResult(new HttpResponseMessage(HttpStatusCode.Conflict));
            string body = "{}";
            if (path == "/healthz") body = "{\"ok\":true}";
            if (path == "/api/login") body = "{\"csrf\":\"synthetic-csrf\"}";
            if (path == "/api/status") body = "{\"account\":\"" + (WrongAccount ? "other-account" : "synthetic") + "\",\"native\":{\"authenticated\":true}}";
            if (path == "/api/runtime/drain") body = "{\"ticket\":\"synthetic-ticket\"}";
            if (path.StartsWith("/api/runtime/drain?")) body = "{\"ready\":" + (Ready ? "true" : "false") + "}";
            return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent(body) });
        }
    }
    internal static class ControllerTests {
        static int count;
        static void Assert(bool condition, string reason) { if (!condition) throw new Exception(reason); count++; }
        static Deployment Config() { return new Deployment { Workspace = @"E:\synthetic workspace", Distro = "Synthetic", User = "synthetic", Root = "/home/synthetic/data", Account = "synthetic", Music = "wechat-agent-music.service" }; }
        static async Task Reject(Func<Task> action) { bool failed = false; try { await action(); } catch (Exception) { failed = true; } Assert(failed, "Expected refusal"); }
        static async Task Tests() {
            Assert(HiddenRunner.Quote("plain") == "plain", "plain argv");
            Assert(HiddenRunner.Quote("") == "\"\"", "empty argv");
            Assert(HiddenRunner.Quote("space end\\") == "\"space end\\\\\"", "trailing slash");
            Assert(HiddenRunner.Quote("a\"b") == "\"a\\\"b\"", "embedded quote");
            Assert(HiddenRunner.Quote("$()`&") == "$()`&", "no shell evaluation");
            Assert(WindowsRuntime.ComparablePath(@"\\?\E:\synthetic path\data") == @"E:\synthetic path\data", "Rust canonical Windows paths retain identity");
            var command = new FakeCommands(); var web = new FakeWeb();
            using (var c = new ServiceController(Config(), command, web)) {
                await c.Start(CancellationToken.None);
                Assert(command.Calls.Any(x => x == "systemctl --user start wechat-bot-native.service wechat-bot-web.service"), "start existing units");
                Assert(!command.Calls.Any(x => x.Contains(" connect ")), "already authenticated native login preserved");
                await c.Stop(CancellationToken.None);
                var stops = command.Calls.Where(x => x.Contains(" stop ")).ToArray();
                Assert(stops.SequenceEqual(new [] { "systemctl --user stop wechat-bot-web.service", "systemctl --user stop wechat-agent-music.service", "systemctl --user stop wechat-bot-native.service" }), "stop order");
                Assert(web.Calls.Contains("/api/runtime/drain?ticket=synthetic-ticket"), "must drain before stop");
                Assert(!command.Calls.Any(x => x.Contains("--terminate") || x.Contains("shutdown") || x.Contains("powershell")), "never terminate shared WSL or launch scripts");
            }
            command = new FakeCommands { Busy = true }; web = new FakeWeb(); var cancel = new CancellationTokenSource();
            using (var c = new ServiceController(Config(), command, web, (t, ct) => { cancel.Cancel(); ct.ThrowIfCancellationRequested(); return Task.FromResult(0); })) {
                await Reject(() => c.Stop(cancel.Token));
                Assert(!command.Calls.Any(x => x.Contains(" stop ")), "busy cancellation does not stop units");
                Assert(web.Calls.Contains("/api/runtime/undrain"), "cancel resumes work");
            }
            command = new FakeCommands { AuthWarmup = true }; web = new FakeWeb();
            using (var c = new ServiceController(Config(), command, web, (t, ct) => Task.FromResult(0))) {
                await c.Start(CancellationToken.None);
                Assert(command.Calls.Count(x => x.Contains("auth status")) == 2, "cold native readiness retry");
                Assert(!command.Calls.Any(x => x.Contains(" connect ")), "no reconnect during native warmup");
            }
            command = new FakeCommands { ProbeFailure = true }; web = new FakeWeb();
            using (var c = new ServiceController(Config(), command, web)) {
                await Reject(() => c.Stop(CancellationToken.None)); Assert(!command.Calls.Any(x => x.Contains(" stop ")), "unknown busy state refuses stop");
                Assert(web.Calls.Contains("/api/runtime/undrain"), "failed probe releases lease");
            }
            command = new FakeCommands(); web = new FakeWeb { WrongAccount = true };
            using (var c = new ServiceController(Config(), command, web)) {
                await Reject(() => c.Stop(CancellationToken.None)); Assert(!web.Calls.Contains("/api/runtime/drain"), "other account not drained"); Assert(!command.Calls.Any(x => x.Contains(" stop ")), "other account not stopped");
            }
            command = new FakeCommands(); web = new FakeWeb { Offline = true };
            using (var c = new ServiceController(Config(), command, web)) { await Reject(() => c.Stop(CancellationToken.None)); Assert(!command.Calls.Any(x => x.Contains(" stop ")), "unresponsive active web not treated as drained"); }
            command = new FakeCommands(); web = new FakeWeb { FailDrain = true };
            using (var c = new ServiceController(Config(), command, web)) { await Reject(() => c.Stop(CancellationToken.None)); Assert(!command.Calls.Any(x => x.Contains(" stop ")), "drain admission failure refuses stop"); }
            command = new FakeCommands { StopFailure = true }; web = new FakeWeb();
            using (var c = new ServiceController(Config(), command, web)) { await Reject(() => c.Stop(CancellationToken.None)); Assert(command.Calls.Count(x => x.Contains(" stop ")) == 1, "do not stop native after web stop fails"); Assert(web.Calls.Contains("/api/runtime/undrain"), "web stop failure resumes worker"); }
            string root = Path.Combine(Path.GetTempPath(), "desktop synthetic " + Guid.NewGuid()); Directory.CreateDirectory(Path.Combine(root, "secrets"));
            try {
                var path = Path.Combine(root, "secrets", "local-deployment.json");
                File.WriteAllText(path, "{\"backend\":\"wsl\",\"distro\":\"Synthetic\",\"linux_user\":\"synthetic\",\"data_root\":\"/home/synthetic/data\",\"account\":\"synthetic\",\"music_service\":\"unrelated.service\"}");
                await Reject(() => { Deployment.Load(root); return Task.FromResult(0); });
                File.WriteAllText(path, File.ReadAllText(path).Replace("unrelated.service", "wechat-agent-music.service"));
                Assert(Deployment.Load(root).Workspace == Path.GetFullPath(root), "workspace relocation");
            } finally { Directory.Delete(root, true); }
            root = Path.Combine(Path.GetTempPath(), "desktop windows native " + Guid.NewGuid()); Directory.CreateDirectory(root);
            try {
                var d = Deployment.Load(root);
                var close = new CloseSettings(root);
                Assert(close.Behavior == "ask", "close choice is asked by default");
                close.Save("tray"); Assert(new CloseSettings(root).Behavior == "tray", "remember tray across restart");
                close.Save("exit"); Assert(new CloseSettings(root).Behavior == "exit", "remember exit across restart");
                Assert(File.Exists(Path.Combine(root, "secrets", "desktop-settings.json.bak")), "previous close choice retained as backup");
                close.Save("ask"); Assert(new CloseSettings(root).Behavior == "ask", "restore close prompt from tray settings");
                await Reject(() => { close.Save("unexpected"); return Task.FromResult(0); });
                Assert(d.Windows && d.Root == Path.Combine(root, "data"), "fresh installation defaults to native Windows");
                Assert(d.Identity == Deployment.Load(root + Path.DirectorySeparatorChar).Identity, "shortcut and direct launch share one instance");
                Assert(d.NativeExe == Path.Combine(root, "runtimes/windows-x64/wx.exe".Replace('/', Path.DirectorySeparatorChar)), "native executable resolved from portable root");
                Directory.CreateDirectory(Path.Combine(root, "secrets"));
                var config = Path.Combine(root, "secrets", "local-deployment.json");
                File.WriteAllText(config, "{\"backend\":\"windows-native\",\"data_root\":\"data with 空格\",\"admin_url\":\"http://127.0.0.1:19081\",\"native_url\":\"http://127.0.0.1:19174\",\"account\":\"synthetic\"}");
                d = Deployment.Load(root);
                Assert(d.Root == Path.Combine(root, "data with 空格") && d.Url.EndsWith(":19081") && d.NativeUrl.EndsWith(":19174"), "portable data and port overrides");
                File.WriteAllText(config, File.ReadAllText(config).Replace("127.0.0.1:19081", "example.com:19081"));
                await Reject(() => { Deployment.Load(root); return Task.FromResult(0); });
                File.WriteAllText(config, "{\"backend\":\"unknown\"}");
                await Reject(() => { Deployment.Load(root); return Task.FromResult(0); });
                d = Deployment.WindowsDefault(root);
                Directory.CreateDirectory(Path.GetDirectoryName(d.NativeExe));
                File.WriteAllText(d.NativeExe, "synthetic executable"); File.WriteAllText(d.AdminExe, "synthetic executable");
                string hash; using (var sha = System.Security.Cryptography.SHA256.Create()) hash = BitConverter.ToString(sha.ComputeHash(File.ReadAllBytes(d.NativeExe))).Replace("-", "");
                File.WriteAllText(Path.Combine(Path.GetDirectoryName(d.NativeExe), "windows-native-manifest.json"), "{\"native_sha256\":\"" + hash + "\",\"admin_sha256\":\"" + hash + "\"}");
                WindowsRuntime.VerifyBundle(d); Assert(true, "bundle checksums match");
                File.AppendAllText(d.NativeExe, "tamper");
                await Reject(() => { WindowsRuntime.VerifyBundle(d); return Task.FromResult(0); });
                using (var job = new ProcessJob()) {
                    var child = Process.Start(HiddenRunner.Info(System.Reflection.Assembly.GetExecutingAssembly().Location, new [] { "--synthetic-child" }));
                    job.Assign(child); job.Dispose();
                    Assert(child.WaitForExit(5000), "owned job closes its process without taskkill or WSL"); child.Dispose();
                }
            } finally { Directory.Delete(root, true); }
            root = Path.Combine(Path.GetTempPath(), "desktop 合成 probe " + Guid.NewGuid()); Directory.CreateDirectory(root);
            try {
                var real = new HiddenRunner();
                string setup = "from pathlib import Path; import sqlite3,sys; p=Path(sys.argv[1]); a=p/'accounts/synthetic/agent'; a.mkdir(parents=True); c=sqlite3.connect(a/'control.sqlite'); c.execute('create table media_jobs(state text)'); c.executemany('insert into media_jobs values (?)',[('running',),('accepted',)]); c.commit(); c.close(); m=p/'pri_workspace/synthetic/music/jobs'; m.mkdir(parents=True); (m/'jobs.json').write_text('{\"a\":{\"state\":\"queued\"},\"b\":{\"state\":\"completed\"}}')";
                var prepared = await real.Run("python", new [] { "-c", setup, root }, CancellationToken.None);
                Assert(prepared.Code == 0, "synthetic probe setup");
                var probe = await real.Run("python", new [] { "-c", ServiceController.BusyProbe, root, root }, CancellationToken.None);
                Assert(probe.Code == 0 && probe.Output.Trim() == "2", "real native/music busy probe on unicode path");
                File.WriteAllText(Path.Combine(root, "pri_workspace/synthetic/music/jobs/jobs.json"), "invalid json");
                probe = await real.Run("python", new [] { "-c", ServiceController.BusyProbe, root, root }, CancellationToken.None);
                Assert(probe.Code != 0, "corrupt music status cannot authorize stop");
            } finally { Directory.Delete(root, true); }
        }
        static int Main(string[] args) { try {
            Console.OutputEncoding = new System.Text.UTF8Encoding(false);
            // Only this test executable implements these synthetic server
            // responses; the shipped Go executable uses its real SQLite probe.
            if (args.Length == 1 && args[0] == "--desktop") return 23;
            if (args.Length == 2 && args[0] == "desktop-busy") { Console.WriteLine("{\"pending\":0}"); return 0; }
            if (args.Length == 2 && args[0] == "--failure-cycle") {
                var config = Deployment.Load(args[1]);
                if (!config.Windows) throw new Exception("failure cycle requires isolated native deployment");
                using (var c = new ServiceController(config)) {
                    for (int i = 0; i < 2; i++) {
                        bool exited = false;
                        try { c.Start(CancellationToken.None).GetAwaiter().GetResult(); }
                        catch (InvalidOperationException e) { exited = e.Message.Contains("提前退出"); }
                        Assert(exited, "early Web exit must remain recoverable without leaked log handles");
                        c.Stop(CancellationToken.None).GetAwaiter().GetResult();
                    }
                }
                Console.WriteLine("Native Windows failed-start recovery passed twice."); return 0;
            }
            if (args.Length == 1 && args[0] == "--synthetic-child") { Thread.Sleep(60000); return 0; }
            if (args.Length == 2 && args[0] == "--cycle") {
                var config = Deployment.Load(args[1]);
                if (!config.Windows) throw new Exception("cycle requires isolated Windows native deployment");
                using (var c = new ServiceController(config)) {
                    c.Progress = Console.WriteLine;
                    for (int i = 0; i < 2; i++) { c.Start(CancellationToken.None).GetAwaiter().GetResult(); c.Stop(CancellationToken.None).GetAwaiter().GetResult(); }
                }
                Console.WriteLine("Native Windows desktop cycle passed twice; no login or message send."); return 0;
            }
            if (args.Length == 2 && (args[0] == "--probe" || args[0] == "--start" || args[0] == "--stop")) {
                var d = Deployment.Load(args[1]);
                using (var c = new ServiceController(d)) {
                    if (args[0] == "--start" || args[0] == "--stop") {
                        c.Progress = Console.WriteLine;
                        if (args[0] == "--start") c.Start(CancellationToken.None).GetAwaiter().GetResult();
                        else c.Stop(CancellationToken.None).GetAwaiter().GetResult();
                        return 0;
                    }
                    var workspace = c.Wsl(CancellationToken.None, "wslpath", "-a", d.Workspace).GetAwaiter().GetResult();
                    if (workspace.Code != 0) throw new Exception("wslpath failed");
                    var probe = c.Wsl(CancellationToken.None, "python3", "-c", ServiceController.BusyProbe, d.Root, workspace.Output.Trim()).GetAwaiter().GetResult();
                    Console.WriteLine("Probe exit=" + probe.Code + " count=" + probe.Output.Trim()); return probe.Code;
                }
            }
            Tests().GetAwaiter().GetResult(); Console.WriteLine("Desktop controller assertions passed: " + count); return 0;
        } catch (Exception e) { Console.Error.WriteLine(e); return 1; } }
    }
}
