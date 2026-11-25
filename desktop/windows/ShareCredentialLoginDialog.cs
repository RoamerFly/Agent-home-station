using System;
using System.Drawing;
using System.Text;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace WechatAgent.Desktop {
    // The provider cookie is collected by the host process only.  This dialog
    // deliberately has no host object or web message bridge, and it never
    // writes a browser profile to the user's Edge profile.
    internal sealed class ShareCredentialLoginDialog : Form {
        readonly Deployment deployment;
        readonly string provider, origin, host;
        readonly Action<string, string> completed;
        readonly Action<string> closed;
        readonly WebView2 view = new WebView2 { Dock = DockStyle.Fill, DefaultBackgroundColor = Color.White };
        readonly Label detail = new Label { Dock = DockStyle.Top, Height = 50, Padding = new Padding(16, 12, 12, 6), AutoSize = false };
        readonly Button save = new Button { Text = "保存当前登录", Dock = DockStyle.Bottom, Height = 46, FlatStyle = FlatStyle.Flat };
        readonly Button cancel = new Button { Text = "取消", Dock = DockStyle.Bottom, Height = 42, FlatStyle = FlatStyle.Flat };
        readonly Timer cookiePoll = new Timer { Interval = 1200 };
        bool terminal;
        bool polling;

        internal ShareCredentialLoginDialog(Deployment deployment, string provider, Action<string, string> completed, Action<string> closed) {
            this.deployment = deployment; this.provider = provider ?? ""; this.completed = completed; this.closed = closed;
            if (String.Equals(this.provider, "assistant", StringComparison.OrdinalIgnoreCase)) {
                origin = "https://channels.weixin.qq.com/platform/post/list"; host = "channels.weixin.qq.com";
                Text = "视频号助手登录"; detail.Text = "请在视频号助手完成登录，确认页面已显示账号后保存。";
            } else {
                origin = "https://yuanbao.tencent.com/"; host = "yuanbao.tencent.com";
                Text = "元宝登录"; detail.Text = "请在元宝完成登录，确认页面已显示账号后保存。";
            }
            StartPosition = FormStartPosition.CenterParent; AutoScaleMode = AutoScaleMode.Dpi; Size = new Size(920, 720); MinimumSize = new Size(640, 520);
            FormBorderStyle = FormBorderStyle.Sizable; MaximizeBox = false; MinimizeBox = false; ShowInTaskbar = false;
            BackColor = Color.FromArgb(247, 249, 255);
            save.FlatAppearance.BorderColor = Color.FromArgb(87, 123, 206); save.Enabled = false; save.Visible = false;
            cancel.FlatAppearance.BorderColor = Color.FromArgb(206, 214, 234);
            save.Click += async delegate { await SaveCookies(); };
            cancel.Click += delegate { Finish(null, "已取消登录。", false); };
            Controls.Add(view); Controls.Add(detail); Controls.Add(save); Controls.Add(cancel); CancelButton = cancel;
            FormClosed += delegate { if (!terminal) Finish(null, "已取消登录。", false); };
            cookiePoll.Tick += async delegate { await TryAutoCapture(); };
            Shown += async delegate { await StartView(); };
        }

        async Task StartView() {
            try {
                Program.ConfigureWebViewLoader(deployment.Workspace);
                var folder = System.IO.Path.Combine(deployment.PrivateSettingsRoot, "share-login-webview", provider);
                System.IO.Directory.CreateDirectory(folder);
                var environment = await CoreWebView2Environment.CreateAsync(null, folder);
                if (IsDisposed || terminal) return;
                var options = environment.CreateCoreWebView2ControllerOptions();
                options.IsInPrivateModeEnabled = true;
                await view.EnsureCoreWebView2Async(environment, options);
                if (IsDisposed || terminal) return;
                Configure(); view.CoreWebView2.Navigate(origin);
            } catch (Exception) {
                Finish(null, "登录窗口未能启动，请检查 WebView2 后重试。", false);
            }
        }

        void Configure() {
            var core = view.CoreWebView2;
            core.Settings.AreDevToolsEnabled = false; core.Settings.AreDefaultContextMenusEnabled = false; core.Settings.AreDefaultScriptDialogsEnabled = false;
            core.Settings.AreHostObjectsAllowed = false; core.Settings.IsStatusBarEnabled = false; core.Settings.IsWebMessageEnabled = false;
            core.Settings.AreBrowserAcceleratorKeysEnabled = false; core.Settings.IsSwipeNavigationEnabled = false; core.IsMuted = true;
            view.AllowExternalDrop = false;
            core.NewWindowRequested += delegate(object sender, CoreWebView2NewWindowRequestedEventArgs e) {
                e.Handled = true;
                if (AllowsNavigation(e.Uri)) core.Navigate(e.Uri);
            };
            core.DownloadStarting += delegate(object sender, CoreWebView2DownloadStartingEventArgs e) { e.Cancel = true; };
            core.PermissionRequested += delegate(object sender, CoreWebView2PermissionRequestedEventArgs e) { e.State = CoreWebView2PermissionState.Deny; };
            core.LaunchingExternalUriScheme += delegate(object sender, CoreWebView2LaunchingExternalUriSchemeEventArgs e) { e.Cancel = true; };
            core.ProcessFailed += delegate { Finish(null, "登录窗口中断，请重试。", false); };
            core.NavigationCompleted += delegate(object sender, CoreWebView2NavigationCompletedEventArgs e) {
                if (!terminal && e.IsSuccess) { save.Enabled = true; cookiePoll.Start(); detail.Text = "完成网页登录后会自动读取 Cookie 并关闭此窗口。"; }
                else if (!terminal && e.WebErrorStatus != CoreWebView2WebErrorStatus.OperationCanceled) detail.Text = "登录页面加载失败，请检查网络后重试。";
            };
            core.NavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e) {
                if (terminal || !AllowsNavigation(e.Uri)) { e.Cancel = true; if (!terminal) detail.Text = "已阻止离开受限登录页面。"; }
            };
        }

        bool AllowsNavigation(string value) {
            Uri uri;
            return Uri.TryCreate(value, UriKind.Absolute, out uri) && uri.Scheme == "https" && uri.Port == 443 && uri.UserInfo == "" && uri.Fragment == "" && uri.Host.Equals(host, StringComparison.OrdinalIgnoreCase);
        }

        async Task SaveCookies() {
            if (terminal || view.CoreWebView2 == null) return;
            save.Enabled = false; cancel.Enabled = false; detail.Text = "正在读取当前登录状态…";
            try {
                var cookies = await view.CoreWebView2.CookieManager.GetCookiesAsync(origin);
                var output = new StringBuilder();
                foreach (var cookie in cookies) {
                    if (cookie == null || String.IsNullOrWhiteSpace(cookie.Name) || cookie.Name.IndexOfAny(new [] {';', '\r', '\n', '\0'}) >= 0 || cookie.Value == null || cookie.Value.IndexOfAny(new [] {'\r', '\n', '\0'}) >= 0) continue;
                    if (output.Length > 0) output.Append("; ");
                    output.Append(cookie.Name).Append('=').Append(cookie.Value);
                    if (output.Length > 16384) throw new InvalidOperationException("登录凭据过长，请清理当前站点后重试。");
                }
                if (output.Length < 16) throw new InvalidOperationException("当前页面没有可用登录状态，请先完成登录。");
                Finish(output.ToString(), null, true);
            } catch (Exception error) {
                if (!terminal) { detail.Text = error.Message; save.Enabled = true; cancel.Enabled = true; }
            }
        }

        async Task TryAutoCapture() {
            if (terminal || polling || view.CoreWebView2 == null) return;
            polling = true;
            try {
                var cookies = await view.CoreWebView2.CookieManager.GetCookiesAsync(origin);
                var names = new System.Collections.Generic.HashSet<string>(StringComparer.OrdinalIgnoreCase);
                foreach (var cookie in cookies) if (cookie != null && !String.IsNullOrWhiteSpace(cookie.Name)) names.Add(cookie.Name);
                bool authenticated = String.Equals(provider, "assistant", StringComparison.OrdinalIgnoreCase)
                    ? names.Contains("sessionid") || names.Contains("finder_auth") || names.Contains("wxuin") || names.Contains("pass_ticket")
                    : names.Contains("hy_token") || names.Contains("hy_user") || names.Contains("auth_token") || names.Contains("access_token");
                if (authenticated) { detail.Text = "已检测到登录状态，正在自动保存并关闭窗口…"; await SaveCookies(); }
            } catch (Exception) { }
            finally { polling = false; }
        }

        internal void CancelForServiceStop() { Finish(null, "服务已停止，登录窗口已关闭。", false); }
        void Finish(string cookie, string reason, bool success) {
            if (terminal) return;
            terminal = true; save.Enabled = cancel.Enabled = false;
            cookiePoll.Stop();
            if (success && cookie != null) completed(provider, cookie); else closed(reason ?? "已取消登录。");
            if (IsHandleCreated && !IsDisposed) BeginInvoke(new Action(delegate { if (!IsDisposed) Close(); }));
        }
        protected override void Dispose(bool disposing) { base.Dispose(disposing); }
    }
}
