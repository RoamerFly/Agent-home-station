# 全功能与插件可用性验收（2026-11-02）

## 实施边界

用户授权本轮在两个指定机器人测试群及其已登记主人私聊验证。接收方通过原生精确群名和既有 owner/direct 配置绑定核对，原生 ID 与原始回执仅保存在本机 ignored temp。仅使用合成媒体和中性测试文字，没有向其他会话发送测试消息。

继续遵守零新增付费模型测试：无 Pro、云视觉、云听音、云 TTS、联网模型测试调用；真实本地模型允许使用既有资源。没有训练、真实设备操作、安装或扩大插件权限，没有恢复历史未知任务。

## 实际微信投递

每个授权会话各验证文字、图片、短提示音语音、2 秒合成视频、UTF-8 文件、引用，共 18 个独立不可变幂等键。全部取得 accepted 和服务器消息 ID，并从对应原生会话唯一核对到 outgoing 消息；原生类型分别为 1、3、34、43、49/6、49/57。未知结果仅按原 key 查询，不生成替代 key。未撤回或改动既有消息。

另外，真实本地指定声线生成的中性短句，经独立本地 ASR 核对文本无额外参考文案后，向三个授权会话各发送一次，均 accepted 并核对为 type34；再将本轮实际 RVC 换声短句和 10 秒本地音乐预览，分别向三个授权会话各发送一次（语音/文件），全部 accepted。总计 27 次投递、27 个独立幂等键，每个会话 9 次。没有重发历史 mom 成品。

这是服务器接受与原生记录核对，不代表手机实际下载、播放、听感或用户阅读已确认。机器人自行发送的测试消息也不等于外部用户入站、真实聊天模型决策或总结质量验收。

## 插件状态与实测

| 模块 | 本轮实际证据 | 准确边界 |
|---|---|---|
| 插件宿主 | 7 个既有启用插件均通过正式 runtime/start，最终 running | 未将已停用 external-tools 自动启用 |
| 媒体理解 | 官方包/Controller/LPAC/host transcription/真实 SenseVoice ONNX CPU+VAD，单合成短句实际识别一致，临时任务 completed，26.55 秒 | 云视觉/云听音未调用；两个独立合成样本（SAPI 与本地命名声线产物）通过；不代表广泛准确率 |
| 分享读取 | 上轮实际失败响应通过修复后 Go/Python 双解析及结果核验重放；本轮来源已核验视频完整 1,406,115 B，21.617s H264/AAC，生产采样 6 帧及音轨提取通过 | 本机已升级至 0.1.7 并通过实际宿主启动；无模型观看质量声称 |
| 本地说话声线 | 真实登记资源与正式 RunMusicIsolated/Windows helper/LPAC，单次短句推理 86 秒；PCM16/32kHz/5.480s/350764B，四件套与 text hash 核对，无默认声线替换 | 非云 TTS；模型 runner 隔离实测，完整来源权限合同另有合成测试 |
| 音乐/RVC | 正式按需启动；实际 RVC 分离/转换/混回 1 次，固定非默认声线、GPU 与源 hash，92.71 秒；临时 LPAC Comfy 单任务 10 秒音乐生成 1 次，95.20 秒，只有一次 prompt 提交；两者均有效非静音 PCM | 音乐是明确的 2 步功能预览；未验证人耳音色、歌词质量；未训练或恢复旧任务 |
| 项目开发 | 已安装包运行，真实 LPAC 合成工作区读写和 CPU 命令测试通过 | 不修改用户项目作为测试 |
| 手机控制 | 包与 planner 运行；旧 profile/设备/允许动作及任务仍在。已将既有 SDK r33.0.1 的公开文件校验并 stage 到项目受信目录；准备 ADB、持久 profile 和账号 storage 的来源 hash 条件迁移 | 正常停止后已发布受信工具及持久 profile/storage，通过 Store.UpdateHostState CAS 仅更新权威 settings。在线 broker 与实际设备列表查询通过，1 个登记设备为 offline，不宣称实机控制通过 |
| 联网检索 | 包运行、配置存在，公共读取/发现/模型请求绑定合成测试通过 | 不调用真实付费搜索；Surf 发现取决于既有授权 session |
| 外部工具连接 | HTTP/job/poll/MCP 四种 LPAC 合成协议通过 | 插件和保留连接均停用，MCP 声明 0；保持管理员选择 |

8 个既有插件的安装文件均符合各自清单；本地 ASR SDK 1,357 文件及 VAD、四个 RVC profile 与 8 个公有权重、五个私有说话声线 20 文件的大小和摘要核验通过。没有复制或下载模型环境。

## 本轮代码修复

公共声线目录保存着旧 Linux `/mnt/e/...` 绝对路径，虽然对应 Windows 文件完整，文件 API 仍返回 404。统一优先解析已登记 relative_path，按当前公共根查找；同时覆盖文件下载、voice_library_prepare、命名声线文件核验/加载。相对路径不接受 ../、绝对路径、UNC 或 ADS，受控文件句柄拒绝链接；公共下载核对大小与 SHA 后使用同一文件句柄及固定字节区间提供 Range。真实 catalogue、账号私有资源范围及权限不被改写。修复后的临时 Manager/认证 HTTP 接口也已只读验收当前真实 8 个公共权重：全部大小/SHA 相符、Range 返回 206，真实 catalogue 未变。

新增合成回归验证旧 Linux 路径与 Windows 相对成员、越界/错误大小/摘要/内容变化/未登记文件，以及原私有隔离路径。当前 Windows 不允许测试进程创建 symlink，相关创建反例明确 skip，生产 OpenRegular 拒绝链接的边界保留。手机可用性还增加安全枚举提示，区分“已有连接配置需要迁移”和“插件未启用”，不向界面返回路径、设备标识或凭据。

## 完整验证

- `go test ./... -count=1 -timeout 1200s` 通过；admin 517.313 秒。
- `go vet ./...` 通过。
- 公共声线下载/prepare/named/私有隔离定向回归通过，7.329 秒；真实 8 文件只读 Range 验收通过，1.61 秒；手机诊断增量定向回归通过，20.556 秒。
- 四类真实 LPAC 合成 music/named-speech/mobile/project 协议通过，13.612 秒；其余 media/share/web/external 定向 Go 23 项通过、Python 93 项通过。
- Windows 桌面 55 个 controller、86 个 window lifecycle 断言通过；8 个真实 WinForms/WebView 退出周期、12 个并发单例探测，均使用隔离合成服务及私有 desktop，不影响实际用户任务栏。
- 静态 JavaScript、仓库安全检查、Windows/Linux 构建在提交前后执行；结果以本轮终端记录与最终交付说明为准。

## 本机启用状态（已更新）

用户正常停止后，已启用干净提交 `949d879f6805c7948d7dfce681385b4ae2e847e9` 构建的 Windows 管理程序；实际部署 SHA256 为 `4e945fb0a616258f43e249b32d78754a3c0b68d858699258deca19ed131e22bd`。share-reader 已升级 0.1.7、保留原权限与启用状态。程序/插件更新阶段核对配置与密钥记录、插件 jobs、原生二进制与登录 registry 不变；未重提历史未知任务。

设备迁移使用唯一权威 plugin-host SQLite `device-state-v1`，不写历史 settings.json。以原 revision 和 settings SHA 为条件，核对完整设置仅变更 profile/storage 路径，设备、动作、限制和其他桥接参数均保持；通过正式 Store API 把 settings 从 revision 1 CAS 更新到 2，其他 host records（含任务）逐字节不变。原 profile 仅替换已登记 ADB 路径；旧 profile 与 storage 原样保留为迁移来源，不新建额外备份。151 个桥接存储文件、3,910,617 B 发布前后清单一致，目标 profile/storage 位于本账号持久目录；真实目标的最终 Inspect 通过。

已重新启动现有桌面应用，快捷方式显式绑定原配置工作区。微信 authenticated=true、connection=online；Web 健康正常，内嵌插件中心可用。7 个原已启用插件全部通过更新后的正式 runtime/start 并处于 running；外部连接保持停用。实际在线公共声线文件接口对 8 个权重全部返回 206，原 404 已消除。手机 native_broker_registered=true，实际 devices.list 成功，当前登记 1 个设备但状态 offline，在线设备数 0。

本次启用阶段未新增付费模型调用，未重复发送前一阶段的 27 条验收消息。手机实机操作、云模型语义质量及手机播放仍按上述边界报告。本轮没有 CI、Release 或 Docker 镜像发布。
