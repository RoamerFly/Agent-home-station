using System;
using System.Drawing;
using System.IO;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace WechatAgent.Desktop {
    // The station owns the account lifecycle, while the old Whale Console
    // remains the complete WeChat control surface. It is shown as an owned
    // modal-style window so the legacy pages keep their original layout,
    // navigation, execution rounds and tool-call details without a second
    // reimplementation in the station shell.
    internal sealed class LegacyWechatConsoleDialog : Form {
        readonly Deployment deployment;
        readonly Action closed;
        readonly WebView2 view = new WebView2 { Dock = DockStyle.Fill, DefaultBackgroundColor = Color.White };
        bool terminal;

        internal LegacyWechatConsoleDialog(Deployment deployment, Action closed) {
            this.deployment = deployment; this.closed = closed;
            Text = "微信控制台";
            StartPosition = FormStartPosition.CenterParent;
            AutoScaleMode = AutoScaleMode.Dpi;
            Size = new Size(1440, 960);
            MinimumSize = new Size(1024, 700);
            FormBorderStyle = FormBorderStyle.Sizable;
            MaximizeBox = true; MinimizeBox = false; ShowInTaskbar = false;
            BackColor = Color.White;
            Controls.Add(view);
            FormClosed += delegate { if (!terminal) Finish(); };
            Shown += async delegate { await StartView(); };
        }

        async Task StartView() {
            try {
                Program.ConfigureWebViewLoader(deployment.Workspace);
                string folder = Path.Combine(deployment.PrivateSettingsRoot, "legacy-console-webview");
                Directory.CreateDirectory(folder);
                var environment = await CoreWebView2Environment.CreateAsync(null, folder);
                if (IsDisposed || terminal) return;
                var options = environment.CreateCoreWebView2ControllerOptions();
                options.IsInPrivateModeEnabled = false;
                await view.EnsureCoreWebView2Async(environment, options);
                if (IsDisposed || terminal) return;
                Configure();
                view.CoreWebView2.Navigate(deployment.LegacyUrl + "/?from_station=1");
            } catch (Exception error) {
                if (!terminal && !IsDisposed) {
                    MessageBox.Show(this, "旧版微信控制台未能打开。\n" + error.Message, "微信控制台", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                    Finish();
                }
            }
        }

        void Configure() {
            var core = view.CoreWebView2;
            core.Settings.AreDevToolsEnabled = false;
            core.Settings.AreDefaultContextMenusEnabled = false;
            core.Settings.AreDefaultScriptDialogsEnabled = false;
            core.Settings.AreHostObjectsAllowed = false;
            core.Settings.IsStatusBarEnabled = false;
            core.Settings.IsWebMessageEnabled = false;
            core.Settings.AreBrowserAcceleratorKeysEnabled = true;
            core.Settings.IsSwipeNavigationEnabled = false;
            view.AllowExternalDrop = false;
            core.NewWindowRequested += delegate(object sender, CoreWebView2NewWindowRequestedEventArgs e) {
                e.Handled = true;
                if (AllowsNavigation(e.Uri)) core.Navigate(e.Uri);
            };
            core.DownloadStarting += delegate(object sender, CoreWebView2DownloadStartingEventArgs e) { e.Cancel = true; };
            core.PermissionRequested += delegate(object sender, CoreWebView2PermissionRequestedEventArgs e) { e.State = CoreWebView2PermissionState.Deny; };
            core.LaunchingExternalUriScheme += delegate(object sender, CoreWebView2LaunchingExternalUriSchemeEventArgs e) { e.Cancel = true; };
            core.ProcessFailed += delegate { Finish(); };
            core.NavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e) {
                if (!terminal && !AllowsNavigation(e.Uri)) e.Cancel = true;
            };
        }

        bool AllowsNavigation(string value) {
            Uri uri, expected;
            if (!Uri.TryCreate(value, UriKind.Absolute, out uri) || !Uri.TryCreate(deployment.LegacyUrl, UriKind.Absolute, out expected)) return false;
            return uri.Scheme == expected.Scheme && uri.Host.Equals(expected.Host, StringComparison.OrdinalIgnoreCase) && uri.Port == expected.Port && uri.UserInfo == "" && uri.Fragment == "";
        }

        internal void CloseForServiceStop() { Finish(); }
        void Finish() {
            if (terminal) return;
            terminal = true;
            if (closed != null) closed();
            if (IsHandleCreated && !IsDisposed) BeginInvoke(new Action(delegate { if (!IsDisposed) Close(); }));
        }
    }
}
