# 微信视频号实际理解：实现与验证边界

## 已实现的读取路径

原生接入增加账号内 `GET /v1/accounts/{account}/messages/{message}/channels-media`，同时要求 read/media 权限。只读本账号已接收、未撤回的51类视频号卡片，返回账号、会话、消息绑定的私有播放定位信息。卡片中的视频对象ID、nonce、媒体URL和可能存在的 decodeKey 不进入普通消息列表、模型上下文或系统日志。URL不能由模型覆盖。

管理端在“读取视频”权限开启时尝试真实获取视频，最多50MiB、总时限2分钟，公开下载路径执行原有地址/重定向限制。有匹配的数值密钥时使用 ISAAC64 处理最多128KiB前缀；只有 ffprobe 实际识别为可播放视频，才均匀抽取6帧送视觉模型。启用ASR时另取前180秒以内音轨转写。观察结果明确是抽样画面和有限音轨，不能声称完整观看。源卡片仍作为引用目标。

不可播放、缺少定位、缺少密钥或视觉调用失败时保留标题/简介，附固定原因，并明确未观看。取得URL、解码函数存在、甚至下载到视频字节，都不等于已理解视频内容。

## 当前真实验证结果

2026-10-25对亲子群旧卡片的实际读取取得1,591,452字节、HTTP内容类型 video/mp4；但本条卡片没有 decodeKey，文件无法通过 ffprobe，没有生成观察摘要，也没有向群回复。

用户另提供 `weixin.qq.com/sph/…` 公开短链。本机HTTP可转到微信 finder-preview 动态页面。公开网页脚本显示它通过 `POST /finder-preview/api/feed/get_feed_info`，携带 shortUri/exportId 和 generalToken 获取详情。相同链接的匿名请求实际返回HTTP401 `permission verification failed`；页面本身没有返回视频画面。这证明只拿到这个短链还不足以在当前环境观看，不能借标题声称成功。

现阶段**尚未完成真实视频号端到端理解验收**。需要后续在正常微信播放环境取得同一视频对应的授权详情/播放信息；不能凭其他视频的密钥尝试解码，也不能把转发标题当视频总结。

## 调研所得的后续选择

- [wx-video-channel-download](https://github.com/oliver-zch/wx-video-channel-download)：包含取详情与前缀解码实现，但相关取详情路径需要授权态；只复用带MIT署名的解码算法，没有照搬硬编码设备头或凭据日志。
- [wx_channels_download 桌面桥接](https://github.com/ltaoo/wx_channels_download/blob/main/docs/feature/bridge.md)：实际微信视频号窗口作为播放设备，通过本地WebSocket桥取得对象详情。适合有专门授权的Windows/macOS微信实例；不是无登录的通用网页抓取。
- [wx_channel 本地API](https://github.com/nobiyou/wx_channel/blob/main/docs/API_QUICK_START.md)：可以包装成本项目的外部适配服务，但依赖桌面微信、注入页/代理或对应运行环境。尚未安装、未改系统代理、未导入根证书。
- [MobileAgent](https://github.com/X-PLUG/MobileAgent)：授权手机视觉操作可以真正打开、滚动和观看内容，但需要专用设备、账号授权与更高运行成本。尚未接入。

对外适配器应交付同一对象的可验证视频/音轨及来源证据，或提供播放器截图/转写与具体观察时间范围；管理员配置连接和权限，模型只接收观察结果。生成/读取网页的浏览器也应维护独立会话，不能使用任意用户的浏览器认证信息。

原生累计补丁和来源哈希在 `components/native-client/source-manifest.json`，解码移植许可证在 `internal/channels/LICENSE.upstream`。测试包含结构/范围隔离、缺失信息不编造、前缀处理边界；本次真实卡片因为缺少可播放内容而未通过实际观察测试，这一失败作为边界保留。
