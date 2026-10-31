using System;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Reflection;
using System.Threading;
using System.Threading.Tasks;
using System.Text;
using System.Windows.Forms;
using Microsoft.Web.WebView2.WinForms;

namespace WechatAgent.Desktop {
    internal static class WindowTests {
        sealed class SyntheticPageServer : IDisposable {
            readonly TcpListener listener = new TcpListener(IPAddress.Loopback, 0);
            internal string Url { get; private set; }
            internal int Requests;
            internal SyntheticPageServer() {
                listener.Start(); Url = "http://127.0.0.1:" + ((IPEndPoint)listener.LocalEndpoint).Port;
                Serve();
            }
            async void Serve() {
                try {
                    while (true) Reply(await listener.AcceptTcpClientAsync());
                } catch (ObjectDisposedException) { } catch (SocketException) { }
            }
            async void Reply(TcpClient client) {
                try {
                    using (client) using (var stream = client.GetStream()) {
                        var request = new byte[4096];
                        var read = stream.ReadAsync(request, 0, request.Length);
                        if (await Task.WhenAny(read, Task.Delay(2000)) != read || await read == 0) return;
                        ++Requests;
                        byte[] body = Encoding.UTF8.GetBytes("<!doctype html><title>Synthetic desktop lifecycle</title><p>Safe synthetic management page</p>");
                        byte[] header = Encoding.ASCII.GetBytes("HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: " + body.Length + "\r\nConnection: close\r\n\r\n");
                        await stream.WriteAsync(header, 0, header.Length); await stream.WriteAsync(body, 0, body.Length);
                    }
                } catch (ObjectDisposedException) { } catch (SocketException) { } catch (IOException) { }
            }
            public void Dispose() { listener.Stop(); }
        }
        static int assertions;
        static void LoadingSurfaceTests(string root) {
            foreach (var size in new[] { new Size(320,240),new Size(640,480),new Size(1000,720) }) {
                using(var surface=new LifecycleSurface { Size=size,Font=new Font("Microsoft YaHei UI",11) })
                using(var bitmap=new Bitmap(size.Width,size.Height)) {
                    surface.State("正在启动", "正在准备本机服务…");surface.CreateControl();
                    surface.DrawToBitmap(bitmap,new Rectangle(Point.Empty,size));
                    int blue=0;bool clipped=false;
                    for(int y=0;y<size.Height;y++)for(int x=0;x<size.Width;x++) {
                        var c=bitmap.GetPixel(x,y);
                        if(c.B>150&&c.R<70&&c.G>70&&c.G<150){blue++;if(x<3||y<3||x>=size.Width-3||y>=size.Height-3)clipped=true;}
                    }
                    Assert(blue>100&&!clipped,"loading ring must be fully inside its surface");
                    bitmap.Save(Path.Combine(root,"loading-"+size.Width+".png"));
                }
            }
        }
        static void Assert(bool value, string reason) { if (!value) throw new Exception(reason); assertions++; }
        static async Task Wait(Func<bool> condition, string reason) {
            for (int i = 0; i < 150; ++i) { if (condition()) return; await Task.Delay(100); }
            throw new Exception(reason);
        }
        static Process Child(params string[] args) {
            var info = HiddenRunner.Info(Application.ExecutablePath, args);
            return Process.Start(info);
        }
        static async Task InstanceTests(string root) {
            string name = "Local\\WechatAgentSynthetic-" + Guid.NewGuid().ToString("N");
            using (var owner = new DesktopInstance(name)) {
                Assert(owner.IsOwner, "first process owns instance");
                var children = Enumerable.Range(0, 12).Select(i => Child("--instance-probe", name)).ToArray();
                foreach (var child in children) using (child) {
                    string result = await child.StandardOutput.ReadToEndAsync();
                    await Wait(() => child.HasExited, "second process did not exit");
                    Assert(child.ExitCode == 0 && result.Trim() == "secondary", "simultaneous duplicate acquired ownership");
                }
                Assert(owner.Activation.WaitOne(0), "duplicate signals the existing window");
            }
            string ready = Path.Combine(root, "abandoned-owner-ready");
            using (var abandoned = Child("--instance-hold", name, ready)) {
                await Wait(() => File.Exists(ready), "owner probe did not acquire");
                abandoned.Kill(); abandoned.WaitForExit();
            }
            using (var recovered = new DesktopInstance(name)) Assert(recovered.IsOwner, "abandoned instance is recoverable");
        }
        static async Task FormTests(string root) {
            using (var page = new SyntheticPageServer()) {
            for (int cycle = 0; cycle < 8; ++cycle) {
                string workspace = Path.Combine(root, "window-" + cycle); Directory.CreateDirectory(workspace);
                new CloseSettings(workspace).Save(cycle == 0 ? "tray" : "exit");
                var d = new Deployment { Workspace = workspace, Distro = "Synthetic", User = "synthetic", Root = "/home/synthetic/data", Account = "synthetic", Music = "", Url = page.Url };
                var commands = new FakeCommands(); var api = new FakeWeb();
                using (var activation = new EventWaitHandle(false, EventResetMode.AutoReset))
                using (var services = new ServiceController(d, commands, api, (span, token) => Task.FromResult(0)))
                using (var form = new MainForm(d, activation, services, false)) {
                    Assert(form.WindowState == FormWindowState.Maximized, "initial window is maximized");
                    form.WindowState = FormWindowState.Normal; form.StartPosition = FormStartPosition.Manual;
                    form.Location = new Point(-20000, -20000); form.Show();
                    await Task.Delay(100);
                    form.WindowState = FormWindowState.Normal;
                    await Task.Delay(100);
                    IntPtr handle = form.Handle;
                    form.WindowState = FormWindowState.Minimized;
                    Assert(form.Visible && form.ShowInTaskbar, "normal minimize retains taskbar window");
                    form.RestoreWindow();
                    Assert(form.Visible && form.WindowState == FormWindowState.Normal, "minimize restore preserves prior state: " + form.WindowState + ", visible=" + form.Visible);
                    form.ToTray(); Assert(!form.Visible, "explicit tray hides window");
                    Assert(form.Handle == handle, "tray must not recreate HWND");
                    form.RestoreWindow(); Assert(form.Visible && form.Handle == handle, "restore reuses original HWND");
                    if (cycle == 0) {
                        form.Close(); Assert(!form.Visible && !form.IsDisposed && !commands.Stopped, "remembered tray close preserves service");
                        form.RestoreWindow();
                        var exit = (ToolStripMenuItem)typeof(MainForm).GetField("exitItem", BindingFlags.Instance | BindingFlags.NonPublic).GetValue(form);
                        exit.PerformClick();
                    } else {
                        Task starting = form.StartServices();
                        if (cycle % 2 == 0) {
                            await starting; // alternate loaded page and initialization race
                            try {
                                await Wait(() => form.Controls.OfType<WebView2>().Any(view => view.CoreWebView2 != null && view.Visible && view.CoreWebView2.DocumentTitle == "Synthetic desktop lifecycle"), "synthetic management page did not render");
                            } catch {
                                var status = (Label)typeof(MainForm).GetField("status", BindingFlags.NonPublic | BindingFlags.Instance).GetValue(form);
                                Console.Error.WriteLine("Page diagnostic: " + status.Text + "; views=" + form.Controls.OfType<WebView2>().Count());
                                foreach (var view in form.Controls.OfType<WebView2>()) Console.Error.WriteLine("cycle=" + cycle + "; source=" + view.Source + "; visible=" + view.Visible + "; title=" + (view.CoreWebView2 == null ? "uninitialized" : view.CoreWebView2.DocumentTitle) + "; requests=" + page.Requests);
                                throw;
                            }
                            Assert(true, "real WebView rendered isolated synthetic page");
                        }
                        form.Close();
                        activation.Set(); form.RestoreWindow(); // duplicate-launch race during exit
                        await starting;
                    }
                    await Wait(() => form.IsDisposed, "exit left an empty management window");
                    form.RestoreWindow(); form.ToTray();
                    typeof(MainForm).GetMethod("Error", BindingFlags.Instance | BindingFlags.NonPublic).Invoke(form, new object[] {new Exception("late synthetic page callback")});
                    await Task.Delay(150);
                    Assert(!Application.OpenForms.Cast<Form>().Any(f => f is MainForm), "late callback resurrected management window");
                    Assert(commands.Stopped, "exit stops owned services");
                }
            }
            }
        }
        [STAThread]
        static int Main(string[] args) {
            if (args.Length >= 2 && args[0].StartsWith("--instance-", StringComparison.Ordinal)) {
                using (var instance = new DesktopInstance(args[1])) {
                    if (args[0] == "--instance-hold") { File.WriteAllText(args[2], instance.IsOwner ? "owner" : "secondary"); Thread.Sleep(Timeout.Infinite); }
                    instance.ActivateOwner(); Console.WriteLine(instance.IsOwner ? "owner" : "secondary"); return 0;
                }
            }
            Console.OutputEncoding = new System.Text.UTF8Encoding(false);
            Program.RegisterDependencies();
            Application.EnableVisualStyles(); Application.SetCompatibleTextRenderingDefault(false);
            Exception failure = null;
            Application.ThreadException += delegate(object sender, ThreadExceptionEventArgs e) { failure = e.Exception; };
            string root = Path.Combine(Path.GetDirectoryName(Application.ExecutablePath), "window-tests-" + Guid.NewGuid().ToString("N")); Directory.CreateDirectory(root);
            using (var host = new Form { ShowInTaskbar = false, Opacity = 0, Location = new Point(-20000, -20000), StartPosition = FormStartPosition.Manual }) {
                host.Shown += async delegate {
                    try { LoadingSurfaceTests(root); await InstanceTests(root); await FormTests(root); }
                    catch (Exception error) { failure = error; }
                    finally { host.Close(); }
                };
                Application.Run(host);
            }
            if (failure != null) { Console.Error.WriteLine(failure); return 1; }
            Console.WriteLine("Desktop window lifecycle assertions passed: " + assertions + "; 8 real WinForms/WebView exit cycles, 12 simultaneous duplicate probes; synthetic services, no WeChat sends.");
            return 0;
        }
    }
}
