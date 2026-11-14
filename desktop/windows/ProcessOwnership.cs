using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Runtime.InteropServices;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Win32.SafeHandles;

namespace WechatAgent.Desktop {
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
        internal static void ClearInheritedEnvironment(ProcessStartInfo info, bool clearStationSecrets) {
            foreach (var name in info.EnvironmentVariables.Keys.Cast<string>().Where(x =>
                x.StartsWith("WECHAT_", StringComparison.OrdinalIgnoreCase) || x.StartsWith("XDG_", StringComparison.OrdinalIgnoreCase) ||
                (clearStationSecrets && x.StartsWith("AHS_", StringComparison.OrdinalIgnoreCase))).ToArray()) info.EnvironmentVariables.Remove(name);
        }
        internal static OwnedProcess Start(string exe, string[] args, string workspace, Dictionary<string, string> environment, string logPath, ProcessJob job, bool clearStationSecrets = false) {
            var info = HiddenRunner.Info(exe, args); info.WorkingDirectory = workspace;
            ClearInheritedEnvironment(info, clearStationSecrets);
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
            // Hosted Windows runners can be compiling or draining the local
            // station while its stdin stop request is already accepted. Keep
            // waiting for a graceful exit instead of treating that normal
            // drain as a failed install or killing in-flight work.
            var deadline = DateTime.UtcNow.AddSeconds(90);
            while (!process.HasExited) {
                if (DateTime.UtcNow > deadline) throw new InvalidOperationException("服务尚未完成安全退出；没有强制结束在途任务。");
                await Task.Delay(100);
            }
            await Task.WhenAll(output, error);
        }
        public void Dispose() { process.Dispose(); lock (gate) log.Dispose(); }
    }

    internal sealed class ProcessJob : IDisposable {
        internal static int LastConstructionErrorCode { get; private set; }
        readonly SafeFileHandle handle;
        internal int LastWin32Error { get; private set; }
        [StructLayout(LayoutKind.Sequential)] struct BasicLimits { public long ProcessTime, JobTime; public uint Flags; public UIntPtr MinWorking, MaxWorking; public uint ActiveLimit; public UIntPtr Affinity; public uint Priority, Scheduling; }
        [StructLayout(LayoutKind.Sequential)] struct IOCounters { public ulong ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes; }
        [StructLayout(LayoutKind.Sequential)] struct Limits { public BasicLimits Basic; public IOCounters IO; public UIntPtr ProcessMemory, JobMemory, PeakProcess, PeakJob; }
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] static extern SafeFileHandle CreateJobObject(IntPtr attributes, string name);
        [DllImport("kernel32.dll", SetLastError = true)] static extern bool SetInformationJobObject(SafeFileHandle job, int type, ref Limits limits, uint size);
        [DllImport("kernel32.dll", SetLastError = true)] static extern bool AssignProcessToJobObject(SafeFileHandle job, IntPtr process);
        internal ProcessJob() {
            handle = CreateJobObject(IntPtr.Zero, null);
            var limits = new Limits(); limits.Basic.Flags = 0x2000; // KILL_ON_JOB_CLOSE
            if (handle.IsInvalid) {
                LastConstructionErrorCode = Marshal.GetLastWin32Error();
                handle.Dispose(); throw new InvalidOperationException("无法建立本实例的进程隔离，未启动服务。");
            }
            if (!SetInformationJobObject(handle, 9, ref limits, (uint)Marshal.SizeOf(typeof(Limits)))) {
                LastConstructionErrorCode = Marshal.GetLastWin32Error();
                handle.Dispose(); throw new InvalidOperationException("无法建立本实例的进程隔离，未启动服务。");
            }
        }
        internal void Assign(Process process) {
            if (!AssignProcessToJobObject(handle, process.Handle)) {
                LastWin32Error = Marshal.GetLastWin32Error();
                throw new InvalidOperationException("无法确认子进程归属，已拒绝启动。");
            }
        }
        public void Dispose() { handle.Dispose(); }
    }
}
