using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Runtime.InteropServices;
using System.Security.AccessControl;
using System.Security.Cryptography;
using System.Security.Principal;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Win32.SafeHandles;

namespace WechatAgent.Desktop {
    // The desktop owns foreground processes, rather than nesting a detached
    // daemon inside redirected pipes. A Job Object also owns their descendants.
    internal sealed class WindowsRuntime : IDisposable {
        readonly Deployment d;
        readonly ICommandRunner commands;
        readonly HttpClient api = new HttpClient(new HttpClientHandler { UseProxy = false, AllowAutoRedirect = false }) { Timeout = TimeSpan.FromSeconds(3) };
        OwnedProcess native, web;
        ProcessJob job;
        string serverId, generation;
        internal WindowsRuntime(Deployment config, ICommandRunner runner) { d = config; commands = runner; }
        internal bool WebRunning { get { return web != null && web.Alive; } }
        internal bool AnyRunning { get { return WebRunning || native != null && native.Alive; } }
        internal static string ComparablePath(string value) {
            if (value.StartsWith(@"\\?\UNC\", StringComparison.OrdinalIgnoreCase)) value = @"\\" + value.Substring(8);
            else if (value.StartsWith(@"\\?\", StringComparison.Ordinal)) value = value.Substring(4);
            return Path.GetFullPath(value).TrimEnd(Path.DirectorySeparatorChar);
        }
        internal void CheckAlive() {
            if (native == null || !native.Alive || web == null || !web.Alive) throw new InvalidOperationException("Windows 服务提前退出，请查看 data/logs；没有重放发送或清空数据。");
        }
        internal static void NoLinks(string path) {
            for (var item = new DirectoryInfo(Path.GetFullPath(path)); item != null; item = item.Parent)
                if (item.Exists && (item.Attributes & FileAttributes.ReparsePoint) != 0) throw new InvalidOperationException("数据和程序路径不能穿过链接或重解析点。");
        }
        internal static void PrivateDirectory(string path) {
            NoLinks(path);
            var user = WindowsIdentity.GetCurrent().User;
            // Elevated Windows tokens may default new ownership to Administrators.
            // Explicit ownership must agree with the native backend's user SID.
            var exists = Directory.Exists(path);
            if (exists && !user.Equals(Directory.GetAccessControl(path, AccessControlSections.Owner).GetOwner(typeof(SecurityIdentifier))))
                throw new InvalidOperationException("数据目录不属于当前用户，拒绝接管或自动更改所有者。");
            var acl = new DirectorySecurity();
            if (!exists) acl.SetOwner(user);
            acl.SetAccessRuleProtection(true, false);
            var inherit = InheritanceFlags.ContainerInherit | InheritanceFlags.ObjectInherit;
            acl.AddAccessRule(new FileSystemAccessRule(user, FileSystemRights.FullControl, inherit, PropagationFlags.None, AccessControlType.Allow));
            acl.AddAccessRule(new FileSystemAccessRule(new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null), FileSystemRights.FullControl, inherit, PropagationFlags.None, AccessControlType.Allow));
            if (!exists) Directory.CreateDirectory(path, acl); else Directory.SetAccessControl(path, acl);
        }
        internal static void VerifyBundle(Deployment config) {
            var dir = Path.GetDirectoryName(config.NativeExe); NoLinks(dir); NoLinks(config.Root);
            var manifest = Deployment.Json.Deserialize<Dictionary<string, object>>(File.ReadAllText(Path.Combine(dir, "windows-native-manifest.json"), Encoding.UTF8));
            foreach (var item in new [] { new [] { config.NativeExe, "native_sha256" }, new [] { config.AdminExe, "admin_sha256" } }) {
                if ((File.GetAttributes(item[0]) & FileAttributes.ReparsePoint) != 0) throw new InvalidOperationException("运行文件不能使用链接。");
                using (var stream = File.OpenRead(item[0])) using (var sha = SHA256.Create())
                    if (!String.Equals(BitConverter.ToString(sha.ComputeHash(stream)).Replace("-", ""), Deployment.Text(manifest, item[1]), StringComparison.OrdinalIgnoreCase)) throw new InvalidOperationException("Windows 运行文件校验失败，请重新构建完整程序包；用户数据已保留。");
            }
            var resources = Path.GetFullPath(Path.Combine(dir, ".."));
            foreach (var name in new [] { "windows-python-cli", "windows-media", "windows-music" }) {
                var folder = Path.Combine(resources, name);
                if (!Directory.Exists(folder)) continue; // earlier native-only packages remain usable
                VerifyResourceDirectory(folder);
            }
        }
        internal static void VerifyResourceDirectory(string folder) {
            NoLinks(folder);
            var record = Deployment.Json.Deserialize<Dictionary<string, object>>(File.ReadAllText(Path.Combine(folder, "runtime-manifest.json"), Encoding.UTF8));
            var files = record["files"] as Dictionary<string, object>;
            if (files == null || files.Count == 0) throw new InvalidOperationException("托管运行环境清单为空。");
            foreach (var entry in files) {
                var target = Path.GetFullPath(Path.Combine(folder, entry.Key));
                if (!target.StartsWith(Path.GetFullPath(folder).TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase)) throw new InvalidOperationException("托管运行环境清单包含越界路径。");
                NoLinks(target);
                using (var stream = File.OpenRead(target)) using (var sha = SHA256.Create())
                    if (!String.Equals(BitConverter.ToString(sha.ComputeHash(stream)).Replace("-", ""), Convert.ToString(entry.Value), StringComparison.OrdinalIgnoreCase)) throw new InvalidOperationException("项目内运行环境校验失败；请修复该环境，用户资料已保留。");
            }
        }
        static void FreePort(string url) {
            var uri = new Uri(url); var listener = new System.Net.Sockets.TcpListener(IPAddress.Loopback, uri.Port);
            try { listener.Server.ExclusiveAddressUse = true; listener.Start(); }
            catch (System.Net.Sockets.SocketException) { throw new InvalidOperationException("本机端口 " + uri.Port + " 已占用；拒绝接管或结束其他服务。"); }
            finally { listener.Stop(); }
        }
        string[] NativeArgs(params string[] tail) {
            return new [] { "--json", "--server-config", Path.Combine(d.Root, "native-server.toml"), "--data-dir", d.Root, "--listen", new Uri(d.NativeUrl).Authority }.Concat(tail).ToArray();
        }
        internal async Task Start(CancellationToken cancel) {
            VerifyBundle(d);
            if (AnyRunning) { CheckAlive(); return; }
            // A crashed process can still own its log/pipe handles. Release
            // the stopped instance before retrying, without adopting any PID.
            if (native != null || web != null) await Stop();
            FreePort(d.Url); FreePort(d.NativeUrl);
            PrivateDirectory(d.Root);
            PrivateDirectory(Path.Combine(d.Workspace, "secrets"));
            Directory.CreateDirectory(Path.Combine(d.Root, "logs"));
            // Explicit config avoids inheriting another installed wx instance's
            // defaults. Existing config is preserved and only read by wx.
            var serverConfig = Path.Combine(d.Root, "native-server.toml");
            if (!File.Exists(serverConfig)) using (var f = new FileStream(serverConfig, FileMode.CreateNew)) {
                var bytes = Encoding.UTF8.GetBytes("schemaVersion = 1\n"); f.Write(bytes, 0, bytes.Length);
            }
            var adminDir = Path.Combine(d.Root, "admin"); Directory.CreateDirectory(adminDir);
            if (!File.Exists(Path.Combine(adminDir, "admin-token")) && !File.Exists(Path.Combine(adminDir, "admin-password"))) {
                var bytes = new byte[32]; using (var rng = RandomNumberGenerator.Create()) rng.GetBytes(bytes);
                using (var f = new FileStream(Path.Combine(adminDir, "admin-token"), FileMode.CreateNew)) {
                    var text = Encoding.UTF8.GetBytes(Convert.ToBase64String(bytes)); f.Write(text, 0, text.Length);
                }
            }
            job = new ProcessJob();
            serverId = generation = null;
            var resources = Path.GetFullPath(Path.Combine(Path.GetDirectoryName(d.NativeExe), ".."));
            var media = Path.Combine(resources, "windows-media");
            var programEnv = new Dictionary<string, string>();
            if (File.Exists(Path.Combine(media, "ffmpeg.exe")) && File.Exists(Path.Combine(media, "ffprobe.exe")))
                programEnv["PATH"] = media + Path.PathSeparator + Environment.GetEnvironmentVariable("PATH");
            native = OwnedProcess.Start(d.NativeExe, NativeArgs("serve"), d.Workspace, programEnv, Path.Combine(d.Root, "logs", "native.log"), job);
            var deadline = DateTime.UtcNow.AddSeconds(90);
            while (true) {
                cancel.ThrowIfCancellationRequested();
                if (!native.Alive) throw new InvalidOperationException("Windows 微信后台启动失败（退出码 0x" + native.ExitCode.ToString("X8") + "），请查看 data/logs/native.log。若为 0xC0000135，请检查 Microsoft Visual C++ x64 运行库。");
                try {
                    var value = await NativeRequest("/v1/status", null);
                    if (Convert.ToInt32(value["pid"]) != native.ID || !String.Equals(ComparablePath(Deployment.Text(value, "dataDir")), ComparablePath(d.Root), StringComparison.OrdinalIgnoreCase)) throw new InvalidOperationException("微信后台实例身份不匹配，拒绝接管。");
                    serverId = Deployment.Text(value, "serverId"); generation = Deployment.Text(value, "ownershipGeneration");
                    if (serverId == "" || generation == "") throw new InvalidOperationException("微信后台缺少实例归属信息，拒绝接管。");
                    break;
                } catch (HttpRequestException) { } catch (TaskCanceledException) { } catch (FileNotFoundException) { }
                if (DateTime.UtcNow > deadline) throw new InvalidOperationException("Windows 微信后台未就绪，请检查日志。");
                await Task.Delay(300, cancel);
            }
            using (var request = NativeMessage("/v1/accounts/" + Uri.EscapeDataString(d.Account), null)) using (var response = await api.SendAsync(request)) {
                if (response.StatusCode == HttpStatusCode.NotFound) await NativeRequest("/v1/accounts", new { id = d.Account, alias = d.Account });
                else if (!response.IsSuccessStatusCode) throw new InvalidOperationException("微信账号状态无法核验，没有自动重新创建。");
            }
            var env = new Dictionary<string, string> {
                { "WECHAT_ADMIN_DATA_DIR", adminDir }, { "WECHAT_BOT_STATE_DIR", Path.Combine(d.Root, "bot-state") },
                { "WECHAT_ADMIN_LISTEN", new Uri(d.Url).Authority }, { "WECHAT_ADMIN_ORIGIN", d.Url },
                { "WECHAT_NATIVE_URL", d.NativeUrl }, { "WECHAT_NATIVE_TOKEN_FILE", Path.Combine(d.Root, "api-token") },
                { "WECHAT_NATIVE_ACCOUNT", d.Account }, { "WECHAT_WORKSPACE_ROOT", Path.Combine(d.Root, "managed-workspaces") }
            };
            foreach (var pair in programEnv) env[pair.Key] = pair.Value;
            var pythonw = Path.Combine(resources, "windows-python-cli", "pythonw.exe");
            var helper = Path.Combine(resources, "windows-agent-host", "windows_agent_host.pyw");
            if (File.Exists(pythonw) && File.Exists(helper)) env["WECHAT_WINDOWS_BROKER_CONFIG"] = Deployment.Json.Serialize(new {
                pythonw = pythonw, helper = helper, spool = Path.Combine(adminDir, "windows-broker-spool")
            });
            var pluginHelper = Path.Combine(resources, "windows-agent-host", "windows_plugin_host.pyw");
            if (File.Exists(pythonw) && File.Exists(pluginHelper)) env["WECHAT_PLUGIN_RUNTIME_CONFIG"] = Deployment.Json.Serialize(new {
                python = Path.Combine(resources, "windows-python-cli", "python.exe"), pythonw = pythonw,
                windows_helper = pluginHelper, runtime_roots = new [] { Path.GetDirectoryName(pythonw) },
                catalog_directory = Path.Combine(d.Workspace, "plugins")
            });
            web = OwnedProcess.Start(d.AdminExe, new [] { "--desktop" }, d.Workspace, env, Path.Combine(d.Root, "logs", "web.log"), job);
        }
        HttpRequestMessage NativeMessage(string path, object body) {
            var request = new HttpRequestMessage(body == null ? HttpMethod.Get : HttpMethod.Post, d.NativeUrl + path);
            request.Headers.Authorization = new System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", File.ReadAllText(Path.Combine(d.Root, "api-token"), Encoding.UTF8).Trim());
            if (body != null) request.Content = new StringContent(Deployment.Json.Serialize(body), Encoding.UTF8, "application/json");
            return request;
        }
        async Task<Dictionary<string, object>> NativeRequest(string path, object body) {
            using (var request = NativeMessage(path, body)) using (var response = await api.SendAsync(request)) {
                response.EnsureSuccessStatusCode(); return Deployment.Json.Deserialize<Dictionary<string, object>>(await response.Content.ReadAsStringAsync());
            }
        }
        internal async Task<int> Pending(CancellationToken cancel) {
            var result = await commands.Run(d.AdminExe, new [] { "desktop-busy", d.Root }, cancel);
            if (result.Code != 0) throw new InvalidOperationException("无法核验微信在途任务，保持服务运行。");
            var data = Deployment.Json.Deserialize<Dictionary<string, object>>(result.Output);
            int pending = Convert.ToInt32(data["pending"]);
            if (pending < 0) throw new InvalidOperationException("在途任务状态无效。");
            return pending;
        }
        internal async Task Stop() {
            if (web != null) {
                if (web.Alive) await web.StopWeb(); else await web.WaitStopped();
                web.Dispose(); web = null;
            }
            if (native != null) {
                if (native.Alive && (String.IsNullOrEmpty(serverId) || String.IsNullOrEmpty(generation))) {
                    // Startup failed before admission; this process cannot have
                    // received work through our Web. Only terminate our child.
                    native.StopUnadmitted();
                } else if (native.Alive) {
                    await NativeRequest("/v1/daemon/shutdown", new { pid = native.ID, serverId = serverId, ownershipGeneration = generation });
                }
                await native.WaitStopped(); native.Dispose(); native = null;
            }
            if (job != null) { job.Dispose(); job = null; }
        }
        public void Dispose() {
            api.Dispose();
            if (web != null) web.Dispose(); if (native != null) native.Dispose();
            if (job != null) job.Dispose();
        }
    }

    internal sealed class OwnedProcess : IDisposable {
        readonly Process process;
        readonly StreamWriter log;
        readonly Task output, error;
        readonly object gate = new object();
        internal bool Alive { get { return !process.HasExited; } }
        internal int ID { get { return process.Id; } }
        internal int ExitCode { get { return process.ExitCode; } }
        OwnedProcess(Process child, StreamWriter writer) {
            process = child; log = writer;
            output = Pump(child.StandardOutput); error = Pump(child.StandardError);
        }
        async Task Pump(StreamReader stream) { string line; while ((line = await stream.ReadLineAsync()) != null) lock (gate) { log.WriteLine(DateTime.UtcNow.ToString("o") + " " + line); log.Flush(); } }
        internal static OwnedProcess Start(string exe, string[] args, string workspace, Dictionary<string, string> environment, string logPath, ProcessJob job) {
            var info = HiddenRunner.Info(exe, args); info.WorkingDirectory = workspace;
            foreach (var name in info.EnvironmentVariables.Keys.Cast<string>().Where(x => x.StartsWith("WECHAT_", StringComparison.OrdinalIgnoreCase) || x.StartsWith("XDG_", StringComparison.OrdinalIgnoreCase)).ToArray()) info.EnvironmentVariables.Remove(name);
            if (environment != null) foreach (var pair in environment) info.EnvironmentVariables[pair.Key] = pair.Value;
            var log = new StreamWriter(new FileStream(logPath, FileMode.Append, FileAccess.Write, FileShare.Read));
            var child = new Process { StartInfo = info };
            bool started = false;
            try { child.Start(); started = true; job.Assign(child); return new OwnedProcess(child, log); }
            catch { if (started && !child.HasExited) child.Kill(); child.Dispose(); log.Dispose(); throw; }
        }
        internal async Task StopWeb() { await process.StandardInput.WriteLineAsync("stop"); await process.StandardInput.FlushAsync(); await WaitStopped(); }
        internal void StopUnadmitted() { if (!process.HasExited) process.Kill(); }
        internal async Task WaitStopped() {
            var deadline = DateTime.UtcNow.AddSeconds(30);
            while (!process.HasExited) {
                if (DateTime.UtcNow > deadline) throw new InvalidOperationException("服务尚未完成安全退出；没有强制结束在途任务。");
                await Task.Delay(100);
            }
            await Task.WhenAll(output, error);
        }
        public void Dispose() { process.Dispose(); lock (gate) log.Dispose(); }
    }

    internal sealed class ProcessJob : IDisposable {
        readonly SafeFileHandle handle;
        [StructLayout(LayoutKind.Sequential)] struct BasicLimits { public long ProcessTime, JobTime; public uint Flags; public UIntPtr MinWorking, MaxWorking; public uint ActiveLimit; public UIntPtr Affinity; public uint Priority, Scheduling; }
        [StructLayout(LayoutKind.Sequential)] struct IOCounters { public ulong ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes; }
        [StructLayout(LayoutKind.Sequential)] struct Limits { public BasicLimits Basic; public IOCounters IO; public UIntPtr ProcessMemory, JobMemory, PeakProcess, PeakJob; }
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] static extern SafeFileHandle CreateJobObject(IntPtr attributes, string name);
        [DllImport("kernel32.dll", SetLastError = true)] static extern bool SetInformationJobObject(SafeFileHandle job, int type, ref Limits limits, uint size);
        [DllImport("kernel32.dll", SetLastError = true)] static extern bool AssignProcessToJobObject(SafeFileHandle job, IntPtr process);
        internal ProcessJob() {
            handle = CreateJobObject(IntPtr.Zero, null);
            var limits = new Limits(); limits.Basic.Flags = 0x2000; // KILL_ON_JOB_CLOSE
            if (handle.IsInvalid || !SetInformationJobObject(handle, 9, ref limits, (uint)Marshal.SizeOf(typeof(Limits)))) { handle.Dispose(); throw new InvalidOperationException("无法建立本实例的进程隔离，未启动服务。"); }
        }
        internal void Assign(Process process) { if (!AssignProcessToJobObject(handle, process.Handle)) throw new InvalidOperationException("无法确认子进程归属，已拒绝启动。"); }
        public void Dispose() { handle.Dispose(); }
    }
}
