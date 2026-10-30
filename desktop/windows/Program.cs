using System;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Linq;
using System.Net;
using System.Reflection;
using System.Runtime.CompilerServices;
using System.Security.Cryptography;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

[assembly: AssemblyTitle("蓝色大肥鱼桌面版")]
[assembly: AssemblyVersion("1.1.0.0")]
namespace WechatAgent.Desktop {
    internal static class Program {
        [STAThread]
        static void Main(string[] args) {
            Application.EnableVisualStyles(); Application.SetCompatibleTextRenderingDefault(false);
            try {
                AppDomain.CurrentDomain.AssemblyResolve += delegate(object sender, ResolveEventArgs e) {
                    string name = new AssemblyName(e.Name).Name;
                    if (name != "Microsoft.Web.WebView2.Core" && name != "Microsoft.Web.WebView2.WinForms") return null;
                    using (var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream(name + ".dll")) {
                        var bytes = new byte[stream.Length]; stream.Read(bytes, 0, bytes.Length); return Assembly.Load(bytes);
                    }
                };
                var d = Deployment.Load(Deployment.FindWorkspace(args));
                bool owner;
                using (var mutex = new Mutex(true, "Local\\WechatAgentDesktop-" + d.Identity, out owner))
                using (var activate = new EventWaitHandle(false, EventResetMode.AutoReset, "Local\\WechatAgentDesktopActivate-" + d.Identity)) {
                    if (!owner) { activate.Set(); return; }
                    Run(d, activate); mutex.ReleaseMutex();
                }
            } catch (Exception error) { MessageBox.Show(error.Message, "大肥鱼 Agent · 无法打开", MessageBoxButtons.OK, MessageBoxIcon.Error); }
        }
        [MethodImpl(MethodImplOptions.NoInlining)]
        static void Run(Deployment d, EventWaitHandle activate) { Application.Run(new MainForm(d, activate)); }
        internal static string ExtractLoader(string workspace) {
            byte[] bytes;
            using (var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream("WebView2Loader.dll")) {
                bytes = new byte[stream.Length]; stream.Read(bytes, 0, bytes.Length);
            }
            string hash; using (var sha = SHA256.Create()) hash = BitConverter.ToString(sha.ComputeHash(bytes)).Replace("-", "");
            string folder = Path.Combine(workspace, "temp", "desktop", "runtime", hash);
            Directory.CreateDirectory(folder); string path = Path.Combine(folder, "WebView2Loader.dll");
            if (!File.Exists(path) || !File.ReadAllBytes(path).SequenceEqual(bytes)) File.WriteAllBytes(path, bytes);
            return folder;
        }
    }

}
