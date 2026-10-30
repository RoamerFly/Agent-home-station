using System;
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
    internal sealed class MainForm : Form {
        readonly Deployment deployment;
        readonly ServiceController controller;
        readonly CloseSettings closeSettings;
        readonly EventWaitHandle activate;
        readonly NotifyIcon tray = new NotifyIcon();
        readonly ContextMenuStrip menu = new ContextMenuStrip();
        readonly ToolStripMenuItem startItem = new ToolStripMenuItem("启动全部服务"), stopItem = new ToolStripMenuItem("停止全部服务"), exitItem = new ToolStripMenuItem("退出并停止全部服务");
        readonly Label status = new Label();
        readonly Button retry = new Button(), cancel = new Button();
        readonly Panel placeholder = new Panel();
        readonly System.Windows.Forms.Timer timer = new System.Windows.Forms.Timer();
        WebView2 web;
        CancellationTokenSource operation;
        bool busy, allowExit, exitRequested, stoppingCommitted;
        FormWindowState restoredState = FormWindowState.Maximized;
        bool closeDialogOpen;

        internal MainForm(Deployment d, EventWaitHandle activation) {
            deployment = d; activate = activation; controller = new ServiceController(d);
            closeSettings = new CloseSettings(d.Workspace);
            Text = "蓝色大肥鱼"; Size = new Size(1440, 980); MinimumSize = new Size(920, 640);
            WindowState = FormWindowState.Maximized;
            StartPosition = FormStartPosition.CenterScreen; AutoScaleMode = AutoScaleMode.Dpi;
            Font = new Font("Microsoft YaHei UI", 11); BackColor = Color.White; Icon = SystemIcons.Application;
            // The management page owns the full client area. These controls are
            // visible only during startup, shutdown, or a recoverable failure.
            placeholder.Dock = DockStyle.Fill; placeholder.BackColor = Color.FromArgb(248, 250, 252);
            var center = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 3, Padding = new Padding(36) };
            center.RowStyles.Add(new RowStyle(SizeType.Percent, 50)); center.RowStyles.Add(new RowStyle(SizeType.AutoSize)); center.RowStyles.Add(new RowStyle(SizeType.Percent, 50));
            status.Text = "正在启动蓝色大肥鱼…"; status.Dock = DockStyle.Fill; status.TextAlign = ContentAlignment.BottomCenter; status.AutoSize = false;
            var buttons = new FlowLayoutPanel { AutoSize = true, Anchor = AnchorStyles.Top, WrapContents = false, Margin = new Padding(0, 24, 0, 0) };
            retry.Text = "重新启动"; retry.AccessibleName = retry.Text; retry.AutoSize = true; retry.MinimumSize = new Size(120, 44); retry.Visible = false;
            cancel.Text = "取消退出"; cancel.AccessibleName = cancel.Text; cancel.AutoSize = true; cancel.MinimumSize = new Size(120, 44); cancel.Visible = false;
            buttons.Controls.AddRange(new Control[] {retry, cancel}); center.Controls.Add(status, 0, 0); center.Controls.Add(buttons, 0, 1);
            placeholder.Controls.Add(center); Controls.Add(placeholder);
            var show = new ToolStripMenuItem("打开管理页"); show.Click += delegate { RestoreWindow(); };
            var hide = new ToolStripMenuItem("收起到托盘（保持运行）"); hide.Click += delegate { ToTray(); };
            startItem.Click += async delegate { await StartServices(); };
            stopItem.Click += async delegate { await StopServices(false); };
            exitItem.Click += async delegate { RequestExit(); if (!busy) await StopServices(true); };
            var about = new ToolStripMenuItem("关于蓝色大肥鱼");
            var closeMode = new ToolStripMenuItem("关闭窗口时");
            foreach (var value in new [] { new [] {"ask", "每次询问"}, new [] {"tray", "最小化到托盘运行"}, new [] {"exit", "退出并停止全部服务"} }) {
                string behavior = value[0]; var item = new ToolStripMenuItem(value[1]) { Checked = closeSettings.Behavior == behavior };
                item.Click += delegate {
                    try { closeSettings.Save(behavior); foreach (ToolStripMenuItem other in closeMode.DropDownItems) other.Checked = other == item; }
                    catch (Exception error) { MessageBox.Show(this, "关闭方式未能保存，原设置保留。\n" + error.Message, "蓝色大肥鱼", MessageBoxButtons.OK, MessageBoxIcon.Warning); }
                };
                closeMode.DropDownItems.Add(item);
            }
            closeMode.DropDownOpening += delegate { for (int i = 0; i < closeMode.DropDownItems.Count; i++) ((ToolStripMenuItem)closeMode.DropDownItems[i]).Checked = closeSettings.Behavior == new [] {"ask", "tray", "exit"}[i]; };
            about.Click += delegate { MessageBox.Show("蓝色大肥鱼桌面版 1.1.0\n开发者：RoamerFly\n项目：https://github.com/RoamerFly/wechat-agent\n反馈：https://github.com/RoamerFly/wechat-agent/issues\n\n最小化：收起到托盘，继续运行\n关闭窗口：选择取消、托盘或退出，可记住选择\n选择退出：安全停止全部本实例服务并退出\n\n后端：" + (d.Windows ? "Windows 原生（Linux 微信协议标识）" : "WSL") + "\n项目：" + d.Workspace + "\n服务数据：" + d.Root, "关于蓝色大肥鱼", MessageBoxButtons.OK, MessageBoxIcon.Information); };
            menu.Items.AddRange(new ToolStripItem[] {show, hide, new ToolStripSeparator(), startItem, stopItem, new ToolStripSeparator(), closeMode, about, exitItem});
            tray.Icon = Icon; tray.Text = "蓝色大肥鱼 · 正在启动"; tray.ContextMenuStrip = menu; tray.Visible = true;
            tray.DoubleClick += delegate { RestoreWindow(); };
            Resize += delegate { if (WindowState == FormWindowState.Minimized) ToTray(); else restoredState = WindowState; };
            retry.Click += async delegate { await StartServices(); };
            cancel.Click += delegate { if (!stoppingCommitted && operation != null) { exitRequested = false; operation.Cancel(); } };
            controller.Progress = text => {
                status.Text = text;
                if (text.StartsWith("正在停止本实例")) { stoppingCommitted = true; cancel.Enabled = false; }
            };
            Shown += async delegate { await StartServices(); };
            FormClosing += async delegate(object sender, FormClosingEventArgs e) {
                if (allowExit) return; e.Cancel = true;
                if (closeDialogOpen || exitRequested) return;
                string choice = closeSettings.Behavior;
                if (choice == "ask") {
                    closeDialogOpen = true;
                    try {
                        using (var dialog = new CloseChoiceDialog()) {
                            dialog.ShowDialog(this); choice = dialog.Choice;
                            if (choice == "cancel") return;
                            if (dialog.Remember) closeSettings.Save(choice);
                        }
                    } catch (Exception error) {
                        MessageBox.Show(this, "关闭方式未能保存，服务保持运行。\n" + error.Message, "蓝色大肥鱼", MessageBoxButtons.OK, MessageBoxIcon.Warning); return;
                    } finally { closeDialogOpen = false; }
                }
                if (choice == "tray") { ToTray(); return; }
                RequestExit(); if (!busy) await StopServices(true);
            };
            timer.Interval = 1000;
            timer.Tick += delegate { if (activate.WaitOne(0)) RestoreWindow(); }; timer.Start();
        }
        void RequestExit() {
            exitRequested = true;
            if (busy) {
                status.Text = "操作完成后将安全停止服务并退出…";
                tray.Text = "蓝色大肥鱼 · 正在准备退出";
            }
        }
        void ToTray() {
            Hide(); ShowInTaskbar = false;
            tray.ShowBalloonTip(1800, "蓝色大肥鱼", "已收起到托盘，服务继续运行。双击托盘图标打开管理页。", ToolTipIcon.Info);
        }
        void RestoreWindow() {
            // Restore the state before Show, otherwise showing a minimized
            // form can fire Resize and immediately hide it in the tray again.
            WindowState = restoredState; ShowInTaskbar = true; Show(); Activate();
        }
        void ClosePage() { if (web != null) { Controls.Remove(web); web.Dispose(); web = null; } placeholder.Visible = true; placeholder.BringToFront(); }
        void SetBusy(bool value, bool stopping) {
            busy = value; startItem.Enabled = stopItem.Enabled = !value;
            retry.Visible = false; cancel.Visible = value && stopping; cancel.Enabled = true;
        }
        async Task StartServices() {
            if (busy) return; SetBusy(true, false); operation = new CancellationTokenSource();
            try {
                status.Text = "正在启动蓝色大肥鱼…"; tray.Text = "蓝色大肥鱼 · 正在启动";
                await controller.Start(operation.Token);
                if (!exitRequested) await ShowPage();
                tray.Text = "蓝色大肥鱼 · 运行中";
            } catch (Exception error) { Error(error); }
            finally { operation.Dispose(); operation = null; SetBusy(false, false); if (web == null) retry.Visible = true; }
            if (exitRequested) await StopServices(true);
        }
        async Task StopServices(bool exiting) {
            if (busy) return; if (exiting) exitRequested = true;
            SetBusy(true, true); stoppingCommitted = false; operation = new CancellationTokenSource();
            bool cancelled = false;
            try {
                ClosePage(); status.Text = "正在等待在途任务结束，随后停止全部服务…"; tray.Text = "蓝色大肥鱼 · 正在安全停止";
                await controller.Stop(operation.Token);
                tray.Text = "蓝色大肥鱼 · 已停止";
                if (exitRequested) { allowExit = true; tray.Visible = false; Close(); }
            } catch (OperationCanceledException) {
                exitRequested = false; status.Text = "已取消退出，服务继续运行。"; tray.Text = "蓝色大肥鱼 · 运行中";
                cancelled = true;
            } catch (Exception error) { exitRequested = false; Error(error); }
            finally {
                operation.Dispose(); operation = null;
                if (!IsDisposed) { SetBusy(false, false); if (web == null) retry.Visible = true; }
            }
            if (cancelled) { try { await ShowPage(); } catch (Exception error) { Error(error); } }
        }
        async Task ShowPage() {
            if (web != null) return;
            var view = new WebView2 { Dock = DockStyle.Fill, DefaultBackgroundColor = Color.White }; web = view; Controls.Add(view); view.BringToFront();
            try {
                CoreWebView2Environment.SetLoaderDllFolderPath(Program.ExtractLoader(deployment.Workspace));
                var environment = await CoreWebView2Environment.CreateAsync(null, Path.Combine(deployment.Workspace, "secrets", "desktop-webview"));
                await view.EnsureCoreWebView2Async(environment);
                view.CoreWebView2.Settings.AreDevToolsEnabled = false;
                view.CoreWebView2.NewWindowRequested += delegate(object sender, CoreWebView2NewWindowRequestedEventArgs e) { e.Handled = true; if (Local(e.Uri)) view.CoreWebView2.Navigate(e.Uri); else if (e.IsUserInitiated && Http(e.Uri)) Process.Start(e.Uri); };
                view.CoreWebView2.NavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e) { if (!Local(e.Uri) && e.Uri != "about:blank") { e.Cancel = true; if (e.IsUserInitiated && Http(e.Uri)) Process.Start(e.Uri); } };
                // Use protected API session cookies; never inject credentials or a host object into JS.
                await controller.Login(CancellationToken.None);
                foreach (Cookie cookie in controller.SessionCookies()) {
                    var copy = view.CoreWebView2.CookieManager.CreateCookie(cookie.Name, cookie.Value, cookie.Domain, cookie.Path);
                    copy.IsHttpOnly = cookie.HttpOnly; copy.IsSecure = cookie.Secure;
                    view.CoreWebView2.CookieManager.AddOrUpdateCookie(copy);
                }
                view.CoreWebView2.Navigate(deployment.Url + "/"); placeholder.Visible = false;
            } catch { ClosePage(); throw; }
        }
        bool Local(string url) { Uri uri; return Uri.TryCreate(url, UriKind.Absolute, out uri) && uri.GetLeftPart(UriPartial.Authority) == deployment.Url && uri.UserInfo == ""; }
        static bool Http(string url) { Uri uri; return Uri.TryCreate(url, UriKind.Absolute, out uri) && (uri.Scheme == "http" || uri.Scheme == "https"); }
        void Error(Exception error) { ClosePage(); status.Text = "操作未完成\n\n" + error.Message + "\n\n最小化可收起到托盘；关闭窗口会再次尝试安全停止。"; retry.Visible = true; tray.Text = "蓝色大肥鱼 · 需要处理"; RestoreWindow(); }
        protected override void Dispose(bool disposing) {
            if (disposing) { timer.Dispose(); tray.Visible = false; tray.Dispose(); menu.Dispose(); if (web != null) web.Dispose(); controller.Dispose(); }
            base.Dispose(disposing);
        }
    }
}
