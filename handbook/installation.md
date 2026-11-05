# 安装与启动

## 已有 Windows 便携包

1. 把**整个程序目录**解压到当前用户可以写入的位置。确认根目录有 `WechatAgent.exe`，旁边有 `runtimes/`。在桌面创建快捷方式时，目标指向此处的 EXE；单独复制 EXE 会丢失微信后台等运行文件。
2. 双击 EXE。窗口应直接显示管理页。桌面外壳需要 Microsoft Edge WebView2 Runtime；使用者无须为了基础微信连接安装 WSL、CUDA 或开发工具链。
3. 首次运行会在程序目录下准备本机数据。默认账号标识为 `desktop`，管理页使用本机回环端口 8081，原生服务使用 6174。已有部署可以覆盖这些值，因此以本机配置为准。
4. 进入 [首次配置](first-chat.md) 完成微信和模型设置。窗口打开只证明管理端能运行，手机扫码和 Agent 启动分别有状态。

最小化会留在任务栏。点关闭按钮时可选择托盘运行或退出；托盘菜单可以恢复窗口、启停服务，以及修改已记住的关闭方式。要彻底停机，选择退出并停止服务，等待在途工作收尾。

## 只有源码时如何构建 Windows 包

构建机准备 Git、与 `go.mod` 匹配的 Go、Python 3.13、Rust 1.93.1、MSVC C++ 工具链、Windows SDK、原生 Perl，以及含真实 `ffmpeg.exe` 和 `ffprobe.exe` 的媒体分发目录。首次构建需要能访问所固定的上游依赖。安装程序的快捷入口或包管理器 shim 无法代替媒体二进制。

```powershell
git clone --branch main https://github.com/RoamerFly/Agent-home-station.git
cd wechat-agent
# 将路径改成你的媒体分发目录；选择一个新的输出目录。
.\scripts\Build-Desktop.ps1 -Portable -Test -MediaDirectory 'C:\tools\ffmpeg\bin' -OutputDirectory 'dist\first-portable'
.\dist\first-portable\WechatAgent.exe
```

构建脚本校验原生客户端和依赖摘要。若报文件缺失或摘要不匹配，先修复报错所指的依赖，然后在新输出目录重建。不要把测试构建直接写进正在使用的数据目录。文档 OCR、语音模型和音乐 GPU 环境需另行准备。[Windows 构建说明](https://github.com/RoamerFly/Agent-home-station/blob/89b0bfa/desktop/windows/README.md)

## Linux 服务器

Linux 部署需要可运行的原生微信服务和独立的数据目录。先按分支中的原生客户端清单准备后端，再从仓库根目录构建管理端。

```bash
mkdir -p dist
go build -o dist/wechat-admin ./cmd/wechat-admin
```

为管理端配置 `WECHAT_ADMIN_DATA_DIR`、`WECHAT_ADMIN_LISTEN`、`WECHAT_NATIVE_URL`、`WECHAT_NATIVE_TOKEN_FILE`、`WECHAT_NATIVE_ACCOUNT`；为持久任务和工作区设置 `WECHAT_BOT_STATE_DIR`、`WECHAT_WORKSPACE_ROOT`。令牌文件必须与该原生服务匹配。先在回环地址验证连接、扫码和重启后的账号状态，再配置远程访问。仓库内的 Docker 配置保留了历史部署路线，使用前需单独核对对应版本。[原生依赖清单](https://github.com/RoamerFly/Agent-home-station/blob/89b0bfa/components/native-client/README.md) · [部署说明](https://github.com/RoamerFly/Agent-home-station/blob/89b0bfa/deploy/README.md)

下一步：[扫码并完成第一次对话](first-chat.md)
