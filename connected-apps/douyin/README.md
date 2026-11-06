# 抖音 · 官方视频搜索

Windows 本地成长站可选择随包提供抖音应用。

```powershell
./scripts/Build-Desktop.ps1 -LocalStation -WithDouyinApp
```

主人先为抖音开放平台的应用取得 `aweme.dy.video_search` 能力，再在平台侧生成该应用的 `client_token`。按凭据返回的 `expires_in` 计算过期时间。设备编号须按平台要求提供。[视频搜索接口](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/douyin-search-capability/aweme-dy-video-search) · [生成 client_token](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/account-permission/client-token)

将设置放在独立站点私有数据目录的 `apps/douyin/search-settings.json`，限制为主人和站点进程可读。填写自己的有效令牌、设备编号、过期时间和明确获准的关键词。以下为格式示例。

```json
{
  "schema_version": 1,
  "enabled": true,
  "client_token": "<抖音开放平台应用令牌>",
  "expires_at": "2030-01-01T00:00:00+08:00",
  "device_id": "1",
  "keywords": ["城市公共交通"]
}
```

管理页「应用 → 抖音」显示本机设置状态。通过本地聊天的 `app_observe(action=list)` 获取关键词及其编号，再以 `field=search` 读取。默认每页 10 条，可选 1–50 条。续读时省略 `limit`，重新搜索时传 `refresh=true`。同一关键词的查询游标由宿主保存。

结果只表示官方搜索中返回的候选作品。标题、编号、发布时间和可核对的链接可用于继续选题。画面、音轨、播放和账号内容各自需要后续的独立接入。
