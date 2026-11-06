# 哔哩哔哩应用 · 公开资料读取

Windows 本地成长站可随包提供此应用。在「应用 → 哔哩哔哩 → 公开读取」填写公开稿件 BV 号并保存授权。开启后，大肥鱼可在本地聊天使用 `app_observe`，从授权列表读取标题或投稿者填写的简介。

接入使用 B 站网站的匿名稿件资料接口。网页服务可能限流或调整接口。遇到限流，可稍后用原 BV 号重试。接口的已读范围是标题和简介。其他字段分别授权、分别记录覆盖范围。

开启「读取公开一级评论」后，可用 `app_observe(field=comments)` 分页读取获准稿件的评论文字、评论编号和发布时间。默认每页 10 条，可选 1–30 条。续读省略 `limit` 即可沿用首页大小。每页按平台返回顺序，评论区实时变化，覆盖范围只表示本次返回的一级评论文字。

需要读取页面时，可另行开启「使用独立浏览器读取页面文本」。本机 Edge 可用时，大肥鱼可调用 `app_observe(field=page_text)` 读取获准 BV 页面的标题和可用简介。这个页面文字入口和匿名稿件接口分别标注来源。

进一步开启播放授权后，可用 `app_observe(field=playback)` 从开头采样公开单段 MP4。当前媒体上限为 32 MiB。默认静音采样 6 秒，可用 `limit` 选 1–20 秒。结果包含 Edge 核实的已播放区间及就绪、暂停和结束状态。只有已结束且实际区间覆盖整段，才会返回完整播放。

## 音轨读取

音轨需要 FFmpeg 和已启用的本地媒体识别工具。用 `scripts/Build-Desktop.ps1 -LocalStation -WithBilibiliApp -WithMediaUnderstanding` 构建含这两项声明的 Windows 包。

1. 主人在大肥鱼家目录和站点私有数据之外准备本机识别资源。可用 `python scripts/install_local_asr.py --workspace <资源工作区> --archive <核验过的模型归档> --wheel-dir <SDK 本地轮子目录>` 生成资源工作区下的 `public_workspace/manifests/asr-sensevoice-small-int8.json`。也可复用同样结构的已有只读模型资源。
2. 在独立站点的私有数据目录创建 `station-asr.json`。填写资源工作区的绝对路径，也就是包含 `public_workspace` 的目录。

   ```json
   {
     "schema_version": 1,
     "enabled": true,
     "workspace": "D:/AgentResources/Station-ASR",
     "profile_id": "sensevoice-small-int8"
   }
   ```

3. 在「项目工具」中安装并启用「本地媒体识别」。在「应用 → 哔哩哔哩」保存获准 BV，并开启「使用本机语音识别读取音轨」。

`app_observe(field=audio)` 默认读取 60 秒，可选 1–240 秒。结果包含本页窗口、实际 PCM 时长和转写状态，续读从上一窗口结束位置开始。当前只处理 32 MiB 内的匿名公开单段 MP4，续页再次核对媒体摘要。无音轨和未识别到词会单独标注。

Windows 隔离验证包使用 `scripts/Build-Desktop.ps1 -LocalStation -WithBilibiliApp` 构建。家目录的源码副本便于检查和编辑，主程序使用随包校验过的适配器。


## 创作发布

适配器内已准备初始化、分片传输和分片合并的 Go 客户端。初始化与合并使用开放平台 2.0 签名，调用方依序安排分片，客户端发送裸文件字节。分片接口只接收上传令牌，应用密钥和用户访问令牌留在签名接口。每次签名生成新的随机数。客户端区分参数、鉴权、权限、限流、处理中及服务错误，不自动重新提交请求；宿主可调整请求超时。[官方签名规范](https://open.bilibili.com/doc/4/8673959e-f7bb-56e6-6e68-d225f971b81b) · [官方分片上传说明](https://open.bilibili.com/doc/4/733a520a-c50f-7bb4-17cb-35338ba20500)

主人在 B 站取得对应应用的投稿许可和用户 OAuth 后，将它们登记到**站点私有数据目录**的 `apps/bilibili/publication-settings.json`。账号编号填写与 OAuth 对应的稳定账号标识，失效时间使用 RFC 3339。启用发布是独立授权。以下为字段示例，不能用于真实调用。

```json
{
  "schema_version": 1,
  "enabled": false,
  "client_id": "<开放平台应用编号>",
  "app_secret": "<应用签名密钥>",
  "access_token": "<同一应用和账号的用户令牌>",
  "account_id": "<同一账号的稳定标识>",
  "expires_at": "2030-01-01T00:00:00Z"
}
```

发布目标为 `bilibili / publish`。本地聊天中的 `app_publish(action=draft)` 绑定一份登记过的 MP4、标题、简介和 `platform_options`。后者包含明确的 `category_id`、`tags`、`copyright`（1 原创，2 转载）及可选的 `no_reprint`。转载需要 `source`。已验证的草稿使用原 `job_id` 提交。

分片默认 10 MiB。主人可设置 `part_bytes`，当前客户端接受 1–64 MiB，这是宿主分片资源边界。任务进度按原始配置和字节范围保存。中断后 `status` 读取进度，`resume` 从原令牌的第一个未确认分片继续。初始化、合并和投稿一旦发起但未得到可核实结果，保留待核对状态。已取得稿件编号时，`status` 查询原稿件。平台明确报告开放浏览后才记为已发布。[官方单稿状态查询](https://openhome.bilibili.com/doc/4/d9554788-dcef-f139-6217-b487d41c3826)

签名和回执契约已用合成传输验证。初始化、合并和投稿请求形状参考了已归档的官方示例，生产接口兼容性与真实 OAuth 投稿仍待验收。当前只支持单个 MP4，尚无 OAuth 获取与续期界面，也没有缺失稿件编号时的自动对账接口。[官方历史示例](https://github.com/bilibili-openplatform/demo/tree/331ed2d8918cbb8a3b069dec488129f19383e4b9)
