# 视频号后台读取候选核实（2026-10-25）

目标是取得账号收到的同一视频的真实可播放内容，再送入本项目的画面/音轨理解；下载器的存在、标题或元宝总结本身不代表本项目已观看。以下为源码与一次受控请求证据，不保证未来上游接口持续可用。

| 项目 | 已核实路径 | 对本项目的适配判断 |
| --- | --- | --- |
| [oliver-zch/wx-video-channel-download](https://github.com/oliver-zch/wx-video-channel-download) | Go HTTP 服务；`parseShareUrl` 带元宝 Cookie 请求 `get_parse_result`，从播放页参数取得 `exportId/generalToken`，再请求 `get_feed_info` | 适合作为轻量后台解析思路。MIT；固定核实提交 `baf68f25726b2f4a7f1885721ac993feeb18df37`。源码存在直接记录播放参数的日志，若复用需改为私有传输及脱敏，不可原样引入。不是无需授权的解析器。 |
| [LcpMarvel/sph](https://github.com/LcpMarvel/sph) | Rust CLI，提供 Linux 构建；账号文档区分视频号助手与元宝下载凭据，支持 `login --yuanbao`、`inspect`、`download` | 可作为独立进程适配候选，不要求整个桌面应用常驻；仍需下载授权凭据。本轮没有编译、安装或导入其代码；仓库元数据未给出明确许可证。固定提交 `a5565383958551e94dd43fc5f065498db1ecf592`。 |
| [topgptus/WechatVideoDL](https://github.com/topgptus/WechatVideoDL) | HTTP 异步任务、Browserless 无头浏览器调用元宝、保存MP4和总结；管理员配置专用元宝 Cookie/Session | 可作外部服务，但组件和浏览器开销更高。本轮没有安装或复用源码；仓库元数据未给出明确许可证。固定提交 `25dd8d95dad1c255ed08cc7d1e2a360ba0f56824`。 |
| [joeseesun/qiaomu-download](https://github.com/joeseesun/qiaomu-download/blob/main/references/wechat-video.md) | 固定两个在线解析器；或通过本机已登录微信与下载后端取得媒体 | 在线路线省本地登录流程，但依赖外部服务；本地路线需要已有播放授权。固定核实提交 `3d05f5f9391d032c4faa8511ef8f14e08e6fae37`。 |
| [ltaoo/wx_channels_download](https://github.com/ltaoo/wx_channels_download/blob/main/docs/cli/mcp.md) | 提供HTTP/stdio MCP，视频号页面桥接可取得详情，也有需配置凭据的SPH Worker | 工具接口较完整，但不能凭MCP接口存在就推断无需登录。当前LICENSE为MIT附Commons Clause，需连同附加条款评估，不能当纯MIT整包引入。本轮未安装、未部署Worker、未改代理或证书。 |

## 对用户公开视频短链的受控实测

只提交用户提供的公开视频号短链，不发送微信会话、设备信息、聊天记录或本机文件。2026-10-25各请求一次：

- `sph.litao.workers.dev/api/fetch_video_profile`：HTTP401，未取得媒体。
- `wx-dl.qiaomu.ai/api/fetch_video_profile`：HTTP500，未取得媒体。
- 元宝 `get_parse_result` 的无Cookie请求：HTTP401，未取得媒体。

这些是当时的请求结果，不等于永久不可用。不能将README所述“无需登录”替代真实返回状态，也不能据此编造视频内容。当前群目录最近290条消息只找到2条视频号卡片，均有定位但无decodeKey；本轮仅核对结构，未据此重新回复群聊。

## 后续实现选择

优先在现有Go服务内实现可配置的授权解析适配，或连接管理员部署的解析服务；凭据仅在本机私有文件/网页配置，模型只见工具schema和经实际解码的观察结果。暂不引入整套浏览器后台。接入需同时验证短链/对象绑定、账号/会话范围、下载地址和重定向、最大字节数、正确密钥、ffprobe/解码及画面/音轨覆盖范围；任一阶段未取得资料就明确降级。

本轮尚未完成此授权解析适配，也未完成真实视频号端到端验收。已经完成的是普通微信视频与可播放视频号共用的观察层修复，见 `channels-content-understanding.md`。默认不使用外部解析器，不自动读取现有浏览器Cookie。
