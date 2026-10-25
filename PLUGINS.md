# API / MCP 插件配置与开发

插件中心支持模板、粘贴 JSON、导入 JSON 文件、独立密钥、启停、移除、导出无密钥声明。兼容接口可动态接入，不需重编译或重新生成镜像。当前没有托管的第三方在线市场、评分、签名或自动更新体系。

## 接入方式

| 类型 | 配置内容 | 适用场景 |
|---|---|---|
| 声明式 HTTP API | 固定地址、JSON 请求模板、返回字段或二进制、可选轮询 | TTS、绘图、音乐/人声、视频等常见生成接口 |
| MCP | Streamable HTTP 或 stdio，发现工具描述与参数 | 已有 MCP、复杂 SDK/OAuth/WebSocket、外部任务适配服务 |

远程 API/MCP 只需配置连接。stdio 需要管理端所在的 Linux 环境已安装对应命令与依赖，Windows 的 E 盘 node.exe 不能直接用于 Linux。声明不会安装依赖；外部程序使用管理端用户权限。正常聊天为每个平台/账号/会话维护独立的MCP会话，stdio进程复用初始化状态，HTTP保留会话头；发现缓存30秒、空闲5分钟关闭，运行退出时清理。取消/断连后不自动重播不确定的操作。手动检查和预演仍使用短会话。详见 [生命周期检查点](docs/lifecycle-upgrade.md)。

## 同步语音 API 示例

这是契约示例，必须替换自己的真实地址、音色和字段，不是任何供应商的开箱配置：

```json
{
  "id": "my_voice",
  "name": "我的鲸鱼娘声音",
  "version": "1",
  "description": "文字转为已配置的角色音色",
  "tools": [{
    "name": "speak",
    "description": "适合语音回复时，将简短中文合成为角色声音。",
    "input_schema": {
      "type": "object",
      "properties": {"text": {"type": "string"}},
      "required": ["text"]
    },
    "url": "https://your-provider.example/v1/tts",
    "method": "POST",
    "timeout_seconds": 90,
    "headers": {"Authorization": "Bearer {{secret.API_KEY}}"},
    "body_template": {"text": "{{arg.text}}", "voice": "your_voice_id"},
    "media_url_path": "data.audio_url",
    "text_path": "data.note",
    "media_kind": "voice"
  }]
}
```

独立密钥框填写 `{"API_KEY":"你的服务商密钥"}`，以后留空保留旧值。常见鉴权头必须引用 secret 占位符，不接受直接写真实值。修改有密钥的声明时需重新填密钥或明确清除，避免凭据带往变更后的服务。

若接口直接返回 audio/wav、audio/mpeg 等二进制，只保留 media_kind 并删除 URL 映射即可。支持 voice/image/video/file；歌曲要发成微信原生语音选 voice，要保留音乐文件选 file，并开启对应权限。转码和分段由原生后台处理，微信限制仍可能造成拒绝。

文本接口只配 text_path，不设 media_kind。JSON 返回没有映射到任何文字或媒体时判失败，不能凭空报告成功。目前只支持 JSON 请求，GET/POST/PUT/PATCH；multipart、WebSocket、OAuth 等复杂需求可封装成外部 MCP。

## 异步任务轮询

长任务推荐同时声明 `poll` 和 `job`，持久保存编号，释放当前模型回合，由后台查询同一远端任务。只有 `poll` 的旧插件仍在当前工具调用中等待，保持兼容。音乐与视频模板新建时已包含 `job`，已安装的旧声明不会自动改写。

在工具中增加 poll。假设创建返回 `{"task_id":"abc","status":"pending"}`，完成返回 `{"status":"done","data":{"audio_url":"https://..."}}`：

```json
"poll": {
  "url": "https://your-provider.example/v1/tasks/{{result.task_id}}",
  "method": "GET",
  "status_path": "status",
  "done_values": ["done", "completed"],
  "failed_values": ["failed", "cancelled"],
  "media_url_path": "data.audio_url",
  "interval_seconds": 30,
  "max_attempts": 12
}
```

任务 ID 从创建响应解析一次，pending 不必重复给 ID。轮询主机固定，ID 仅填路径，模型不能改目标主机。同源默认沿用创建请求头；不同源不转发这些头，需用 poll.headers 显式配置目标鉴权。可用 poll.text_path 覆盖文字路径。

- `{{arg.text}}` 使用模型参数，整字段占位保留数字、布尔、数组等 JSON 类型。
- `{{secret.KEY}}` 使用管理员密钥，可用于请求头和 JSON 请求体。
- `{{result.task_id}}` 仅用于轮询路径。API URL 不接受 arg/secret 占位，凭据应放请求头或请求体。

创建请求不自动重试。单次 HTTP 请求默认 60 秒，可用 timeout_seconds 调到最多 120 秒；轮询最多 12 次、间隔最多 60 秒。单次角色决策总上限约 15 分钟，人工测试也为 15 分钟，停用/超时不会自动重新创建。机器人按会话并发处理，长生成与工具调用期间持续收件。持续任务保存检查点；运行中断后进入待复核状态，已提交的外部请求不会自动重新创建。外部服务的远端生成任务仍由该服务管理。

### 可跨重启的后台任务

在上述 `poll` 同级增加：

```json
"job": {
  "id_path": "task_id",
  "deadline_seconds": 3600,
  "max_polls": 240,
  "cancel": {
    "url": "https://your-provider.example/v1/tasks/{{result.task_id}}/cancel",
    "method": "POST"
  }
}
```

`id_path` 必须对应创建响应的真实任务编号，支持简单点路径、字符串及不丢精度的非负整数编号。轮询和取消 URL 仅以该编号填路径，主机由管理员固定；后台不保存创建参数或密钥副本。取消接口可省略，也可声明 DELETE、headers 和 JSON body_template；请求头可引用 secret，请求体可引用 secret 和该 result 编号，不能引用原始 arg。同源默认继承创建请求头，跨源需显式配置。插件声明改变后，旧任务停在“需要检查”，不会把旧编号提交到新接口。

后台模式使用 `poll.interval_seconds`，默认10秒；`job.deadline_seconds` 默认3600秒、范围30–86400秒，`job.max_polls` 默认240、最大1440。`poll.max_attempts` 只用于旧的同步等待模式。需要启用 Agent 和持续任务，原会话与插件/媒体权限仍逐次检查；这是用户已请求结果的续交，不要求主动问候权限。预演和人工工具测试不创建后台任务。普通 MCP 服务暂不自动获得此后台任务协议，仍使用其明确的工具契约。

创建前先保存领取记录；创建响应取得编号后保存。创建结果未知或进程在提交中中断时进入“需要检查”，不自动补交创建请求。已保存、等待中的任务重启后继续 GET 查询同一编号。模型通过 `plugin_job_status` 读取完成输出，附件进入正常发送队列，微信交付证据满足要求才标记完成。已暂存结果或已有交付记录的任务不能通过“恢复执行”重复发送，需核对原发送记录。

模型可用 `plugin_job_cancel` 取消本人在原会话发起的任务，可信身份权限另行校验。管理页可取消正在处理的插件任务，并显示远端状态、查询次数和取消结果。先停止本地后续交付，再至多调用一次声明的远端取消接口；HTTP接受请求不代表远端已停止计算。取消回执未知不自动重发，已提交微信的内容仍可能送达。查询超出期限或次数只停止本地轮询，不重新生成，也不代表远端已终止。

## MCP 插件示例

```json
{
  "id": "creative_mcp",
  "name": "创作工具",
  "version": "1",
  "mcp": {
    "id": "creative_mcp",
    "name": "创作工具",
    "transport": "http",
    "url": "https://your-mcp.example/mcp",
    "timeout_seconds": 600,
    "headers": {"Authorization": "Bearer {{secret.TOKEN}}"},
    "tools": ["speak", "sing", "make_video"]
  },
  "tool_media_kinds": {"speak":"voice", "sing":"voice", "make_video":"video"}
}
```

tool_media_kinds 让媒体总权限在调用前生效，避免关闭语音后仍调用已标注的收费语音工具。未标注的普通 MCP 工具无法自动推断用途，但媒体返回仍受发送权限控制。请为媒体工具填写映射并配置允许工具列表。总工具开关、搜索/网页读取开关、每次调用次数可在“联网与 MCP”设置。

也可直接在 MCP 页面配置服务数组，ID 不得与启用的插件重复。stdio 使用 `transport:"stdio"`、`command:"/usr/bin/node"`、`args:["/opt/my-mcp/server.mjs"]` 和 env。插件声明的 env/headers 可引用 secret；直接 MCP 配置的 env/headers 填实际值，读回隐藏，留空保留同一端点已有凭据。MCP 请求默认 20 秒，timeout_seconds 可调到最多 900 秒。

## MCP 媒体返回

标准 content 中 image/audio 的 data（Base64）和 mimeType 会转成暂存附件，不把二进制传给语言模型。stdio 帧约 1 MiB、HTTP 响应约 2 MiB 上限；大媒体应使用 URL。

也可让工具返回一段 text 内容，其值为以下 JSON：

```json
{
  "text": "已经生成结果",
  "media": [{
    "kind": "voice",
    "url": "https://public-cdn.example/generated-song.mp3",
    "mime_type": "audio/mpeg"
  }]
}
```

每次最多三份附件。媒体下载仅限公开网络 HTTP/HTTPS，拒绝本机/内网目标。自托管 API 可使用 loopback HTTP，但若返回本机媒体 URL，下载规则仍会拒绝；应直接返回二进制，或提供公开 HTTPS URL。

处理链路：服务返回→下载/解析→上传微信后台暂存→真实 source ID→模型选 media_ids→检查会话和发送权限→提交微信。人工工具测试和角色预演只暂存，不发微信。模型提示要求普通聊天不展示 MCP、接口、工具名和任务 ID，生成失败不能说唱好了。

## 上限

- 最多保存 30 个插件（含移除保留记录），每插件最多 20 个 API 工具，最多启用 80 个 API 工具；外部 MCP 合计最多 8 个，每服务最多 40 工具。
- 单轮工具步数在「Agent 控制 → 行动与任务」统一配置为 1–30；关闭自主行动后兼容旧版 MCP 预算。模型工具目录有 128 个/64 KiB 总预算，超大声明不会进入该次上下文。input_schema 必须描述对象，目前不是完整 JSON Schema 校验器；固定模板引用缺失字段会被拒绝。
- API JSON 响应上限 2 MiB、二进制 25 MiB；微信还有各类媒体限制。网页人工上传最多 64 MiB。
- 配置保存会重载角色进程，不需重建镜像。新的 stdio 依赖仍须另行安装，不保证任意第三方插件都能原样运行。
- 早期插件阶段验证了真实模型选择测试音附件、真实后台暂存和模拟媒体发送适配；之后已加入百炼语音与本地 YuE 接入，当前能力与证据以 FEATURES.md 和后文内置工具说明为准。


## 内置公开媒体下载（0.4.3）

`web_media_download` 不需安装插件或配置生成服务；受总工具、网页读取、对应媒体发送权限控制。参数为 `{ "url": "公开 HTTP(S) 文件地址", "kind": "voice|video|image|file" }`，按单个类型调用，返回可供最终 `reply.media_ids` 选择的真实暂存 ID。权限检查先于下载和上传；复用公开网络校验、重定向检查、大小限制及媒体类型检查，不接收伪装成媒体的 HTML 登录/播放器页面。公开资源下载不是站点通用解析器，不涵盖 DRM、需要登录的资源或分段流媒体。

`web_fetch` 仍提取正文，并附带页面中可识别的公开媒体候选链接。角色只使用用户或工具实际提供的 URL，现有音视频称为分享，不冒充生成或演唱。人工工具调用只暂存，不发微信。


## 0.6.0 联网记录与音乐来源

内置联网调用在独立“AI 网上冲浪”页面记录开始、结束、来源、耗时和结果，以同一 call_id 合并；日志不回显凭据。自主浏览设置独立存于 surf.json，分享目标受会话白名单、主动权限及发送前复核约束。默认关闭自主浏览，未配置目标时只浏览不分享。

`web_audio_search` 搜索公开音乐目录，返回实际候选与资源类型；不是付费音乐解锁器，也不保证找到主流歌曲原版。可继续用声明式 API/MCP 插件接入你的音源服务，以真实 API 返回的媒体 URL 交给现有发送链路。收到的音频可直接解码为微信语音；不需要扬声器录音。公开音频自动处理最多 15 分钟，长音频拆为至多三份上传材料，每条微信语音最长 60 秒；更长改为文件或在外部插件预处理。


## Agent 高权限边界（0.7）

「权限与服务器」可将外部 MCP / API 插件限制为指定可信用户和会话调用。内置公开搜索与网页读取仍遵循自身开关；角色预演不会获得内置服务器、账号动作或持久任务工具。配置授权不会安装插件依赖。

内置 server_status/list/read/write/exec 与 wechat_read/conversations/send_text/account_action/operation_status 由管理端提供，不需要第三方 MCP。创建文件、命令、跨会话发送和账号动作先持久领取，再执行；返回 operation_id，可查询已提交微信动作的回执，不通过重发来查状态。

## 0.8.0 内置语音与本地歌曲工具

0.8.2起，`music_local_start` 自动建立持久音乐跟进任务，返回 `automatic_followup=true`、`job_id` 和 `task_id`；模型不必另建 `agent_task`。0.8.3默认 `kind=voice` 准备整首歌曲并由微信按每条不超过60秒拆分；用户明确要文件才选择file，不主动询问文件。`duration_seconds`指定10–600秒的生成上限，默认60，由模型结合用户语义选择时长，后台校验范围和实际输出，不再用关键词覆盖模型参数。需要启用Agent持续任务，但这是原用户请求的续交，不要求主动问候权限或目标名单。后台只查询已提交job，完成后再调用 `music_local_status` 暂存附件，经过正常微信发送队列交付。等待状态可跨重启恢复；副作用未知的运行中交付仍保留核查边界。

“服务商 → 语音与唱歌”已提供百炼 TTS/ASR 和本地 YuE 配置；它们无需导入示例插件。启用且具备对应发送权限后，模型可以按需发现 `speech_synthesize`、`music_local_start`、`music_local_status`；百炼 Fun-Music 仅在开通并启用后提供 `music_generate`。普通 API/MCP 插件继续兼容。工具结果只代表生成或暂存，实际发送仍由微信队列和回执确定。

本地 YuE 可用当前会话已下载音频的 `reference_asset_id`，不会接受模型猜测的主机文件路径。生成任务 ID 持久化在当前会话的执行记录，其他会话不能据此读取文件。它是异步生成：先保存任务检查点，完成后取文件或试听语音；不会自动重新提交中断的生成任务。部署说明与证据见 [语音唱歌与上下文升级验收](research/语音唱歌与上下文升级验收.md)。


## 0.9.7 素材关联与完整复用

`media_recent` 返回当前会话的来源 URL、真实缓存 MIME、`parent_asset_id` 和最近请求的发言者/原请求/任务关联。精确引用优先，其次结合当前发言者和近期上下文；其他群的素材不会进入目录或复用范围。缓存的音轨与视频分别存储，不以音频文件冒充原视频。

从公开视频提取音轨时保留已下载的原视频父素材，后续可按父素材 ID 复用为文件，不必再搜索。`media_reuse(kind=voice)` 对同一份完整音频返回全部上传材料，按顺序放入 `media_ids`；多份连续语音材料的 `required_media_ids` 防止只选择末尾或漏段。准备完成不等于发送成功，模型选择安静仍不会被强制改为分享。旧索引若只保存最后一份语音且仍有完整音频缓存，会本地重建整组语音材料。

上述关联与缓存不改变工具权限、会话隔离、发送顺序、随机间隔和回执规则。详见 [媒体关联升级与验证](docs/media-lineage-upgrade.md)。
