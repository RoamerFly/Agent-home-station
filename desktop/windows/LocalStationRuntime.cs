using System;
using System.Collections.Generic;
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
        ProcessJob job;
        WechatStationRuntime wechat;
        bool stationAdmitted;
        internal LocalStationRuntime(Deployment deployment) { d = deployment; }
        bool UsesPortableData { get { return d.UsesPortableStationData(AppDomain.CurrentDomain.BaseDirectory); } }
        internal bool Running { get { return server != null && server.Alive; } }
        internal int PID { get { return server == null ? 0 : server.ID; } }
        internal int NativePID { get { return wechat == null ? 0 : wechat.PID; } }
        internal bool NativeAvailable { get { return wechat != null && wechat.Available; } }

        void Stage(string name, int errorCode = 0) {
            try {
                var path = Path.Combine(d.Root, "logs", "startup-stage.log");
                var suffix = errorCode == 0 ? "" : " code=0x" + errorCode.ToString("X8");
                File.AppendAllText(path, DateTime.UtcNow.ToString("o") + "\t" + name + suffix + Environment.NewLine, new UTF8Encoding(false));
            } catch { }
        }

        internal void CheckAlive() {
            if (!Running) throw new InvalidOperationException("本机成长站提前退出，请查看 data/station/logs/station.log；没有改动微信或旧数据。");
        }
        internal async Task Start(CancellationToken cancel) {
            cancel.ThrowIfCancellationRequested();
            if (Running) { CheckAlive(); return; }
            var startNative = d.LocalStationNative;
            if (server != null) { server.Dispose(); server = null; }
            if (job != null) { job.Dispose(); job = null; }
            await StopWechat(false, true);
            if (!File.Exists(d.StationExe)) throw new InvalidOperationException("本机成长站程序不存在，请使用 LocalStation 预览包重新构建。");
            WindowsRuntime.NoLinks(d.StationExe); WindowsRuntime.NoLinks(d.Root);
            // Overlapping private data and unsafe desktop ownership are host
            // errors. They must still stop the launch instead of degrading.
            if (startNative) WechatStationRuntime.ValidateLayout(d);
            WindowsRuntime.PrivateDirectory(d.Root, UsesPortableData); Directory.CreateDirectory(Path.Combine(d.Root, "logs")); Directory.CreateDirectory(d.StationWorkspace); WindowsRuntime.NoLinks(d.StationWorkspace);
            Stage("logs-ready");
            try { Stage("free-port.begin"); WindowsRuntime.FreePort(d.Url); Stage("free-port.ok"); }
            catch { Stage("free-port.failed"); throw; }
            try { Stage("process-job.begin"); job = new ProcessJob(); Stage("process-job.ok"); }
            catch { Stage("process-job.failed", ProcessJob.LastConstructionErrorCode); throw; }
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
                if (wechat != null) wechat.AddServerArguments(args);
                var programEnv = new Dictionary<string, string>();
                var media = Path.Combine(Path.GetDirectoryName(d.StationExe), "runtimes", "windows-media");
                var ffmpeg = Path.Combine(media, "ffmpeg.exe");
                var ffprobe = Path.Combine(media, "ffprobe.exe");
                if (File.Exists(ffmpeg) && File.Exists(ffprobe)
                    && ((File.GetAttributes(ffmpeg) | File.GetAttributes(ffprobe)) & FileAttributes.ReparsePoint) == 0) {
                    WindowsRuntime.NoLinks(media);
                    programEnv["PATH"] = media + Path.PathSeparator + Environment.GetEnvironmentVariable("PATH");
                }
                Stage("server.start.begin");
                server = OwnedProcess.Start(d.StationExe, args.ToArray(), d.Workspace, programEnv, Path.Combine(d.Root, "logs", "station.log"), job);
                Stage("server.start.ok");
                stationAdmitted = true;
                Stage("server.admitted");
            } catch (Exception error) {
                startFailure = error;
                Stage("server.start.failed", job == null ? 0 : job.LastWin32Error);
            }
            if (startFailure != null) {
                Stage("startup.cleanup.begin");
                if (server != null) { server.Dispose(); server = null; }
                Exception cleanupFailure = null;
                if (job != null) { job.Dispose(); job = null; }
                try { await StopWechat(false, true); } catch (Exception error) { cleanupFailure = error; }
                Stage(cleanupFailure == null ? "startup.cleanup.ok" : "startup.cleanup.failed");
                if (cleanupFailure != null) throw new InvalidOperationException("本机成长站启动失败，且已拥有的微信原生进程未能正常关闭。", new AggregateException(startFailure, cleanupFailure));
                throw startFailure;
            }
        }
        internal async Task Stop() {
            var ownedServer = server; server = null; Exception failure = null;
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
            if (job != null) job.Dispose();
            if (wechat != null) wechat.Dispose();
        }
    }
}
