# Windows 桌面程序

双击 `WechatAgent.exe` 自动启动本机服务，窗口直接显示现有管理页，没有额外的启动控制台或外部浏览器窗口。右上角 **× 关闭窗口** 会等待在途任务，然后停止全部本实例服务并退出。**最小化** 则收起到托盘，服务继续运行；双击托盘图标返回管理页。托盘菜单提供打开窗口、收起到托盘、启动服务、停止服务、关于及“退出并停止全部服务”。启动服务不会更改管理页中的 Agent 自动回复开关。

当前复用 `secrets/local-deployment.json` 的 WSL 部署：原生微信、Web 和可选受管音乐服务。读取 `distro`、`linux_user`、`data_root`、`account`、`music_service`，服务使用已有配置、SQLite、登录资料、模型和素材；不复制大型运行时。当前管理地址沿用本机 `127.0.0.1:8081`。Docker 部署继续使用原入口，桌面版不会擅自控制 Docker。

## 构建和使用

在 Windows 运行一次构建（使用系统 .NET Framework C# 编译器）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/Build-Desktop.ps1 -Test
```

产物在 `dist/windows-desktop/WechatAgent.exe`。它是 Windows x64 GUI 可执行文件，内嵌 SDK 程序集及加载器，无需从 PowerShell、BAT 或 Python 启动。运行时直接调用隐藏的 `wsl.exe` 和现有 systemd 单元；Python 3 仅用于 WSL 内的只读在途任务检查，已有本机部署已具备此依赖。

放在项目子目录中可自动找到项目根，也支持：

```text
WechatAgent.exe --workspace "E:\项目目录"
```

可以为此命令创建桌面快捷方式。关闭、启动与重复双击都只作用于这一个项目实例；重复双击会恢复已有窗口，包括已收起到托盘的窗口。缺少配置、非 WSL 配置、异常服务名或账号不匹配会拒绝操作。

## 退出和数据边界

停止时通过现有管理员鉴权、CSRF 和全部账号排空协议暂停接受新处理，等待在途 Agent、维护、手机及后台程序；只读检查各账号原生附件队列和音乐任务。等待可取消，并解除排空。检查失败或未知时保持运行，不强制杀掉任务。

排空后依次停止 Web、受管音乐、原生微信服务，并检查其确实变为 inactive。停止 systemd 单元会断开当前在线连接，但不退出微信账号或删除登录资料。重新启动使用原连接资料，必要时要求原生服务连接已保存账号。不会调用 WSL shutdown/terminate，也不结束其他项目或整个发行版。操作已进入 systemd 停止阶段时不再接受“取消”，以实际完成结果为准。

关闭窗口等待停止成功后才退出；停止失败保留窗口及具体错误，等待期间可“取消退出”。登录、配置和模型保留。程序不会创建新微信消息、重放任务或改变 Agent 自动回复启停设置。停止后也结束仅属于本项目的旧启动器和桌面程序 WSL 保活进程，匹配完整实例标识及 argv，不结束其他 WSL 会话；旧手动计划任务定义可以保留，但已结束的保活进程不会被手动任务自动拉起。桌面程序不新增计划任务或开机启动项。

## 内嵌页面和依赖

使用 WinForms 与系统 WebView2 Runtime；本机已有 Runtime 时无需下载安装浏览器。缺少 Runtime 时会显示错误，服务仍可通过托盘菜单安全停止。SDK `Microsoft.Web.WebView2` 固定 `1.0.3537.50`，从[官方 NuGet 包](https://www.nuget.org/packages/Microsoft.Web.WebView2/1.0.3537.50)下载，并核验包 SHA-256：

```text
5ea526bbd728adda0da4d31219267e96460494a427e4894c4e09d9f320f4b9aa
```

SDK 许可为包内 Microsoft WebView2 SDK License，构建时同时输出 `WebView2-LICENSE.txt`，分发 EXE 时保留此许可。加载器从 EXE 提取到项目 `temp/desktop/runtime/<hash>` 并核验内容；页面登录状态保存在被 Git 忽略的 `secrets/desktop-webview`。不向页面脚本注入密码或提供宿主执行对象；只允许当前本机管理源在内嵌视图导航，用户点击的外站链接交给外部浏览器。

构建依赖缓存、Runtime、页面状态及生成物均不入 Git。程序和测试源码位于本目录，CI 的 Windows job 编译 GUI EXE 并执行控制器回归；Linux Source checks 继续验证服务端。没有生成 Docker 镜像或发布 Release。
