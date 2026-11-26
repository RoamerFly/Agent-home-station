# 运行时

`main.py` 是 MCP stdio 入口，`public_share.py` 管理公开页、快手与 B 站解析。仅使用 Python 标准库，通过 `wechat/http_read` 请求宿主读取，不创建网络套接字，不读取宿主环境凭据，不发送微信。

当前入口提供已就绪的、来源绑定的公开页和视频号解析；综合 `share.read` 仍是无权限且不可发现的迁移占位。单文件公开视频地址返回为 `private_media_locators`；B站 DASH 返回 `private_stream_pairs`，每项含同一官方播放响应的 `video_url` 和 `audio_url`，由宿主下载合并。解析结果仍须由宿主核对实际媒体，不能当作可发送素材、已下载或已观察视频。

视频号 feed 若省略 `objectId`，解析器仅接受宿主提供的原生 `expected_object_id` 与本次 parse/export/feed 关联：`sceneInfo.dynamicExportId` 必须精确匹配实际请求的 export。显式对象身份不能被该兼容分支覆盖。未提供 `decodeKey` 时保留缺失状态，后续实际媒体下载、容器解析和解码由宿主独立核验。
