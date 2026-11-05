# 运行与部署

## 已配置的 Windows 本机

双击根目录 `启动大肥鱼.vbs` 恢复已有WSL实例并打开管理页。

- WSL：`Ubuntu-New`，用户 `fly`。
- 管理端：http://127.0.0.1:8081/ 。
- 原生接入：WSL loopback `6174`；管理端使用原生接入。旧独立发送API不随本轮启动。
- 私有运行目录由 `secrets/local-deployment.json` 的 `data_root` 指定；本机当前为 `/home/fly/.local/share/wechat-agent-local-20261028`。原目录保留为回滚依据。
- 用户服务：`wechat-bot-native.service`、`wechat-bot-web.service`。旧`wechat-bot-ai.service`不得与网页机器人同时启动。

启动脚本保留原有配置中的机器人启停选择。管理端存活与微信实际在线不同，请进入网页查看连接状态。

需要指定另一套已安装实例时，从项目根目录运行：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/Start-Local-Admin.ps1 `
  -Distro Ubuntu-New -LinuxUser fly `
  -DataRoot /home/fly/.local/share/wechat-bot-native-research-20261024 `
  -Account background-test-20261024
```

本机YuE按需运行 `启动大肥鱼.bat Music`。登录Windows自动启动的触发器已关闭；启动器不会重新开启它。未安装本机YuE时可单独启动管理端和微信聊天。

本机管理端的源代码更新入口统一为 `scripts/update_local_web.py`（在WSL内执行，先准备`dist/linux-amd64/wechat-admin`）；旧`research/scripts/update_local_web.py`仅保留兼容转发。更新前等待活动工作结束，更新后检查健康再保留近期回滚程序。独立预览历史程序清理可运行`python scripts/binary_retention.py --directory <私有运行目录>/bin --keep 3`，默认只输出清单，实际执行须指定匹配的`--apply-plan-hash`。

## 新机器和 Linux

源码仓库不包含微信登录、Key、模型、原生程序或Python/CUDA环境。新机器须按[原生依赖清单](../components/native-client/README.md)和[YuE集成说明](../components/yue2/README.md)准备依赖，并使用独立私有目录授权。不可只复制源码后同时打开同一个微信会话目录。

```bash
go test ./...
go vet ./...
go build -o dist/wechat-admin ./cmd/wechat-admin
```

Linux管理端使用环境变量配置：`WECHAT_ADMIN_DATA_DIR`、`WECHAT_ADMIN_LISTEN`、`WECHAT_NATIVE_URL`、`WECHAT_NATIVE_TOKEN_FILE`、`WECHAT_NATIVE_ACCOUNT`、`WECHAT_BOT_STATE_DIR`、`WECHAT_WORKSPACE_ROOT`。Windows本机启动器依赖WSL，不能作为Linux服务器的服务管理器。

跨平台新机环境和按需工具安装路径见[镜像构建方案](../镜像构建方案.md)。本机启动器不会自动安装新机器的 WSL、原生微信程序或模型。

## 容器草稿

`docker/`集中存放既有Dockerfile与Compose，保留为后续部署参考，本轮不构建或导出镜像。其中早期iPad/CLI方案并非当前网页Agent的完整交付。

移动后的Compose构建上下文指向仓库根目录。例如，未来进入容器阶段后，原生API方案的配置路径为 `deploy/docker/compose.linux-native.yaml`，Dockerfile为 `deploy/docker/Dockerfile.linux-stack`。

已删除固定导出旧`0.3.0`镜像的过期脚本，防止把历史镜像误当最新版打包。后续发布须明确版本、重新验证依赖和目标环境。
