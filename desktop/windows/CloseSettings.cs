using System;
using System.Collections.Generic;
using System.IO;
using System.Text;

namespace WechatAgent.Desktop {
    internal sealed class CloseSettings {
        readonly string path;
        internal string Behavior { get; private set; }
        internal CloseSettings(string workspace) {
            path = Path.Combine(workspace, "secrets", "desktop-settings.json");
            Behavior = "ask";
            if (!File.Exists(path)) return;
            try {
                var data = Deployment.Json.Deserialize<Dictionary<string, object>>(File.ReadAllText(path, Encoding.UTF8));
                if (data == null) return;
                var value = Deployment.Text(data, "close_behavior");
                if (Valid(value)) Behavior = value;
            } catch (ArgumentException) { } catch (InvalidOperationException) { }
        }
        internal static bool Valid(string value) { return value == "ask" || value == "tray" || value == "exit"; }
        internal void Save(string value) {
            if (!Valid(value)) throw new ArgumentException("关闭方式无效。");
            var directory = Path.GetDirectoryName(path); WindowsRuntime.PrivateDirectory(directory);
            string temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try {
                File.WriteAllText(temporary, Deployment.Json.Serialize(new { close_behavior = value }), new UTF8Encoding(false));
                if (File.Exists(path)) File.Replace(temporary, path, path + ".bak"); else File.Move(temporary, path);
                Behavior = value;
            } finally { if (File.Exists(temporary)) File.Delete(temporary); }
        }
    }
}
