using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Threading;
using System.Threading.Tasks;

namespace WechatAgent.Desktop {
    // Owns only the application-independent local station process. It never
    // probes, drains, stops, or adopts a WeChat/native deployment.
    internal sealed class LocalStationRuntime : IDisposable {
        readonly Deployment d;
        OwnedProcess server;
        ProcessJob job;
        internal LocalStationRuntime(Deployment deployment) { d = deployment; }
        internal bool Running { get { return server != null && server.Alive; } }
        internal int PID { get { return server == null ? 0 : server.ID; } }

        static void FreePort(string url) {
            var uri = new Uri(url);
            var listener = new TcpListener(IPAddress.Loopback, uri.Port);
            try { listener.Server.ExclusiveAddressUse = true; listener.Start(); }
            catch (SocketException) { throw new InvalidOperationException("本机成长站端口 " + uri.Port + " 已被占用；拒绝接管或结束其他服务。"); }
            finally { listener.Stop(); }
        }
        internal void CheckAlive() {
            if (!Running) throw new InvalidOperationException("本机成长站提前退出，请查看 data/station/logs/station.log；没有改动微信或旧数据。");
        }
        internal Task Start(CancellationToken cancel) {
            cancel.ThrowIfCancellationRequested();
            if (Running) return Task.FromResult(0);
            if (server != null) { server.Dispose(); server = null; }
            if (job != null) { job.Dispose(); job = null; }
            if (!File.Exists(d.StationExe)) throw new InvalidOperationException("本机成长站程序不存在，请使用 LocalStation 预览包重新构建。");
            WindowsRuntime.NoLinks(d.StationExe); WindowsRuntime.NoLinks(d.Root);
            WindowsRuntime.PrivateDirectory(d.Root);
            Directory.CreateDirectory(Path.Combine(d.Root, "logs"));
            Directory.CreateDirectory(d.StationWorkspace);
            // An explicit workspace root is permitted, but it must still be a
            // real directory rather than a link out to an unrelated profile.
            WindowsRuntime.NoLinks(d.StationWorkspace);
            FreePort(d.Url);
            job = new ProcessJob();
            try {
                server = OwnedProcess.Start(d.StationExe, new [] { "--desktop", "--data", d.Root, "--workspace", d.StationWorkspace, "--listen", new Uri(d.Url).Authority }, d.Workspace, new Dictionary<string, string>(), Path.Combine(d.Root, "logs", "station.log"), job);
                return Task.FromResult(0);
            } catch { job.Dispose(); job = null; throw; }
        }
        internal async Task Stop() {
            var owned = server;
            Exception failure = null;
            try {
                if (owned != null) {
                    if (owned.Alive) await owned.StopWeb(); else await owned.WaitStopped();
                    if (owned.ExitCode != 0) throw new InvalidOperationException("本机成长站以退出码 0x" + owned.ExitCode.ToString("X8") + " 结束。");
                }
            } catch (Exception error) {
                failure = new InvalidOperationException("本机成长站未能正常完成关闭；已回收本实例进程，记忆和原始记录未被删除。请查看 data/station/logs/station.log。", error);
            } finally {
                server = null;
                if (owned != null) owned.Dispose();
                if (job != null) { job.Dispose(); job = null; }
            }
            if (failure != null) throw failure;
        }
        public void Dispose() { if (server != null) server.Dispose(); if (job != null) job.Dispose(); }
    }
}
