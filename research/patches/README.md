> 当前完整集成补丁为 `native-client-local-integration.patch`，含下述语音修复与后续分享/引用适配；与旧单独补丁二选一应用。来源及哈希见 `components/native-client/source-manifest.json`。

# 原生语音播放补丁

基座 https://github.com/thisnick/agent-wechat ，固定提交 `7095ef01e0cd3d5b567bcb6970f379dcd88bbfa8`。在该仓库根目录应用 `native-voice-playback-fix.patch` 后，进入 `packages/native-client-rust` 使用 Cargo 构建 `wx-native`。补丁修改原生协议源码与合成测试向量，不包含账号、会话或密钥。

`UploadVoiceRequest` 的字段 11 是 CancelFlag，字段 13 是 VoiceFormat。原实现写入 4、0；修正为 0、4，使实际 SILK 数据使用正确的格式标记。响应中非零 CancelFlag 不再判定成功。参考字段定义来自公开 wechatdll 仓库 `Cilent/mm/mm.pb.go` 的 UploadVoiceRequest/Response。上传块同时保守限制为 16 KiB；单独缩小上传块未解决本次播放故障，不能归因于块大小。

原始播放修正时 136 项原生协议测试通过，用户确认修正后的 15 秒提示音和 40 秒音乐可完整播放。整首 Happy Alley 的第二段当时出现一次连接中断，未算作整首播放验收通过。历史证据见 `research/evidence/native-voice-playback-diagnosis.json` 与 `song-voice-060-format-fixed.json`。

目标1的累计补丁另外包含：首段及后续语音均按录音时长等待（时长加 0.8～2 秒，扣除准备已消耗时间）、账号范围持久化发送节奏、等待期间释放账号锁、逐段提交/接受时间、提交前握手最多三次尝试、一条语音共用一次握手取得的短链票据（每个 HTTP 请求仍使用新的随机早期握手数据）、最终语音响应等待上限 90 秒。不含结束标记的数据块可在同一消息ID、偏移和内容下最多尝试三次，间隔3、6秒；最终块未知结果不自动重放。异常只保存固定失败阶段、固定网络错误类别与有界上传位置，避免记录原始请求或凭据。

累计协议测试增至138项（含非最终块恢复与最终块不重试的边界测试）。两次受控 Happy Alley 复测曾出现首个上传块响应超时；长音乐四段测试中前三段接受、第四段超时，证据保留在 `goal1-voice-full-pacing.json`，这些失败不足以认定微信限速。最终版本的完整 Happy Alley 两段各40.124秒均获得微信接受回执、段间42.188秒，见 `goal1-voice-prefix-recovery.json`。该次成功实测的前缀重试次数为0，因此不声称真实网络重试后恢复已由这次实测证明。新的两段手机播放未在本轮验证。

本机原生服务和 `dist/linux-amd64/wx-linux-native` 已使用补丁后的程序；旧程序保存在 WSL 私有 bin 目录的哈希命名备份中。构建复用了既有的本机研究编译环境，没有生成或导出新的 Docker 镜像。
