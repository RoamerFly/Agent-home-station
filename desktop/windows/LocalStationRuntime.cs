using System;
using System.Collections.Generic;
using System.IO;
using System.Net.Http;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace WechatAgent.Desktop {
    // A local station owns only its own child processes.  A wx daemon is an
    // explicit owner opt-in: it is never discovered, adopted, or enrolled.
    internal sealed class LocalStationRuntime : IDisposable {
        readonly Deployment d;
        readonly HttpClient nativeApi = new HttpClient(new HttpClientHandler { UseProxy = false, AllowAutoRedirect = false }) { Timeout = TimeSpan.FromSeconds(3) };
        OwnedProcess server, native;
        ProcessJob job;
        string nativeServerId, nativeGeneration, nativeToken;
        internal LocalStationRuntime(Deployment deployment) { d = deployment; }
        internal bool Running { get { return server != null && server.Alive; } }
        internal int PID { get { return server == null ? 0 : server.ID; } }
        internal int NativePID { get { return native == null ? 0 : native.ID; } }

        static bool Contains(string parent, string child) {
            parent = WindowsRuntime.ComparablePath(parent).TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar;
            child = WindowsRuntime.ComparablePath(child).TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar;
            return String.Equals(parent, child, StringComparison.OrdinalIgnoreCase) || child.StartsWith(parent, StringComparison.OrdinalIgnoreCase);
        }
        static void NoLinkedFile(string path) {
            if (File.Exists(path) && (File.GetAttributes(path) & FileAttributes.ReparsePoint) != 0) throw new InvalidOperationException("微信原生配置和令牌文件不能使用链接。");
        }
        void ValidateNativeLayout() {
            var nativeRoot = d.StationNativeRoot;
            var appRoot = Path.GetDirectoryName(nativeRoot);
            if (Contains(d.StationWorkspace, appRoot) || Contains(appRoot, d.StationWorkspace))
                throw new InvalidOperationException("本机成长站工作区不能与微信应用私有目录重叠。请在配置中设置独立 workspace_root。");
            // Old desktop native deployments have this exact root layout; a
            // station must never reinterpret it as its own private state.
            if (File.Exists(Path.Combine(d.Root, "api-token")) && Directory.Exists(Path.Combine(d.Root, "admin")))
                throw new InvalidOperationException("data_root 看起来是旧版微信原生数据目录，拒绝接管。请为本机成长站选择独立 data_root。");
            WindowsRuntime.NoLinks(nativeRoot);
            NoLinkedFile(Path.Combine(nativeRoot, "native-server.toml"));
            NoLinkedFile(Path.Combine(nativeRoot, "api-token"));
        }
        void AdmitNativeDirectories() {
            var nativeRoot = d.StationNativeRoot;
            WindowsRuntime.PrivateDirectory(Path.Combine(d.Root, "apps"));
            WindowsRuntime.PrivateDirectory(Path.Combine(d.Root, "apps", "wechat"));
            WindowsRuntime.PrivateDirectory(nativeRoot);
        }
        static void WriteNativeConfig(string nativeRoot) {
            var path = Path.Combine(nativeRoot, "native-server.toml");
            NoLinkedFile(path);
            if (!File.Exists(path)) using (var file = new FileStream(path, FileMode.CreateNew, FileAccess.Write, FileShare.Read)) {
                var bytes = new UTF8Encoding(false).GetBytes("schemaVersion = 1\n"); file.Write(bytes, 0, bytes.Length);
            }
            var text = File.ReadAllText(path, Encoding.UTF8);
            if (text.IndexOf("schemaVersion = 1", StringComparison.Ordinal) < 0)
                throw new InvalidOperationException("本机成长站微信配置版本不受支持；仅接受 schemaVersion = 1 的私有配置。");
        }
        internal void CheckAlive() {
            if (!Running) throw new InvalidOperationException("本机成长站提前退出，请查看 data/station/logs/station.log；没有改动微信或旧数据。");
            if (d.LocalStationNative && (native == null || !native.Alive)) throw new InvalidOperationException("本机成长站微信原生服务提前退出，请查看 data/station/logs/wechat-native.log；没有创建账号或修改旧数据。");
        }
        HttpRequestMessage NativeMessage(string path, object body) {
            var request = new HttpRequestMessage(body == null ? HttpMethod.Get : HttpMethod.Post, d.StationNativeUrl + path);
            if (String.IsNullOrEmpty(nativeToken)) {
                var token = Path.Combine(d.StationNativeRoot, "api-token"); NoLinkedFile(token);
                nativeToken = File.ReadAllText(token, Encoding.UTF8).Trim();
                if (nativeToken == "") throw new InvalidOperationException("微信原生服务令牌为空，拒绝连接。");
            }
            request.Headers.Authorization = new System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", nativeToken);
            if (body != null) request.Content = new StringContent(Deployment.Json.Serialize(body), Encoding.UTF8, "application/json");
            return request;
        }
        async Task<Dictionary<string, object>> NativeRequest(string path, object body) {
            using (var request = NativeMessage(path, body)) using (var response = await nativeApi.SendAsync(request)) {
                response.EnsureSuccessStatusCode();
                return Deployment.Json.Deserialize<Dictionary<string, object>>(await response.Content.ReadAsStringAsync());
            }
        }
        async Task WaitForNative(CancellationToken cancel) {
            var deadline = DateTime.UtcNow.AddSeconds(90);
            while (true) {
                cancel.ThrowIfCancellationRequested();
                if (native == null || !native.Alive) throw new InvalidOperationException("本机成长站微信原生服务启动失败，请查看 data/station/logs/wechat-native.log。没有创建或登录微信账号。");
                try {
                    var value = await NativeRequest("/v1/status", null);
                    if (Convert.ToInt32(value["pid"]) != native.ID || !String.Equals(WindowsRuntime.ComparablePath(Deployment.Text(value, "dataDir")), WindowsRuntime.ComparablePath(d.StationNativeRoot), StringComparison.OrdinalIgnoreCase))
                        throw new InvalidOperationException("微信原生服务实例身份或数据目录不匹配，拒绝接管。");
                    nativeServerId = Deployment.Text(value, "serverId"); nativeGeneration = Deployment.Text(value, "ownershipGeneration");
                    if (nativeServerId == "" || nativeGeneration == "") throw new InvalidOperationException("微信原生服务缺少归属信息，拒绝接管。");
                    return;
                } catch (HttpRequestException) { } catch (TaskCanceledException) { } catch (FileNotFoundException) { }
                // The owned daemon can still be writing its newly created token.
                catch (IOException error) {
                    if (error.HResult != unchecked((int)0x80070020) && error.HResult != unchecked((int)0x80070021)) throw;
                }
                if (DateTime.UtcNow > deadline) throw new InvalidOperationException("本机成长站微信原生服务未就绪，请检查 data/station/logs/wechat-native.log。");
                await Task.Delay(300, cancel);
            }
        }
        async Task StopNative(bool startupFailure) {
            var owned = native; native = null;
            if (owned == null) return;
            Exception failure = null;
            try {
                if (owned.Alive && !String.IsNullOrEmpty(nativeServerId) && !String.IsNullOrEmpty(nativeGeneration))
                    await NativeRequest("/v1/daemon/shutdown", new { pid = owned.ID, serverId = nativeServerId, ownershipGeneration = nativeGeneration });
                else if (owned.Alive && startupFailure) owned.StopUnadmitted();
                else if (owned.Alive) throw new InvalidOperationException("微信原生服务缺少启动时确认的归属信息，拒绝结束它。");
                await owned.WaitStopped();
            } catch (Exception error) {
                failure = new InvalidOperationException("微信原生服务未能正常停止；仅此桌面实例拥有的进程将由进程隔离回收。请查看 data/station/logs/wechat-native.log。", error);
            } finally { owned.Dispose(); nativeServerId = nativeGeneration = nativeToken = null; }
            if (failure != null) throw failure;
        }
        internal async Task Start(CancellationToken cancel) {
            cancel.ThrowIfCancellationRequested();
            if (Running) { CheckAlive(); return; }
            if (server != null) { server.Dispose(); server = null; }
            if (job != null) { job.Dispose(); job = null; }
            if (!File.Exists(d.StationExe)) throw new InvalidOperationException("本机成长站程序不存在，请使用 LocalStation 预览包重新构建。");
            WindowsRuntime.NoLinks(d.StationExe); WindowsRuntime.NoLinks(d.Root);
            if (d.LocalStationNative) {
                if (!File.Exists(d.StationNativeExe)) throw new InvalidOperationException("本机成长站微信原生运行文件不存在，请使用包含 wx.exe 的 Windows 程序包。");
                WindowsRuntime.VerifyNativeBundle(d.StationNativeExe, true); ValidateNativeLayout();
            }
            // All potentially destructive validation has completed above.
            WindowsRuntime.PrivateDirectory(d.Root); Directory.CreateDirectory(Path.Combine(d.Root, "logs")); Directory.CreateDirectory(d.StationWorkspace); WindowsRuntime.NoLinks(d.StationWorkspace);
            WindowsRuntime.FreePort(d.Url);
            if (d.LocalStationNative) { AdmitNativeDirectories(); WriteNativeConfig(d.StationNativeRoot); WindowsRuntime.FreePort(d.StationNativeUrl); }
            job = new ProcessJob(); nativeServerId = nativeGeneration = nativeToken = null;
            Exception startFailure = null;
            try {
                if (d.LocalStationNative) {
                    // Empty explicit environment: OwnedProcess clears WECHAT_/
                    // XDG_ values, and this child inherits no station secrets.
                    native = OwnedProcess.Start(d.StationNativeExe, new [] { "--json", "--server-config", Path.Combine(d.StationNativeRoot, "native-server.toml"), "--data-dir", d.StationNativeRoot, "--listen", d.StationNativeUrl.Substring("http://".Length), "serve" }, Path.GetDirectoryName(d.StationNativeExe), new Dictionary<string, string>(), Path.Combine(d.Root, "logs", "wechat-native.log"), job, true);
                    await WaitForNative(cancel);
                }
                var args = new List<string> { "--desktop", "--data", d.Root, "--workspace", d.StationWorkspace, "--listen", new Uri(d.Url).Authority };
                if (d.LocalStationNative) { args.Add("--wechat-native-url"); args.Add(d.StationNativeUrl); args.Add("--wechat-native-pid"); args.Add(native.ID.ToString()); }
                var programEnv = new Dictionary<string, string>();
                var media = Path.Combine(Path.GetDirectoryName(d.StationExe), "runtimes", "windows-media");
                var ffmpeg = Path.Combine(media, "ffmpeg.exe");
                var ffprobe = Path.Combine(media, "ffprobe.exe");
                if (File.Exists(ffmpeg) && File.Exists(ffprobe)
                    && ((File.GetAttributes(ffmpeg) | File.GetAttributes(ffprobe)) & FileAttributes.ReparsePoint) == 0) {
                    WindowsRuntime.NoLinks(media);
                    programEnv["PATH"] = media + Path.PathSeparator + Environment.GetEnvironmentVariable("PATH");
                }
                server = OwnedProcess.Start(d.StationExe, args.ToArray(), d.Workspace, programEnv, Path.Combine(d.Root, "logs", "station.log"), job);
            } catch (Exception error) {
                startFailure = error;
            }
            if (startFailure != null) {
                if (server != null) { server.Dispose(); server = null; }
                Exception cleanupFailure = null;
                try { await StopNative(true); } catch (Exception error) { cleanupFailure = error; }
                if (job != null) { job.Dispose(); job = null; }
                if (cleanupFailure != null) throw new InvalidOperationException("本机成长站启动失败，且已拥有的微信原生进程未能正常关闭。", new AggregateException(startFailure, cleanupFailure));
                throw startFailure;
            }
        }
        internal async Task Stop() {
            var ownedServer = server; server = null; Exception failure = null;
            try {
                // The station stops before its daemon, preventing new provider
                // work while the original authenticated daemon is shut down.
                if (ownedServer != null) {
                    if (ownedServer.Alive) await ownedServer.StopWeb(); else await ownedServer.WaitStopped();
                    if (ownedServer.ExitCode != 0) throw new InvalidOperationException("本机成长站以退出码 0x" + ownedServer.ExitCode.ToString("X8") + " 结束。");
                }
            } catch (Exception error) { failure = new InvalidOperationException("本机成长站未能正常完成关闭；微信原生服务仍将按本实例的归属信息停止。", error); }
            finally { if (ownedServer != null) ownedServer.Dispose(); }
            try { await StopNative(false); } catch (Exception error) { if (failure == null) failure = error; }
            if (job != null) { job.Dispose(); job = null; }
            if (failure != null) throw failure;
        }
        public void Dispose() { nativeApi.Dispose(); if (server != null) server.Dispose(); if (native != null) native.Dispose(); if (job != null) job.Dispose(); }
    }
}
