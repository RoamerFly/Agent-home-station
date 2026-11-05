# Windows 独立家目录预览 · 5.B.1

这是应用化重构的第一个可运行小步：不启动微信原生端、账号 Manager、设备桥或模型服务，准备本机主人身份及大肥鱼的持久家目录。**此入口还不能聊天或执行 Agent 工具**，现有微信管理程序保持原来的工作方式。

## 启动

在仓库根目录使用 Go 构建 Windows 本地服务：

```powershell
go build -o temp/local-home/agent-home-station.exe ./cmd/agent-home-station
& ./temp/local-home/agent-home-station.exe
```

浏览器打开 `http://127.0.0.1:8082`。首次使用空的测试安装时，初始管理密码为 `admin`；也可在启动前设置 `AHS_ADMIN_PASSWORD`。已有 `admin-password` 优先于环境变量，启动日志不输出口令。此程序目前是开发预览服务，尚未替换桌面启动器或创建新的桌面快捷方式。

默认设置目录为 **可执行文件所在目录** 下的 `data/station`，不受启动时当前目录影响。默认工作空间为该设置目录下的 `managed-workspaces`。显式路径必须是绝对路径，监听仅允许本机回环地址。

```powershell
& ./temp/local-home/agent-home-station.exe `
  -data 'E:\AgentHomePreview\host' `
  -workspace 'E:\AgentHomePreview\workspace' `
  -listen '127.0.0.1:8082'
```

上例用于独立预演，创建的是测试安装的新身份，不代表已经迁移原来的大肥鱼。旧管理服务升级到这版代码后，`/local-home` 使用它原本的身份注册表和管理登录。不要同时启动旧版和新版程序写同一份真实身份文件；本轮没有执行生产数据迁移。

## 保存内容

点击“为大肥鱼准备家”后，宿主在私有设置目录中的 `fish-identity.json` 保存稳定鱼身份、本机主人及家目录授权版本。家目录为：

```text
<workspace>/fish_home/<完整 fish_id>/home/
  diary/
  projects/
  works/
  scratch/
  Project Tools/
  Connected Apps/
```

创建空的 `Connected Apps` 不表示微信已经迁入，也不会生成虚构的应用账号。原有账号私密目录保留，暂不自动搬动日记、消息、模型或凭据。本机授权不随微信解绑而消失；页面的暂停与恢复只修改这份新授权记录，**尚未控制旧微信 Agent 的文件和执行权限**。相关执行挂载将在 5.B.2 接入。

读取状态不会创建鱼身份，显式初始化才建立本机主人。重复初始化保留原身份和暂停状态；旧页面提交过期版本会收到冲突，不能反向覆盖新授权。身份损坏时停止更新，保留原文件。目录准备失败时保留身份和已有文件，可在修复目录后再次初始化续办。

## 数据兼容与恢复边界

新代码可以读取 v1/v2 身份文件；读取本身不升级内容。身份写操作使用 v3，保留账号和设备绑定，同时新增可选本机主人。新版注册表在 Windows 使用文件锁串行化多个新版进程的更新。旧程序不理解 v3，不能在新身份已经写入后直接切回旧版继续写入。需要暂停相关服务，用兼容 v3 的版本修复；本轮没有给真实安装写入 v3。

## 本轮验证

使用 Windows 上真实构建的可执行文件、实际 HTTP 和无头 Edge，配合临时合成目录完成登录、初始化、暂停、过期版本冲突、恢复、退出、重启保留身份和文件，以及 1280 浅色与 375 深色页面检查。没有模型调用、真实微信消息或真实账号数据。

```powershell
python -B scripts/test_local_home_ui.py `
  --binary temp/local-home/agent-home-station.exe `
  --output temp/local-home/browser
```

此脚本需要本机 Python Playwright 和 Edge；截图与测试目录均不进入 Git。重启保留文件由测试程序写入合成日记验证，不能据此宣称 Agent 已具备自主写日记能力。

下一步：复用同一身份连接软件内聊天、记忆与任务，并让文件及执行能力在每次操作边界核对本机授权；随后再实施应用注册和微信迁移。
