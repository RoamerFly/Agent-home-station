using System;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Linq;
using System.Net;
using System.Reflection;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
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
        static readonly object loaderGate = new object();
        static bool loaderConfigured;
        [DllImport("shell32.dll", CharSet=CharSet.Unicode)]
        static extern int SetCurrentProcessExplicitAppUserModelID(string identity);
        [STAThread]
        static void Main(string[] args) {
            if (args.Length == 1 && (args[0] == "--start-services" || args[0] == "--stop-services" || args[0] == "--exit-services")) {
                Environment.ExitCode = DesktopInstance.RequestCommand(args[0].Substring(2)) ? 0 : 1;
                return;
            }
            Application.EnableVisualStyles(); Application.SetCompatibleTextRenderingDefault(false);
            try {
                using (var instance = new DesktopInstance()) {
                    // Admit before reading another copy's configuration or
                    // constructing any form/WebView/service process.
                    if (!instance.IsOwner) { instance.ActivateOwner(); return; }
                    SetCurrentProcessExplicitAppUserModelID("RoamerFly.WechatAgent.Desktop");
                    RegisterDependencies();
                    var d = Deployment.Load(Deployment.FindWorkspace(args));
                    Run(d, instance);
                }
            } catch (Exception error) { MessageBox.Show(error.Message, "大肥鱼 Agent · 无法打开", MessageBoxButtons.OK, MessageBoxIcon.Error); }
        }
        internal static void RegisterDependencies() {
                AppDomain.CurrentDomain.AssemblyResolve += delegate(object sender, ResolveEventArgs e) {
                    string name = new AssemblyName(e.Name).Name;
                    if (name != "Microsoft.Web.WebView2.Core" && name != "Microsoft.Web.WebView2.WinForms") return null;
                    using (var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream(name + ".dll")) {
                        var bytes = new byte[stream.Length]; stream.Read(bytes, 0, bytes.Length); return Assembly.Load(bytes);
                    }
                };
        }
        [MethodImpl(MethodImplOptions.NoInlining)]
        static void Run(Deployment d, DesktopInstance instance) { Application.Run(new MainForm(d, instance)); }
        internal static void ConfigureWebViewLoader(string workspace) {
            lock (loaderGate) {
                if (loaderConfigured) return;
                // WebView2 permits this setter only before its first API call.
                // Restarting services/pages must reuse the loaded DLL path.
                CoreWebView2Environment.SetLoaderDllFolderPath(ExtractLoader(workspace));
                loaderConfigured = true;
            }
        }
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
