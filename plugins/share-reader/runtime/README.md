# 运行时

`main.py` 是 MCP stdio 入口，`public_share.py` 管理公开页与快手解析。仅使用 Python 标准库，通过 `wechat/http_read` 请求宿主读取，不创建网络套接字，不读取宿主环境凭据，不发送微信。

当前入口用于迁移开发和合成隔离测试。整个包仍为 migration-pending，宿主必须拒绝正式安装。公开视频地址返回为 private_media_locators，后续由宿主媒体代理校验并传递，不能当作可发送素材或已观察视频。
