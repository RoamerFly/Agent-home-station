# Windows 专用 OBS 安装

此入口使用独立的 OBS Studio 33.0.1 x64 便携副本。专用输出可使用已登记的 MP4 成品；没有选择节目时仍显示测试色板。安装成功只表示本机具备输出条件。开始直播还需要平台适配器和本次开播授权。

1. 完全退出成长站桌面程序。
2. 从 OBS 官网下载 Windows x64 ZIP。脚本会校验固定版本和 SHA-256。
3. 打开 PowerShell，在成长站目录执行下列命令。将 ZIP 和数据目录换成实际位置。
4. 重新启动成长站。登录后的直播状态接口 `/api/local/live` 会显示 `encoder: ready`。直播目标列表取决于应用授权。

~~~powershell
.\Install-StationOBS.ps1 -Archive 'D:\Downloads\OBS-Studio-33.0.1-Windows-x64.zip' -StationDataDirectory 'D:\AgentHomeStation\data\station'
~~~

从源码执行时，脚本位于 `scripts\Install-StationOBS.ps1`。安装后的运行目录在所选站点数据目录的 `managed-obs\studio`。该目录必须位于大肥鱼工作空间之外。安装脚本拒绝覆盖已有副本。需要移走旧副本时，先完全停止成长站，再将整个 `managed-obs` 移到别处。重新安装和启动后会校验新副本。

私有编码器使用自己的 Windows 桌面和节目配置。只读节目预览继续连接由你独立启动的 OBS。本机校验失败时，优先检查 ZIP 版本、目录位置和是否修改了已安装的模块。平台未登记或授权不可用时，目标列表为空。

在本机聊天里让大肥鱼使用已有作品时，她可先调用 `artifact_publish` 取得成品编号及 SHA-256，再按需发现 `app_live`。开始会话同时提供 `program_artifact_id` 与 `program_sha256`，`program_loop` 指定是否循环；原会话查询和停止只提供原编号。节目引用跟随原任务保存，不能在查询或重试中悄悄换片。播放完一遍不等于结束直播：未开启循环时媒体源按其结束行为停止呈现，直播会话仍按授权时长或明确停止收口。

宿主先验证原件、身份及来源，读取视频元数据并解码一帧。编码器使用另行核验过的私有文件，按视频比例居中显示并保留音频；家目录原文件后来被编辑不改变已选版本。来源被遗忘或家目录授权撤销后，输出检查会停止原会话。首版节目输入明确限于 MP4，不接受网络播放列表、任意 OBS 配置或媒体 URL；其他容器可先用媒体工具转码。该本地媒体源使用 [OBS 33.0.1 的官方媒体源设置](https://github.com/obsproject/obs-studio/blob/33.0.1/plugins/obs-ffmpeg/obs-ffmpeg-source.c)。生成配置、实际编码画面及平台开播是三个不同的验证阶段，部署记录分别说明。

[OBS 官方下载](https://obsproject.com/download?os=windows_x86) · [OBS 33.0.1 发布记录](https://github.com/obsproject/obs-studio/releases/tag/33.0.1)
