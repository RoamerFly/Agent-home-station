using System.Drawing;
using System.Windows.Forms;

namespace WechatAgent.Desktop {
    internal sealed class CloseChoiceDialog : Form {
        readonly CheckBox remember = new CheckBox();
        internal string Choice { get; private set; }
        internal bool Remember { get { return remember.Checked; } }
        internal CloseChoiceDialog() {
            Choice = "cancel";
            Text = "关闭程序"; Font = new Font("Microsoft YaHei UI", 10);
            AutoScaleMode = AutoScaleMode.Dpi; ClientSize = new Size(630, 255);
            FormBorderStyle = FormBorderStyle.FixedDialog; MaximizeBox = MinimizeBox = false;
            ShowInTaskbar = false; StartPosition = FormStartPosition.CenterParent;
            BackColor = Color.FromArgb(248, 250, 252);
            var layout = new TableLayoutPanel { Dock = DockStyle.Fill, Padding = new Padding(24), ColumnCount = 1, RowCount = 4 };
            layout.RowStyles.Add(new RowStyle(SizeType.AutoSize)); layout.RowStyles.Add(new RowStyle(SizeType.Percent, 100));
            layout.RowStyles.Add(new RowStyle(SizeType.AutoSize)); layout.RowStyles.Add(new RowStyle(SizeType.AutoSize));
            var heading = new Label { Text = "请选择关闭方式", AutoSize = true, Font = new Font(Font.FontFamily, 15, FontStyle.Bold), Margin = new Padding(0, 0, 0, 12) };
            var description = new Label { Text = "最小化到托盘将继续运行。退出程序会等待任务结束并停止本实例的全部服务。\n未勾选“记住此选项”时，下次关闭仍会询问。", AutoSize = true, MaximumSize = new Size(575, 0), Margin = new Padding(0, 0, 0, 12) };
            remember.Text = "记住此选项(&R)"; remember.AccessibleName = "记住此选项"; remember.AutoSize = true; remember.Margin = new Padding(0, 0, 0, 16); remember.TabIndex = 0;
            var buttons = new FlowLayoutPanel { Dock = DockStyle.Fill, AutoSize = true, FlowDirection = FlowDirection.RightToLeft, WrapContents = false, Margin = Padding.Empty };
            var exit = Button("退出程序", "exit", 150, 3);
            var tray = Button("最小化到托盘运行", "tray", 210, 2);
            var cancel = Button("取消", "cancel", 110, 1);
            buttons.Controls.AddRange(new Control[] {exit, tray, cancel});
            layout.Controls.Add(heading, 0, 0); layout.Controls.Add(description, 0, 1); layout.Controls.Add(remember, 0, 2); layout.Controls.Add(buttons, 0, 3);
            Controls.Add(layout); CancelButton = cancel; AcceptButton = cancel;
            Shown += delegate { cancel.Focus(); };
        }
        Button Button(string text, string choice, int width, int tab) {
            var button = new Button { Text = text, AccessibleName = text, Size = new Size(width, 44), Margin = new Padding(8, 0, 0, 0), TabIndex = tab, UseVisualStyleBackColor = true };
            button.Click += delegate { Choice = choice; DialogResult = choice == "cancel" ? DialogResult.Cancel : DialogResult.OK; Close(); };
            return button;
        }
    }
}
