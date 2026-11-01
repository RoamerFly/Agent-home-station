# 运行时

main.py 是严格 JSON-RPC stdio 入口，拒绝重复 JSON 键、非有限数和无账号/会话/请求者/目的/请求键的调用。music.generate 保持独立的 wechat/model_request 云端协议。

music.bridge_request 只供宿主内部调用，不在 stdio tools/list 中列出。参数与 wechat/music_bridge 回调完全一致：

```json
{"method":"POST","path":"/jobs","body_ref":{"ref":"request_0","sha256":"<64 lowercase hex>","bytes":1234},"idempotency_key":"original-operation"}
```

method 只允许 GET、POST。精确路径目录：

- GET /health、/rvc-health、/parameters、/sampling-options。
- POST /jobs、/rvc-jobs。
- GET /jobs/{id}、/jobs/{id}/audio。
- POST /jobs/{id}/cancel、/jobs/{id}/resume。

id 为 1..128 个 ASCII 字母、数字、下划线、@ 或连字符，与宿主任务 ID 契约一致。禁止 query、fragment、百分号编码、点、反斜线、重复斜线、绝对 URL、未知动作和方法混用。GET 不得携带正文或幂等键；start 必须有正文引用与非空安全幂等键；resume 必须有正文引用；cancel 可无正文。插件绝不解引用文件，不接收原始正文、歌词、base64、endpoint 或 headers。

宿主响应：

```json
{"status":200,"mime":"application/json","body_ref":{"ref":"reply_0","sha256":"<64 lowercase hex>","bytes":987},"json":{"id":"original-job","state":"running"}}
```

status、mime、body_ref 是实际传输证据。json 可省略，只能是宿主校验实际响应字节后提供的 JSON；不根据上游 MIME 是否含 json 再次拒绝，兼容旧桥的 text/plain。二进制音频不经 stdio。宿主负责字节摘要、大小、解析内容与私有任务/来源的绑定。插件保留这些原字段，非 2xx 才额外返回 normalized_error：

```json
{"kind":"input","code":"yue_recovery_restart_missing_required","provider_code":"yue_recovery_prompt_missing_explicit_restart_required","message":"固定的原任务恢复操作说明"}
```

kind 为 service 或 input；service code 使用 yue_account_scope_mismatch、yue_bridge_auth_failed、yue_backend_unavailable、yue_job_not_found、yue_request_rejected。input code 使用 rvc_recovery_failed、yue_recovery_restart_missing_required、yue_recovery_retry_failed_required、yue_recovery_failed、yue_native_parameters_invalid、rvc_service_not_ready、music_recovery_unavailable。所有 normalized_error 都含 message。分类在 Python；文案来自已索引的 runtime/error_templates.json，保持旧 UX 并提示原 job_id，不返回任意 provider 错误字符串。provider_code 仅保留实际 JSON error 中符合小写安全标识规则的值，另允许总长不超过 160 字符的 `sampling_[a-z0-9_]+:[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*` 原生参数字段错误（如 sampling_outside_native_spec:render.steps）；不接受 URL、路径、任意诊断或凭据后缀。原 JSON 中的任意诊断不能插入 message。

模板数据形状为 `{"version":1,"templates":{"rvc_recovery_failed":{"kind":"input","message":"原RVC任务恢复校验失败：{provider_code}；检查原文件、配方及声线配置，不新建重复歌曲"}}}`。当前共有 12 个固定 code，每项恰含 kind、message；唯一占位符是 `{provider_code}`。Python 及宿主均只做字面替换，不求值格式表达式。

宿主必须通过已安装包文件索引读取并验证该 JSON 的哈希，按 opaque code 查固定 kind/message 模板，核验 provider_code 与实际 JSON error 一致，并用实际安全 provider_code（无值时空串）替换后严格比较 message。分类逻辑不应再复制到 Go。宿主还必须核验原传输字段未变，再使用插件分类；不得把插件提供的引用当成已下载或已发送的音频。每个绑定调用只回调一次，失败或不确定结果不重试。相同 account/thread/requester/purpose/request_key 在同一运行时内不能再次请求，即使第一次宿主失败；恢复进程与持久幂等仍由宿主控制，不在插件伪造缓存成功。

music.service_control 是仅宿主调用的固定服务动作协议：start/status/stop 经 wechat/service_control 回调，插件无启动器、进程句柄或主机路径。状态不证明 GPU/模型就绪。
# 内部纯音乐准备

`music.prepare` 在隔离的已安装包内实现 RVC bridge 版本/声线能力协商与
请求模板、原任务恢复状态机/配方解释，以及原曲约束和 vocal style 构造。
它没有 host callback，不访问网络、音频、文件、凭据或 GPU。
RVC 模板仅包含 `$music_ref`；宿主核验选定声线、原音频 hash、范围和引用后
才填入音频。恢复结果不能更改原 job、声线或素材；取消、权限、任务排程及
成品投递仍由宿主负责。该能力不出现在模型的 `tools/list` 中。
