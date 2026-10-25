# 后台文字发送：公开协议源码可行性调查

调查范围是从镜像模块名 `wechatdll`、`models/Msg.SendNewMsg`、`Algorithm` 和 `Mmtls` 出发找到的公开源码。所有结论均为源代码与构建检查；未登录微信、未向微信服务器发送请求。

## 结论

**最接近完整纯后台文字发送的是 [git.yixinzone.com/library/wechatdll](https://git.yixinzone.com/library/wechatdll)，但当前不能作为可合法复用的生产基座。** 原因是该公开 checkout 没有 `LICENSE` 或 `COPYING`，无法从仓库内容确定复制、修改和分发许可；且最近提交是 2024-05-23，未证明仍能登录或群发。它适合作为协议行为和模块边界的参考；若取得权利人书面许可，可作为实验性 fork 的候选。

其余公开 MMTLS 项目只解决传输层，缺少账户登录、完整业务请求和群消息发送，不能单独达成目标。

## 1. wechatdll：技术上最接近，法律许可未确认

- 源码：<https://git.yixinzone.com/library/wechatdll>
- 已检查提交：`e2b99d3d23a77d0ae4c606dfc3f1ce09aa979339`，提交时间 2024-05-23 11:28:16 +08:00。
- 许可：根目录没有 `LICENSE`、`COPYING` 或等价许可文本。公开可读不等于授权复用，因此不能标为“可合法复用”。
- 构建：在该提交上使用 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 成功退出。宿主 Windows 直接构建会因 Linux epoll 源文件与 Windows 目标不匹配失败；这不影响其 Ubuntu/Docker 目标可构建性。
- 依赖：`go.mod` 只声明 Go 模块；检查树中未发现 `.dll` 或 `.so`。普通 `Dockerfile` 以 `golang:1.15.4` 下载模块并构建源码。`Dockerfile.http` 例外地使用 `FROM wic-go:1.0.1`，该基础镜像未由源码证明，故不应作为可审计构建链。

### 群文字发送的代码证据

`models/Msg/SendNewMsg.go:14–20` 定义 `Wxid`、`ToWxid`、`Content`、`Type`、`At`。第 22 行的 `SendNewMsg`：

- 在 37–55 行生成 `mm.NewSendMsgRequest`，将 `ToWxid` 写入目标会话，将 `Content` 写入消息内容；
- 在 58–60 行当 `At` 非空时生成 `<atuserlist>`；
- 在 68–82 行调用 `/cgi-bin/micromsg-bin/newsendmsg`，CGI 值为 522；
- 使用来自登录数据的 UIN、cookie、session key、ECDH key 和 MMTLS 客户端。

这支持“源代码中存在后台向指定会话发送文本及群 @ 的实现”。`ToWxid` 是否是当前有效群 ID、现行服务器是否接受此包、登录所需设备数据是否仍可获得，都没有通过本调查验证。

### 隐含状态与运行条件

`comm/Redis.go:39–69` 的 `LoginData` 包含 UIN、cookie、session key、ECDH 相关字段、设备 token 和 MMTLS 状态，并持久化到 Redis（初始化见 134–145 行）。该事实意味着即使不依赖闭源本地库，服务仍将保管高度敏感的会话材料。

## 2. duo/gommtls：仅传输层参考，不能发群消息

- 源码：<https://github.com/duo/gommtls>
- 已检查提交：`d82b1a1ef921f3e6a1ef124ccf6a935036e66646`，提交时间 2023-11-06 08:26:43 +08:00。
- 许可：同样没有 `LICENSE` 或 `COPYING`，因此不能假定许可。README 还限定“仅用于协议的研究学习”。
- 构建：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 成功。
- 缺口：`cmd/main.go` 演示 MMTLS 握手和 session 装载；仓库没有 `SendNewMsg`、微信业务 protobuf 定义、账户登录或群成员/群 ID 处理。它不能单独实现本项目的后台群文字发送。

## 3. Citizen Lab 文档：MIT 参考资料，不是发送实现

[wechat-security-report](https://github.com/citizenlab/wechat-security-report) 标注 MIT，包含 MMTLS 文档、流量样本和 Frida 工具。它是合规的研究参考来源，但没有作为完整账户客户端发送群文字的实现。

## 推荐的工程决策

目前没有发现同时满足“完整当前后台群发”“公开完整源码”“明确可复用许可证”“无闭源授权服务”的基座。若要自行源码构建，最现实的路线是以 `wechatdll` 的**协议结构作参考**，在获得许可之前避免直接复制其代码；MMTLS 部分必须自行清理权利链或采用经许可的实现。先做隔离的纯文本发送最小原型，并把登录、设备令牌、MMTLS、消息 protobuf、状态存储拆成模块。编译成功只说明构建可行，不能替代小号测试和安全评估。

机器可读记录：[send-source-options.json](evidence/send-source-options.json)。
