# 抖音 · 官方只读接入

Windows 本地成长站可选择随包提供抖音应用。

```powershell
./scripts/Build-Desktop.ps1 -LocalStation -WithDouyinApp
```

## 官方视频搜索

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

## 已授权本人作品

在抖音开放平台为应用申请用户投稿任务能力，由作品所属账号授权 `posting.behavior`。将同一次 OAuth 授权返回的用户令牌、账号编号和到期时间写到站点私有数据目录下的 `apps/douyin/own-videos-settings.json`。在 `video_ids` 填写主人允许查询的明文作品编号。下面是格式示例。[官方作品查询](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/video-management/posting-task/video-basic-info)

```json
{
  "schema_version": 1,
  "enabled": true,
  "scope": "posting.behavior",
  "access_token": "<授权返回的用户令牌>",
  "open_id": "<同次授权返回的账号编号>",
  "expires_at": "2030-01-01T00:00:00+08:00",
  "video_ids": ["7000000000000000001"]
}
```

管理页会单独显示「本人作品读取」状态。通过 `app_observe(action=list)` 查到已登记作品后，以 `connection_id=own-videos`、`field=own_video` 和该作品编号查询。每次都读平台当前状态，结果带观察时间。已知状态有审核中、不适宜公开和公开视频，其他状态以未知标注。只有平台明确返回公开视频时才附上可用的标题与作品创建时间。[平台状态说明](https://developer.open-douyin.com/forum/bulletin/post/6736c38048f043667b75f70a)

令牌续期由主人按平台流程处理。该连接只读核对指定作品的状态，后续上传、投稿与发布回执由独立能力处理。
