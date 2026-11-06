# 抖音 · 官方应用接入

Windows 本地成长站可选择随包提供抖音应用。

```powershell
./scripts/Build-Desktop.ps1 -LocalStation -WithDouyinApp
```

## 连接抖音账号

先在抖音开放平台准备审核通过的网站应用，并登记由你控制的 HTTPS 回调地址。

1. 在 Windows 成长站打开「应用 → 抖音 → 扫码授权」，填写 Client Key、Client Secret 和已登记的回调地址。
2. 点击「打开扫码窗口」，用手机抖音扫描官方授权页的二维码并确认。窗口只用于授权，完成后自动关闭。
3. 成长站核对回跳、兑换授权码并确认账号身份，随后显示昵称和授权到期时间。取消或超过十分钟后可以重新发起。更换账号前先移除本机连接。

如果直接从浏览器访问管理页，仍可打开官方授权页并手动粘贴完整回跳地址。此方式的回调网页应保留地址栏查询参数，并避免记录或转发授权码。

默认请求读取账号公开资料的 user_info。移除本机授权会清除本机令牌，需要在平台侧取消授权时，可从抖音的授权管理处理。关键词搜索和本人作品状态仍使用下方各自的授权。[抖音网页授权](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/sdk/web-app/web/permission/) · [获取用户公开信息](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/account-permission/get-account-open-info)

## 读取已授权账号的作品列表

如果你的开放平台网站应用开通了「查询授权账号的视频列表」能力，可在扫码时勾选「授权读取我的作品列表」。手机同意并由平台确认作品权限后，`app_observe(action=list)` 会列出该账号的 `account_works`。重新授权时取消勾选，或移除账号连接，可撤销本机读取。已有账号授权需要显式重新授权才会开通这项能力。

`field=account_works` 默认每页 10 条。首读可设置 `limit`，续读省略或沿用原值，`refresh=true` 重新从首页读取。平台文档限制每轮最多四页，第四页若还有记录会明确标记覆盖不完整。超出本机 1 MiB 响应边界时，可以改用较小页大小重新读取。分页进度保存在宿主，作品条目不会持久缓存。

列表只提供作品编号及平台创建时间。平台的加密作品标识显示为宿主生成的来源编号。公开状态通过独立的本人作品状态查询核对。普通抖音登录与搜索权限无法启用此列表。当前已验证合成分页和官方端点的令牌校验，真实账号授权查询取决于平台对该网站应用的权限。[官方作品列表契约](https://open.douyin.com/platform/resource/docs/openapi/video-management/douyin/search-video/account-video-list) · [平台权限及字段调整](https://developer.open-douyin.com/forum/bulletin/post/6736c38048f043667b75f70a)

## 上传和发布视频

开放平台网站应用取得「代替用户发布内容到抖音」和「视频基础信息查询」后，在应用内扫码时单独勾选「授权发布抖音视频」。平台回跳和令牌须同时包含 `video.create.bind`、`posting.behavior`。发布权限与作品列表分开。

在本地聊天里先登记成品，再用 `app_publish(action=targets)` 查找目标。抖音使用 `app_id=douyin` 和 `connection_id=authorized-account`，一次提交一份 MP4 或 WebM。`platform_options.visibility` 需明确填 `public`、`private` 或 `friends`。标题和正文合成抖音文案，最多 1000 字。宿主按平台文档处理 4 GB 内成品，超过 50 MB 采用分片，靠近请求时限时可在已确认分片后暂停。

保存草稿、提交、继续、查询和取消使用同一个 `job_id`。可从「应用 → 抖音 → 创作发布」查看进度。上传后的媒资编号与创建后的作品编号分别记录。平台创建回执显示为已接受，查询到同一作品明确公开后，才确认公开发布。仅自己或好友可见的作品保留已接受状态，公开状态接口无法核对具体私密范围。

提交结果未知时保留原任务。若平台未返回作品编号，后续不能凭相似标题认领或再次创建，只能在抖音侧核对。取消停止未发起的后续步骤，已创建的作品仍在抖音侧管理。Windows 实际发布、审核结果和各权限开通情况仍需真实环境验证。[上传视频](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/video-management/douyin/create-video/upload-video) · [创建视频](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/video-management/douyin/create-video/video-create) · [查询视频基础信息](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/video-management/posting-task/video-basic-info)

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
  "follow_public_videos": true,
  "keywords": ["城市公共交通"]
}
```

管理页「应用 → 抖音」显示本机设置状态。通过本地聊天的 `app_observe(action=list)` 获取关键词及其编号，再以 `field=search` 读取。默认每页 10 条，可选 1–50 条。续读时省略 `limit`，重新搜索时传 `refresh=true`。同一关键词的查询游标由宿主保存。

结果表示官方搜索中返回的候选作品。主人可选填 follow_public_videos=true，允许跟进来源链接与作品编号相符的结果。带 follow_up_content_id 的条目可使用同一个 search 连接，将该值作为 content_id、field 设为 player_entry。只传本次结果的编号，无需手写 URL 或填写 limit。

宿主会用官方匿名接口重新核对这个作品的播放器入口。作品变为非公开后会返回平台状态。播放器入口和标题只提供来源，播放、画面和音轨是独立观察。省略开关时仅搜索。[官方播放器接口](https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/video-management/douyin/iframe-player/get-iframe-by-video)

## 后台静音播放采样

确认要允许大肥鱼播放公开作品时，可在同一份搜索配置同时开启 follow_public_videos 和 sample_public_playback。播放器入口返回 playback 跟进编号后，通过 app_observe 使用同一 search 连接、field=playback 和该编号采样。limit 默认 6 秒，可选 1–20 秒。应用在隔离 Edge 中打开已核验的官方播放器，没有前台浏览器页面。每次只记录实际已播放区间，不读取画面或转写音轨。

平台播放器改变接口、作品变为非公开或播放未推进时，区分返回失败、无实际进度或有进度。当前以 Windows 和已核验的官方播放器请求范围为准，后续平台版本仍需实测。

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
