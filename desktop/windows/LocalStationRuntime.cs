using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace WechatAgent.Desktop {
    // Owns the local station server. Optional WeChat uses its own process job
    // and can be unavailable while the server continues to operate.
    internal sealed class LocalStationRuntime : IDisposable {
        readonly Deployment d;
        OwnedProcess server;
        OwnedProcess legacy;
        ProcessJob job;
        WechatStationRuntime wechat;
        bool stationAdmitted;
        string startupInstanceId;
        string startupStage;
        DateTime startupStartedAt;
        internal LocalStationRuntime(Deployment deployment) { d = deployment; }
        bool UsesPortableData { get { return d.UsesPortableStationData(AppDomain.CurrentDomain.BaseDirectory); } }
        internal bool Running { get { return server != null && server.Alive; } }
        internal int PID { get { return server == null ? 0 : server.ID; } }
        internal int NativePID { get { return wechat == null ? 0 : wechat.PID; } }
        internal bool NativeAvailable { get { return wechat != null && wechat.Available; } }

        void Stage(string name, int errorCode = 0) {
            startupStage = name;
            try {
            var path = Path.Combine(d.Root, "main_setting", "logs", "startup-stage.log");
                var suffix = errorCode == 0 ? "" : " code=0x" + errorCode.ToString("X8");
                File.AppendAllText(path, DateTime.UtcNow.ToString("o") + "\t" + name + suffix + Environment.NewLine, new UTF8Encoding(false));
            } catch { }
        }

        static string StartupErrorCode(Exception error) {
            var message = error == null ? "" : error.Message ?? "";
            if (message.IndexOf("校验", StringComparison.OrdinalIgnoreCase) >= 0 || message.IndexOf("清单", StringComparison.OrdinalIgnoreCase) >= 0) return "native_manifest_mismatch";
            if (message.IndexOf("端口", StringComparison.OrdinalIgnoreCase) >= 0) return "port_in_use";
            if (message.IndexOf("未就绪", StringComparison.OrdinalIgnoreCase) >= 0 || message.IndexOf("提前退出", StringComparison.OrdinalIgnoreCase) >= 0) return "service_not_ready";
            if (message.IndexOf("不存在", StringComparison.OrdinalIgnoreCase) >= 0 || message.IndexOf("缺少", StringComparison.OrdinalIgnoreCase) >= 0) return "runtime_missing";
            return "startup_failed";
        }

        void WriteStartupDiagnostic(string status, Exception error = null, int errorCode = 0) {
            try {
                var logRoot = Path.Combine(d.Root, "main_setting", "logs");
                Directory.CreateDirectory(logRoot);
                var record = new Dictionary<string, object> {
                    { "schema_version", 1 },
                    { "instance_id", startupInstanceId ?? "" },
                    { "started_at", startupStartedAt == default(DateTime) ? "" : startupStartedAt.ToString("o") },
                    { "updated_at", DateTime.UtcNow.ToString("o") },
                    { "stage", startupStage ?? "unknown" },
                    { "status", status },
                    { "error_code", error == null ? "" : StartupErrorCode(error) },
                    { "error_message", error == null ? "" : error.Message },
                    { "win32_error", errorCode },
                    { "log_files", new Dictionary<string, string> {
                        { "startup", Path.Combine(logRoot, "startup-stage.log") },
                        { "native", Path.Combine(logRoot, "wechat-native.log") },
                        { "station", Path.Combine(logRoot, "station.log") },
                        { "legacy_admin", Path.Combine(logRoot, "legacy-wechat.log") }
                    } }
                };
                var path = Path.Combine(logRoot, "startup-diagnostic.json");
                var temp = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
                File.WriteAllText(temp, Deployment.Json.Serialize(record), new UTF8Encoding(false));
                if (File.Exists(path)) File.Replace(temp, path, null); else File.Move(temp, path);
            } catch { }
        }

        // A desktop crash can leave the same package's station child behind
        // after its job handle was lost. The desktop itself is singleton, so
        // an exact executable match in this package is safe to reclaim before
        // starting the new owned child. Foreign executables and processes in
        // another package are left untouched and still fail the port check.
        void ReclaimStalePackageServer() {
            var expected = WindowsRuntime.ComparablePath(d.StationExe);
            var legacy = WindowsRuntime.ComparablePath(Path.Combine(d.Workspace, "agent-home-station-server.exe"));
            foreach (var process in Process.GetProcessesByName("agent-home-station-server")) {
                try {
                    if (process.Id == Process.GetCurrentProcess().Id || process.HasExited) continue;
                    string path;
                    try { path = WindowsRuntime.ComparablePath(process.MainModule.FileName); }
                    catch { continue; }
                    if (!String.Equals(path, expected, StringComparison.OrdinalIgnoreCase) &&
                        !String.Equals(path, legacy, StringComparison.OrdinalIgnoreCase)) continue;
                    Stage("stale-server.reclaim.begin");
                    process.Kill();
                    if (!process.WaitForExit(5000)) throw new InvalidOperationException("同包旧服务未能退出。");
                    Stage("stale-server.reclaim.ok");
                } catch (InvalidOperationException) { throw; }
                catch (Exception error) {
                    Stage("stale-server.reclaim.failed");
                    throw new InvalidOperationException("同一工作站的残留服务未能安全退出，请关闭后重试。", error);
                } finally { process.Dispose(); }
            }
        }

        internal void CheckAlive() {
            if (!Running) throw new InvalidOperationException("本机成长站提前退出，请查看 data/main_setting/logs/station.log；没有改动微信或旧数据。");
        }
        internal async Task Start(CancellationToken cancel) {
            startupInstanceId = Guid.NewGuid().ToString("N");
            startupStartedAt = DateTime.UtcNow;
            startupStage = "start.begin";
            cancel.ThrowIfCancellationRequested();
            if (Running) { CheckAlive(); return; }
            var startNative = d.LocalStationNative;
            if (server != null) { server.Dispose(); server = null; }
            if (job != null) { job.Dispose(); job = null; }
            await StopWechat(false, true);
            if (legacy != null) { legacy.Dispose(); legacy = null; }
            if (!File.Exists(d.StationExe)) throw new InvalidOperationException("本机成长站程序不存在，请使用 LocalStation 预览包重新构建。");
            WindowsRuntime.NoLinks(d.StationExe); WindowsRuntime.NoLinks(d.Root);
            // Overlapping private data and unsafe desktop ownership are host
            // errors. They must still stop the launch instead of degrading.
            if (startNative) WechatStationRuntime.ValidateLayout(d);
                WindowsRuntime.PrivateDirectory(d.Root, UsesPortableData); Directory.CreateDirectory(Path.Combine(d.Root, "main_setting", "logs")); Directory.CreateDirectory(d.StationWorkspace); WindowsRuntime.NoLinks(d.StationWorkspace);
            Stage("logs-ready");
            try {
                Stage("free-port.begin");
                ReclaimStalePackageServer();
                WindowsRuntime.FreePort(d.Url);
                Stage("free-port.ok");
            }
            catch (Exception error) { Stage("free-port.failed"); WriteStartupDiagnostic("failed", error); throw; }
            try { Stage("process-job.begin"); job = new ProcessJob(); Stage("process-job.ok"); }
            catch (Exception error) { Stage("process-job.failed", ProcessJob.LastConstructionErrorCode); WriteStartupDiagnostic("failed", error, ProcessJob.LastConstructionErrorCode); throw; }
            stationAdmitted = false;
            Exception startFailure = null;
            try {
                if (startNative) {
                    Stage("wechat.start.begin");
                    wechat = new WechatStationRuntime(d);
                    await wechat.Start(cancel);
                    Stage("wechat.start.ok");
                }
                cancel.ThrowIfCancellationRequested();
                var args = new List<string> { "--desktop", "--data", d.Root, "--workspace", d.StationWorkspace, "--listen", new Uri(d.Url).Authority };
                if (!String.IsNullOrEmpty(d.ToolPackagesDirectory)) args.AddRange(new [] { "--tool-packages-directory", d.ToolPackagesDirectory });
                if (!String.IsNullOrEmpty(d.ApplicationPackagesDirectory)) args.AddRange(new [] { "--application-packages-directory", d.ApplicationPackagesDirectory });
                if (wechat != null) wechat.AddServerArguments(args);
                var programEnv = new Dictionary<string, string>();
                var media = Path.Combine(Deployment.PackageRuntimeRoot(d.Workspace), "windows-media");
                var ffmpeg = Path.Combine(media, "ffmpeg.exe");
                var ffprobe = Path.Combine(media, "ffprobe.exe");
                if (File.Exists(ffmpeg) && File.Exists(ffprobe)
                    && ((File.GetAttributes(ffmpeg) | File.GetAttributes(ffprobe)) & FileAttributes.ReparsePoint) == 0) {
                    WindowsRuntime.NoLinks(media);
                    programEnv["PATH"] = media + Path.PathSeparator + Environment.GetEnvironmentVariable("PATH");
                }
                if (startNative && wechat != null && File.Exists(d.AdminExe)) {
                    var legacyDir = Path.Combine(d.Root, "applications", "wechat", "legacy-admin");
                    var workerRoot = Path.Combine(d.Root, "applications", "wechat", "workers");
                    var workerDirs = Directory.Exists(workerRoot) ? Directory.GetDirectories(workerRoot) : new string[0];
                    var workerDir = workerDirs.Length == 1 ? workerDirs[0] : Path.Combine(workerRoot, "legacy");
                    var legacyEnv = new Dictionary<string, string>(programEnv);
                    legacyEnv["WECHAT_ADMIN_DATA_DIR"] = legacyDir;
                    var stationPasswordPath = Path.Combine(d.Root, "main_setting", "authority", "admin-password");
                    if (File.Exists(stationPasswordPath)) legacyEnv["WECHAT_ADMIN_PASSWORD"] = File.ReadAllText(stationPasswordPath).Trim();
                    legacyEnv["WECHAT_BOT_STATE_DIR"] = Path.Combine(legacyDir, "bot-state");
                    legacyEnv["WECHAT_ADMIN_LISTEN"] = new Uri(d.LegacyUrl).Authority;
                    legacyEnv["WECHAT_ADMIN_ORIGIN"] = d.LegacyUrl;
                    legacyEnv["WECHAT_LEGACY_AUTOLOGIN"] = "1";
                    legacyEnv["WECHAT_NATIVE_URL"] = d.StationNativeUrl;
                    legacyEnv["WECHAT_NATIVE_TOKEN_FILE"] = Path.Combine(d.Root, "applications", "wechat", "native", "api-token");
                    legacyEnv["WECHAT_NATIVE_ACCOUNT"] = "desktop";
                    legacyEnv["WECHAT_STATION_DATA_DIR"] = d.Root;
                    legacyEnv["WECHAT_STATION_WORKER_DIR"] = workerDir;
                    legacyEnv["WECHAT_STATION_MODEL_FILE"] = Path.Combine(d.Root, "main_setting", "authority", "local-model.json");
                    legacyEnv["WECHAT_STARTUP_LOG_DIR"] = Path.Combine(d.Root, "main_setting", "logs");
                    var packageRoot = d.Workspace;
                    var packageRuntime = Deployment.PackageRuntimeRoot(packageRoot);
                    var pythonRoot = Path.Combine(packageRuntime, "windows-python-cli");
                    var pluginHelper = Path.Combine(packageRuntime, "windows-agent-host", "windows_plugin_host.pyw");
                    legacyEnv["WECHAT_PLUGIN_RUNTIME_CONFIG"] = Deployment.Json.Serialize(new {
                        python = Path.Combine(pythonRoot, "python.exe"),
                        pythonw = Path.Combine(pythonRoot, "pythonw.exe"),
                        windows_helper = pluginHelper,
                        runtime_roots = new [] { pythonRoot },
                        document_runtime_root = Path.Combine(packageRuntime, "document-workshop"),
                        catalog_directory = String.IsNullOrEmpty(d.ToolPackagesDirectory) ? Path.Combine(packageRoot, "plugins") : d.ToolPackagesDirectory
                    });
                    Stage("legacy-admin.start.begin");
                    legacy = OwnedProcess.Start(d.AdminExe, new [] { "--desktop" }, d.Workspace, legacyEnv, Path.Combine(d.Root, "main_setting", "logs", "legacy-wechat.log"), job);
                    Stage("legacy-admin.start.ok");
                    programEnv["AHS_LEGACY_WECHAT"] = "1";
                }
                Stage("server.start.begin");
                server = OwnedProcess.Start(d.StationExe, args.ToArray(), d.Workspace, programEnv, Path.Combine(d.Root, "main_setting", "logs", "station.log"), job);
                Stage("server.start.ok");
                stationAdmitted = true;
                Stage("server.admitted");
            } catch (Exception error) {
                startFailure = error;
                Stage("server.start.failed", job == null ? 0 : job.LastWin32Error);
            }
            if (startFailure != null) {
                var failureCode = job == null ? 0 : job.LastWin32Error;
                Stage("startup.cleanup.begin");
                if (server != null) { server.Dispose(); server = null; }
                if (legacy != null) { legacy.Dispose(); legacy = null; }
                Exception cleanupFailure = null;
                if (job != null) { job.Dispose(); job = null; }
                try { await StopWechat(false, true); } catch (Exception error) { cleanupFailure = error; }
                Stage(cleanupFailure == null ? "startup.cleanup.ok" : "startup.cleanup.failed");
                WriteStartupDiagnostic("failed", startFailure, failureCode);
                if (cleanupFailure != null) throw new InvalidOperationException("本机成长站启动失败，且已拥有的微信原生进程未能正常关闭。", new AggregateException(startFailure, cleanupFailure));
                throw startFailure;
            }
            WriteStartupDiagnostic("running");
        }
        internal async Task Stop() {
            var ownedServer = server; server = null; var ownedLegacy = legacy; legacy = null; Exception failure = null;
            var gracefulServer = false;
            try {
                // The station stops before its daemon, preventing new provider
                // work while the original authenticated daemon is shut down.
                if (ownedServer != null) {
                    var serverWasAlive = ownedServer.Alive;
                    if (serverWasAlive) await ownedServer.StopWeb(); else await ownedServer.WaitStopped();
                    if (ownedServer.ExitCode != 0) throw new InvalidOperationException("本机成长站以退出码 0x" + ownedServer.ExitCode.ToString("X8") + " 结束。");
                    gracefulServer = stationAdmitted && serverWasAlive;
                }
            } catch (Exception error) { failure = new InvalidOperationException("本机成长站未能正常完成关闭；微信原生服务仍将按本实例的归属信息停止。", error); }
            finally { if (ownedServer != null) ownedServer.Dispose(); }
            if (ownedLegacy != null) {
                try {
                    if (ownedLegacy.Alive) await ownedLegacy.StopWeb(); else await ownedLegacy.WaitStopped();
                    if (ownedLegacy.ExitCode != 0 && failure == null) failure = new InvalidOperationException("旧版微信控制台未能正常关闭。");
                } catch (Exception error) { if (failure == null) failure = error; }
                finally { ownedLegacy.Dispose(); }
            }
            if (job != null) { job.Dispose(); job = null; }
            try { await StopWechat(gracefulServer && failure == null, false); }
            catch (Exception error) { if (failure == null) failure = error; }
            finally { stationAdmitted = false; }
            if (failure != null) throw failure;
        }

        async Task StopWechat(bool hostDrained, bool startupFailure) {
            var app = wechat; wechat = null;
            if (app == null) return;
            try { await app.Stop(hostDrained, startupFailure); }
            finally { app.Dispose(); }
        }

        public void Dispose() {
            if (server != null) server.Dispose();
            if (legacy != null) legacy.Dispose();
            if (job != null) job.Dispose();
            if (wechat != null) wechat.Dispose();
        }
    }
}
