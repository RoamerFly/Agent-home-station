# Windows 桌面程序

双击 `WechatAgent.exe` 自动启动本机服务，窗口直接显示现有管理页，没有额外的启动控制台或外部浏览器窗口。右上角 **× 关闭窗口** 会等待在途任务，然后停止全部本实例服务并退出。**最小化** 则收起到托盘，服务继续运行；双击托盘图标返回管理页。托盘菜单提供打开窗口、收起到托盘、启动服务、停止服务、关于及“退出并停止全部服务”。启动服务不会更改管理页中的 Agent 自动回复开关。

支持两个后端：独立便携包默认使用 **Windows 原生**；已有 `secrets/local-deployment.json` 的 `backend=wsl` 仍按原方式管理 WSL 服务，不自动迁移或覆盖资料。Docker 部署继续使用原入口，桌面版不会擅自控制 Docker。

## Windows 原生便携包

开发机执行：

```powershell
./scripts/Build-Desktop.ps1 -OutputDirectory dist/windows-native -Test -Portable
```

首次源码构建需要 Go、Git、Rust 1.93.1、MSVC C++/Windows SDK 和完整的原生 Windows Perl（例如 Strawberry Perl）。这些是构建依赖，使用者无需安装。`Build-WindowsNative.ps1` 固定使用 `components/native-client/source-manifest.json` 的上游提交与累计补丁，核验补丁哈希；构建 `wx.exe` 和无 CGO 的 `wechat-admin.exe`，同时生成 SHA-256 清单和上游许可证。构建缓存位于 `temp/`，原生程序位于便携包 `runtimes/windows-x64/`，不会下载或打包聊天、模型及 AI 环境。程序包须整体复制。

双击便携包里的 EXE 即可运行，不要求 WSL、Docker、Python、Node 或官方微信桌面版。首次在管理页扫码并配置模型及会话；Agent 默认未启用。系统需有 WebView2 Runtime；语音/视频准备还需要目标机器安装 FFmpeg/FFprobe。首次启动使用空的独立资料，不能读取官方 Windows 微信旧聊天。当前可选的 Linux 音乐服务、ASR 环境与既有私有模型没有自动迁移。

无配置时数据根为程序目录的 `data/`，端口为管理页 `8081`、微信后台 `6174`，账号 ID 为 `desktop`。可在 `secrets/local-deployment.json` 显式配置数据根和回环端口：

```json
{"backend":"windows-native","data_root":"data","account":"desktop","admin_url":"http://127.0.0.1:8081","native_url":"http://127.0.0.1:6174"}
```

相对数据根按程序工作区解析，不依赖当前工作目录。数据、密钥和日志由当前用户与 SYSTEM 独占；链接/重解析路径、校验失败的程序、非回环地址及占用端口拒绝启动，不接管其他实例。`data/logs/` 记录启动失败。页面会话在 `secrets/`。升级保留 `data/`、`secrets/`、模型和生成物。

Windows 后台使用前台 `wx serve` 子进程，由桌面进程统一管理；使用 Windows Job Object 管理本实例及其子进程。正常退出先鉴权排空全部账号并以只读 SQLite 查询核验原生附件队列，随后通过继承的私有 stdin 管道让 Go 服务优雅退出，再使用绑定实例身份的原生停止协议停止微信；不登出账号，不增加公开停止接口，不依赖 Python。桌面进程意外终止时 Job Object 结束其所属进程树，持久任务由后续启动恢复。新安装不导入或启动已有 WSL 实例。

运行验证命令（仅用于独立空白数据根，禁止指向唯一生产副本）：

```text
DesktopTests.exe --cycle <隔离便携包目录>
```

它真实启动/停止两轮 Windows 微信和 Go 服务，但不扫码、不触发模型、不发送消息。真实登录、原有资料迁移及手机媒体播放另行验收，不能以空账号或模拟测试替代。

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

可以为此命令创建桌面快捷方式。关闭、启动与重复双击都只作用于这一个项目实例；重复双击会恢复已有窗口，包括已收起到托盘的窗口。未知后端、异常服务名或账号不匹配会拒绝操作。仅构建桌面壳时，现有 WSL 使用方式如下。

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
