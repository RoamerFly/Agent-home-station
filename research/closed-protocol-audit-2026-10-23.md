# 微信闭源协议镜像初步静态核查

> 此文保留初步调查时点，后续已完成定点反编译、鉴权和调用边界核查。请以 [最终报告](closed-protocol-final-2026-10-23.md) 为准，下列“尚未”不再代表当前进度。

日期：2026-10-23（Asia/Shanghai）。本报告只覆盖文件、镜像配置和公开部署配置；没有启动镜像、执行其中的程序或登录微信。

## 目标与身份

- 镜像：`registry.cn-shenzhen.aliyuncs.com/houhou/wechat-ipad:latest`
- 本次解析的 manifest digest：`sha256:2d7a2c9084fc8a6c354a4961312736abe36d1302a9910eb7e855899580f1adfc`
- 平台：Linux amd64；14 个压缩层，合计约 43.13 MiB。
- 每个下载层均以 SHA-256 对照 manifest 验证。这只能绑定内容身份，不能证明发布者可信或程序安全。
- image config 创建时间：2026-09-13T15:10:02.507068507Z。
- 配置未指定 User；工作目录 `/usr/wic-go`；CMD 为 `./entrypoint.sh`。

## 已观察的文件与元数据

- `/usr/wic-go/main`：24,360,624 字节，ELF x86-64。
- main SHA-256：`9b0f99ad74f11426aeb50b1c543cfadf3562338336d15dd4bc20e306089b0b2f`。
- REA inspect-go-binary 读取到 Go build metadata：`go1.24.13`，模块路径 `wechatdll`，`CGO_ENABLED=1`，`GOOS=linux`，`GOARCH=amd64`。
- REA Evidence ID：`ev_c73e07ccf680fa64d0d106aa9ea668d4533329b0cfe5e2ed4be21f70165f8101`。完整 Evidence 保存于 `evidence/rea-go-evidence.json`。
- `/usr/wic-go/lib/libv08.so`：242,120 字节。只确认它被打包，尚未确认具体调用和算法。
- 镜像还包含 Swagger JSON/YAML、数据文件、DLL 和启动脚本。

## 具体风险线索

1. 启动脚本写出 `httpaddr=0.0.0.0`、端口 9000，以及默认监听 0.0.0.0:9010 的 pprof 配置。是否从外部可达取决于实际端口映射、网络策略及程序行为。
2. 脚本包含指向外部固定 IP 的明文 HTTP `/unban` 和 `/unbanlife` 配置项，以及可配置的 UUID 服务地址。它们的存在不证明发生了网络请求、凭据外传或恶意行为；需要追踪调用点和隔离环境中的运行证据。
3. 公开 openchat-wx 服务器 Compose 将 Docker socket 挂给管理后端，同时向其提供数据库等服务的凭据。不是本协议容器直接挂载 socket，但整套部署的管理后端有较强权限，必须与协议容器分别审计。
4. 镜像标签使用 latest；后续版本变化可能让本次核查失效，应绑定 digest 再复核。

## 可行性判断与未知项

- 观察到主程序的 Go 构建信息、本地库及接口文档，可开展进一步静态分析。
- 不能由构建信息推导已恢复源码或协议，也不能确认 libv08.so 就是协议算法核心。
- 尚未进行 Ghidra 反编译、调用链追踪、出站流量观察、恶意行为验证或登录收发测试。
- 尚不能证明镜像安全，也不能证明存在后门。
- 离线检查与恢复独立协议实现是不同目标。后者还取决于算法、状态机、设备数据和远端服务依赖。

## REA 环境

- 本机 REA CLI 6.5.0；Node 24.15.0。
- doctor 确认 Codex 注册配置 aligned，Ghidra 12.1.4 和 JDK 24 可用。
- 当前聊天没有暴露 REA MCP 工具；本次通过已安装 CLI 调用 inspect-go-binary。
- 没有修改用户的 MCP 配置，没有安装或升级分析工具。

## 来源

- [上游 README：协议服务闭源说明](https://github.com/hp0912/wechat-robot-client)
- [openchat-wx 服务器部署配置](https://github.com/xiaoguiwucan/openchat-wx/blob/main/.deploy/server/docker-compose.yml)
- [Docker daemon 权限说明](https://docs.docker.com/engine/security/#docker-daemon-attack-surface)
- 镜像 config、层清单、选定文件和 REA Evidence（本机离线取得）。
