using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Net;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace WechatAgent.Desktop {
    // Douyin and Bilibili use the same owned-window entry as the WeChat
    // console, while retaining their application-specific station sections.
    internal sealed class ApplicationWorkbenchDialog : Form {
        readonly Deployment deployment;
        readonly ServiceController controller;
        readonly string appID;
        readonly Action closed;
        readonly WebView2 view = new WebView2 { Dock = DockStyle.Fill, DefaultBackgroundColor = Color.White };
        DouyinAuthorizationDialog authorization;
        bool terminal;

        internal ApplicationWorkbenchDialog(Deployment deployment, ServiceController controller, string appID, Action closed) {
            if (appID != "douyin" && appID != "bilibili") throw new ArgumentException("Unknown application workbench.", "appID");
            this.deployment = deployment; this.controller = controller; this.appID = appID; this.closed = closed;
            Text = (appID == "douyin" ? "抖音" : "哔哩哔哩") + "工作台";
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

        string WorkbenchUrl { get { return deployment.Url.TrimEnd('/') + "/?workbench=" + appID; } }

        async Task StartView() {
            try {
                Program.ConfigureWebViewLoader(deployment.Workspace);
                string folder = Path.Combine(deployment.PrivateSettingsRoot, "desktop-webview");
                Directory.CreateDirectory(folder);
                var environment = await CoreWebView2Environment.CreateAsync(null, folder);
                if (IsDisposed || terminal) return;
                await view.EnsureCoreWebView2Async(environment);
                if (IsDisposed || terminal) return;
                Configure();
                await controller.Login(CancellationToken.None);
                if (IsDisposed || terminal) return;
                foreach (Cookie cookie in controller.SessionCookies()) {
                    var copy = view.CoreWebView2.CookieManager.CreateCookie(cookie.Name, cookie.Value, cookie.Domain, cookie.Path);
                    copy.IsHttpOnly = cookie.HttpOnly; copy.IsSecure = cookie.Secure;
                    view.CoreWebView2.CookieManager.AddOrUpdateCookie(copy);
                }
                view.CoreWebView2.Navigate(WorkbenchUrl);
            } catch (Exception error) {
                if (!terminal && !IsDisposed) {
                    MessageBox.Show(this, "应用工作台未能打开。\n" + error.Message, Text, MessageBoxButtons.OK, MessageBoxIcon.Warning);
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
            core.Settings.IsWebMessageEnabled = true;
            core.Settings.AreBrowserAcceleratorKeysEnabled = true;
            core.Settings.IsSwipeNavigationEnabled = false;
            view.AllowExternalDrop = false;
            core.NewWindowRequested += delegate(object sender, CoreWebView2NewWindowRequestedEventArgs e) {
                e.Handled = true;
                if (terminal) return;
                if (AllowsNavigation(e.Uri)) core.Navigate(e.Uri);
                else if (e.IsUserInitiated) OpenExternal(e.Uri);
            };
            core.NavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e) {
                if (terminal || (e.Uri != "about:blank" && !AllowsNavigation(e.Uri))) {
                    e.Cancel = true;
                    if (!terminal && e.IsUserInitiated) OpenExternal(e.Uri);
                }
            };
            core.DownloadStarting += delegate(object sender, CoreWebView2DownloadStartingEventArgs e) { e.Cancel = true; };
            core.PermissionRequested += delegate(object sender, CoreWebView2PermissionRequestedEventArgs e) { e.State = CoreWebView2PermissionState.Deny; };
            core.LaunchingExternalUriScheme += delegate(object sender, CoreWebView2LaunchingExternalUriSchemeEventArgs e) { e.Cancel = true; };
            core.ProcessFailed += delegate { Finish(); };
            core.WebMessageReceived += delegate(object sender, CoreWebView2WebMessageReceivedEventArgs e) { HandleWebMessage(e); };
        }

        static void OpenExternal(string url) {
            Uri uri;
            if (!Uri.TryCreate(url, UriKind.Absolute, out uri) || (uri.Scheme != "http" && uri.Scheme != "https")) return;
            try { Process.Start(url); } catch (Exception) { }
        }

        bool AllowsNavigation(string value) {
            Uri uri, expected;
            if (!Uri.TryCreate(value, UriKind.Absolute, out uri) || !Uri.TryCreate(WorkbenchUrl, UriKind.Absolute, out expected)) return false;
            return uri.Scheme == expected.Scheme && uri.Host.Equals(expected.Host, StringComparison.OrdinalIgnoreCase) && uri.Port == expected.Port &&
                uri.UserInfo == "" && uri.AbsolutePath == "/" && uri.Query == expected.Query && uri.Fragment == "";
        }

        void HandleWebMessage(CoreWebView2WebMessageReceivedEventArgs message) {
            if (terminal || !AllowsNavigation(message.Source)) return;
            Dictionary<string, object> input;
            try { input = Deployment.Json.Deserialize<Dictionary<string, object>>(message.WebMessageAsJson); }
            catch (Exception) { return; }
            if (input == null) return;
            string type = Deployment.Text(input, "type");
            if (type == "application-workbench-close") { Finish(); return; }
            if (type != "douyin-auth-open" || appID != "douyin") return;
            string authorizationURL = Deployment.Text(input, "url");
            DouyinAuthorizationAttempt attempt;
            if (!DouyinAuthorizationPolicy.TryCreate(authorizationURL, Deployment.Text(input, "expires_at"), DateTimeOffset.UtcNow, out attempt)) {
                PostAuthorization("douyin-auth-closed", DouyinAuthorizationPolicy.StateFromOfficialURL(authorizationURL), "", "授权信息无效或已过期，请重新生成二维码。");
                return;
            }
            CloseAuthorization();
            var dialog = new DouyinAuthorizationDialog(deployment, authorizationURL, attempt,
                callback => PostAuthorization("douyin-auth-callback", attempt.State, callback, ""),
                reason => PostAuthorization("douyin-auth-closed", attempt.State, "", reason));
            authorization = dialog;
            dialog.FormClosed += delegate { if (authorization == dialog) authorization = null; };
            dialog.Show(this);
        }

        void PostAuthorization(string type, string state, string callback, string error) {
            if (terminal || IsDisposed || view.CoreWebView2 == null || !AllowsNavigation(view.Source == null ? "" : view.Source.ToString())) return;
            try { view.CoreWebView2.PostWebMessageAsJson(Deployment.Json.Serialize(new { type = type, state = state, callback_url = callback, error = error })); }
            catch (InvalidOperationException) { }
        }

        void CloseAuthorization() {
            var dialog = authorization; authorization = null;
            if (dialog != null && !dialog.IsDisposed) dialog.CancelForServiceStop();
        }

        internal void CloseForServiceStop() { Finish(); }

        void Finish() {
            if (terminal) return;
            terminal = true;
            CloseAuthorization();
            if (closed != null) closed();
            if (!IsDisposed) Close();
        }
    }
}
