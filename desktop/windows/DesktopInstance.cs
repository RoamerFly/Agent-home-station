using System;
using System.Threading;

namespace WechatAgent.Desktop {
    // One desktop application per Windows session, including other copies and
    // --workspace paths. Deployment identity still scopes service ownership.
    internal sealed class DesktopInstance : IDisposable {
        internal const string Name = "Local\\RoamerFly.WechatAgent.Desktop.v2";
        readonly Mutex mutex;
        internal EventWaitHandle Activation { get; private set; }
        internal bool IsOwner { get; private set; }
        internal DesktopInstance() : this(Name) { }
        internal DesktopInstance(string name) {
            Activation = new EventWaitHandle(false, EventResetMode.AutoReset, name + ".Activate");
            mutex = new Mutex(false, name);
            try { IsOwner = mutex.WaitOne(0); }
            catch (AbandonedMutexException) { IsOwner = true; }
        }
        internal void ActivateOwner() { if (!IsOwner) Activation.Set(); }
        public void Dispose() {
            if (IsOwner) { mutex.ReleaseMutex(); IsOwner = false; }
            mutex.Dispose(); Activation.Dispose();
        }
    }
}
