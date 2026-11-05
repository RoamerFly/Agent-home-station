# 深度修复质量协议与已运行证据

证据术语：本文和历史报告的 manual_audit / 逐条来源复核，均指本任务审查代理独立对照实际输出与来源，不是真人参与的验收。提供商模型自评分与该代理复核分别记录；Playwright 和截图检查同属自动化/代理视觉检查，真实手机播放、真人听感均未测。历史字段名与原始计数、gold、阈值保持不变。

## 固定协议

夹具版本 `deep-repair-v1`，目录为 `internal/companion/testdata/deep_repair/scenarios.json`。240 条由六类各八种行为与五种身份/时序置换组成；不是240段独立真实聊天。生成器 `scripts/deep_repair_fixtures.py` 不访问用户资料。两起事故使用匿名成员、合成语句与媒体描述，不复制真实图片。

确定性验收调用真实 Store、上下文选择和回复来源校验。人物与对话类检查错误来源、明确引用、跨账号/会话、助手旧话与失效锚点；媒体类检查不同上传者的观察是否进入个性化上下文；重启类检查来源、时间、引用、媒体与回复对象保存；记忆类检查来源、更正、删除、版本冲突；来源类检查命名空间、编号、发送者、时间、正文碰撞与显式修订。

这些断言是程序边界测试，不代表模型已正确理解所有240种自然语言。异步排队、原生回执、任务取消和桌面状态还需使用各模块端到端测试。

## 可重复命令

```powershell
go test ./internal/companion -run TestDeepRepair240SyntheticScenarios -count=1 -timeout 240s
go test ./internal/companion -run 'TestDeepRepair240SyntheticScenarios/(people|media|restart)' -count=5 -timeout 300s
$env:WECHAT_DEEP_REPAIR_BENCH='1'
$env:WECHAT_DEEP_REPAIR_REPORT='<private-output>/write-benchmark.json'
go test ./internal/companion -run TestDeepRepairWriteScalingReport -count=1 -v -timeout 180s
```

写入基准在同一进程、同一机器测量两个独立 SQLite 库：100 和1000条无关会话事件，然后各在固定会话依次写入100条消息，记录p50/p95。两个固定会话增长相同；比值门槛为2。该小样本实验不是宿主负载下的长期服务 SLA，不包含网络或模型延迟。

提示比较使用 `scripts/deep_repair_benchmark.py --source-root <disposable-source-copy> --output <private-output>`。基线必须由冻结的HEAD加当时未提交文件快照构造，当前源另建隔离副本；两者使用同一探针、同一配置及60条三人合成对话。探针经过生产policy与context选择，但不包含工具schema与提供商tokenizer，不能单凭该值宣称综合输入Token达标。

## 真实模型评估

`scripts/deep_repair_evaluate.py --config <private-config> --report <private-output> --limit 240` 读取明确指定的配置，仅含 `base_url/model/key_file/compatible`，密钥仍从本机原文件读取。全部会话与Store为新建合成数据，不连接真实微信或原生服务，没有发送适配器。

真实模型协议为 `deep-repair-live-v2`。每一种行为构建自己的时间线和状态：更正/遗忘执行真实Store变更，恢复案例关闭后重开数据库，来源冲突执行真实来源写入；普通社交保持social，明确问题才direct。判分器接收当时可见来源、引用、人物、素材和预期，不接收已遗忘正文。每完成一例原子写入checkpoint；用 `--resume` 继续时核验夹具、协议源码和模型配置哈希，协议变更不得混用旧结果。

评估经过生产 `Engine.Respond` 和高风险复核，记录实际输出、结构检查、必须回应场景的回应率及提供商用量。所有人物、媒体、时态和有效性rubric均通过且总体达到95%，明确问题回应率达到95%才通过。语义rubric由同一提供商模型评分，报告明确标记该限制；它不能替代真人听感、手机播放或用户最终评价。结构测试与模型评分分别报告，不能用全部静默通过。

## 已运行结果

2026-10-30，本机 Windows amd64、Go1.25.3：240条确定性suite通过（一次62.667秒）；人物/媒体/重启核心120条子场景连续5轮通过（600次子场景，79.440秒）。补充分配统计后的100/1000无关事件写入p50分别2.7866/2.7316毫秒，p95分别3.7004/3.4881毫秒，p95比值0.9426；100次固定会话写入分配分别17,418,344/17,675,304字节、43,665/44,182次，逻辑来源正文均为1,600字节（不是操作系统物理写入量），门槛2通过。真实模型的240个状态场景构造也通过无网络回归（35.810秒），该项仅验证刺激与状态准备。原始报告位于本机temp目录，不随源码提交。

冻结提示基线的三个说话者均为9444个保守估算Token（仅policy+transcript）。当前相同探针为6,267/6,360/6,267，均值6,298，相对基线均值9,444下降33.3122%；这满足该探针的30%门槛，但因不含工具schema与真实提供商用量，综合输入及真实模型质量仍以对应报告为准。


## 完整初次模型请求比较（实际目录与工作区）

`deep_repair_full_request_probe.py` 在冻结基线与当前源的隔离副本中运行同一个管理员合成夹具，经过真实 permittedTools 发现、AgentContext、Engine、AgentRunner，捕获首次 ChatFunctions 的完整 system、turns 和工具 JSON。共10类普通social与10类长历史综合请求，长历史混合定向群聊、social、持久任务续办与私聊；每例都是新的合成账号状态，模型只捕获请求后返回silent，不执行工具、不调用提供商、不发送微信。

固定v2探针在Windows 11本机得到：普通social平均24,119.7→14,699.7估算Token，下降39.05%；长历史综合平均26,497.9→18,479.4，下降30.26%；全部20例平均25,308.8→16,589.55，下降34.45%。普通30%与综合20%门槛均通过。报告保留system、历史、工作区、当前消息和工具JSON分项及实际工具数量。这是保守估算的完整请求对比，提供商账单与实际生成质量另报告。

WAL文件长度和逻辑正文大小无法证明操作系统缓存下的物理磁盘写入量；本轮未采集ETW/存储设备物理IO，因此不把它们标为物理写入。已有写入延迟和进程分配统计仍有效。


## 实际语义评估中的未通过项

第一批真实模型人物专项40例（协议v2）由主任务运行，提供商rubric通过33例、明确问题回应30/30，未达到95%语义门槛。审查代理逐条来源比对还发现rubric漏报：第三人转述偏好被称为当前发言者的偏好，以及根据通话结束编出房间安静和耳内嗡鸣；另有一个中性询问可能被rubric误判。实际输出与来源保留在本机合成报告和来源审计中。这个结果用于继续修复，不算最终质量通过；同提供商判分不能作为唯一验收依据。


## 协议v3与同群文字来源校验

程序校验允许引用同群另一成员的已核实纯文字作为“关于该作者”的证据；这不允许把来源作者改成当前说话者。确定性回归同时验证source author/version保留、回复Target不漂移、`cross_person_source`风险标记存在。跨账号、跨群、助手无据旧话以及无引用的跨人媒体仍然拒绝。

真实模型协议v3只校准已观察到的判分歧义：中性问句并非事实断言、清楚的转述指代不强制输出内部ID；“你”错绑当前说话者、补造房间或耳鸣等细节仍判错。场景、预期事实和95%门槛未降低。v2报告和来源审计保留，v3使用独立报告与协议哈希，不混合计算通过率。

## 回复依据与关系记忆界面合成验收

2026-10-31运行 `scripts/deep_repair_ui.cjs`。脚本使用真实index.html、CSS、dialogs/logs/memory/save-actions组件，在Playwright内拦截所有请求并返回安全合成资料；不启动Web/原生服务，不读取真实配置、登录或聊天，不调用提供商，不发送微信。该项验证组件行为，不代表真实服务集成或用户手机验收。

1440×900与375×812两种宽度、浅色与深色共4组通过，16张截图保存于本机 `temp/deep-repair-ui/`。执行回合入口保留触发消息编号；回复依据显示目标成员和可打开的原文及媒体来源；来源页显示受话者、原文版本和媒体上传者；关系条目展示关系两端及更正状态。顶部保存实际提交审计保留天数，更正请求携带expected_revision和精确来源原句。4组均无横向溢出、无浏览器pageerror。代理视觉查看8张关键截图，检查桌面等宽双列、手机单列和明暗文字/边界；截图关闭过渡动画以避免采到动画中间帧。此任务修复了遗漏的关系对象、不可点开的媒体参考消息及未知媒体观察状态；设置表单使用局部class改为桌面双列，不改变全局保存位置。

```powershell
$env:PLAYWRIGHT_MODULE='<installed-playwright-module>'
$env:PLAYWRIGHT_CHANNEL='msedge'
node scripts/deep_repair_ui.cjs
```

报告和截图只在temp内保留，安全合成测试脚本随源码保存。该验收没有覆盖全部已有页面、Windows桌面生命周期或真实发送链路，这些分别由其专项验收负责。

## 协议v4：逐条回复对象与生产源码冻结

v3人物40例中出现了rubric漏判：回复把没有参与视频的目标成员说成刚结束视频。v4不改刺激、must_respond或95%阈值，改为逐条保存实际Decision.ReplyContexts并交给判分器；“你”以该气泡target_ids解释，不能把入站话语的addressees当成AI回复的受话者。中性问句仍可通过，但其预设的事件参与者必须有来源。

评估入口在调用模型前使用 `go list -deps -json` 对companion、agenthost、deepseek、mcptools及仓库内编译依赖所选择的生产文件与嵌入资产生成SHA-256清单，包含go.mod/go.sum；报告记录production_sha256、哈希方法与无密钥model_config。完整文件清单保留在报告旁的source-manifest.json。checkpoint必须同时匹配生产、协议、夹具、配置/选择哈希才能续跑。不同源或协议使用新报告，不混合通过率。

确定性fixture先经过生产normalize补齐verified状态、ContextVersion指纹及SourceVersions，再直接Validate；未经核验的原始binding与缺少版本的binding必须被拒绝。原有同群纯文字可作为关于其作者的证据这一语义保持不变。

v4严格binding版本的240条程序回归实际通过（2026-10-31，28.105秒）；真实模型入口无配置时compile/skip通过（0.072秒），后者不算模型语义验收。媒体fixture明确保存触发消息的稳定Source和一致原时间，避免以不完整的合成事件绕过生产proof要求。

## v4媒体fixture边界与v5修正

只读复查发现v4媒体fixture把观察文本放入未指定OriginalText的Input，且failed-observation仍标verified、video-range缺少ObservedRange、late-observation只是顺序写入。v4结果保留用于模型错误排查，不能宣称已通过媒体原文/机器观察分离、失败状态、结构化观察范围或真实乱序回写链路。

v5修正fixture真实性，保持问题文字、预期和mustRespond不变：媒体Source.MessageType按实际image=3、voice=34、video=43，纯附件OriginalText明确为空，观察单独保存在IncomingMedia.Observation；通过真实Store.ReadEvent核对原文未变成观察。失败图标为failed；视频ObservedRange明确0s-2s及余下30s未读。乱序场景先保存两条pending原消息，后上传素材先完成，再更新原上传素材，使用实际UpdateObservation和before条件；验证每条trace/uploader/asset/观察版本仍对应原素材，旧before不能再次覆盖。完成后经真实normalize/Validate校验可引用原素材且不能将另一成员素材代入。v5使用独立协议hash和新报告；旧batch不合并为最终通过率。

v4首120例（人物/媒体/多人对话各40）完成实施代理逐条来源复核，提供商rubric分别38/40、39/40、40/40。额外发现rubric漏报：旧图上传者被说成摄影者、未读视频预设车辆转弯、UTC日期与当地日期混淆，以及存在歧义的父亲代词重绑定。还有一例文字已正确承认AI旧话无依据，但引用assistant事件作为事实proof被程序拒绝，导致明确请求未获得结果。复核没有覆盖改写原评分，也不把该分数当成零错人物/素材或最终95%通过；证据仅保留在本机合成审计temp/deep-repair-v4-readonly-audit.json，按旧production hash归档。v5最终fixture已通过strict240+全240场景无网络构造（53.688秒）；真实模型最终结果单独记录。

## 独立固定草稿verifier实验

`deep_repair_verifier_live_test.go`与`deep_repair_verifier_evaluate.py`建立独立专项，不修改240场景或其门槛。冻结9组坏/好草稿，覆盖松快、缓过来、家常话题预设、上传者变摄影者、观察变OCR、无据近景、失败变空白、转述者变亲历者、同日变昨晚；另加3条普通自然控制，共9条坏稿和12条好稿。

只调用生产只读`checkReplyGrounding`，不调用editor或重新生成来碰运气。坏稿必须全部明确reject，uncertain和服务错误不算成功；正确控制allow至少95%，不能全部拒绝或silent通过。每项保存固定原稿、输入/来源、binding、实际candidate verdict及violations、模型原始答复、用量和草稿未被改动的检查。source、协议、刺激、模型配置和driver均保留hash并用于checkpoint续跑；密钥及key_file不入报告。当前准备与编译检查已通过，真实结果以本机专项报告为准，不与v5生成质量分数合并。

```powershell
python scripts/deep_repair_verifier_evaluate.py --config temp/deep-repair-model-config.json --report temp/deep-repair-verifier-live-v1.json
```

首次真实只读verifier专项完成21例（28.09秒）：坏稿9/9明确拒绝，好稿8/12允许（66.7%），未达到95%控制门槛，测试真实失败。四项误拒包括正常条件邀请、开放式询问视频话题、正确转述乙的经历及普通“你好”。判分理由还出现“原文明确、无缺陷”却返回reject的矛盾。本轮没有改变正确控制、坏稿或门槛，不能将更保守或全拒绝当作成功。

该批生产hash为ea94948e7af5666f51c89fc6bf6ae14b61876a11201f89e4b2b06f4fe96bf8ff，全部21次提供商报告用量，input=45,416、output=3,080、cache_hit=10,240（缓存属于输入分项，不另外相加），purpose为reply_grounding_check。报告与源码清单在本机temp/deep-repair-verifier-live-v1.*保留，后续修复使用新生产hash和新报告，不覆盖此次失败证据。

第二批相同21条固定草稿（相同protocol/fixture，仅生产checker修正）坏稿仍9/9拒绝，好稿11/12允许（91.7%），仍未达95%并真实失败。剩余误拒为“你刚才说过不喝咖啡”：本人约10秒前的原话和时间戳可支持此元陈述，checker却要求原话自身出现“刚才说过”。该批生产hash a7a369f3c229c9609217bd4c07570e943886683d0ae53513777862e2a47bc70a，提供商input=51,765/output=2,613/cache_hit=15,360；证据位于temp/deep-repair-verifier-live-v2.*，控制与门槛保持冻结。

第三批在新生产hash bc4190b80f11bbfe34d0c2aa3d7fcb4c74fd93aa52139b2c2d007085fc617cda上运行完全相同的21条固定草稿，实际通过：9/9坏稿明确reject，12/12正确控制allow，全部21条候选在调用前后的完整JSON未被更改。真实测试28.07秒，Go进程29.091秒，提供商input=56,830/output=2,266/cache_hit=18,560，共21次reply_grounding_check。前三批fixture/protocol及95%门槛相同，变化仅在生产实现；v1/v2失败证据保留。

这说明固定对抗草稿的只读checker达到该专项门槛，不能替代完整240场景的生成、一次编辑后终审、工具执行或真实微信验收，也不撤销旧v5整批manual critical非0的结论。新完整生成批次仍须独立达到95%和关键来源复核零错误。

## 最后来源版本的专项与放行口径

补齐真实当前消息Source的deterministic240回归通过（33.365秒），未改变missing-anchor或其他expected。生产修复后的39d9043ae740a32338a80e7b83cdc122c9d88d91874b3bef04962130da22d596版本上，相同固定21条专项再次通过（9坏拒绝/12好允许，24.06秒）。新增独立工具状态8条也通过（4坏拒绝/4好允许，10.20秒）：使用生产prepared request配对读取，但回执与工具结果全为合成，不执行工具，不证明真实GPU推理或微信投递成功。

工具专项包含queued/generated/accepted正确控制、queued误称生成、工具succeeded误称微信接受、默认实际声线误称mom、微信接受与手机播放的正负控制。微信回执使用native ToolStatus的state及voice_chunks，不把speech_synthesize的accepted当微信回执；期望声线来自请求，不能被候选模型自报默认声线覆盖。每例保留实际prepared证据、合成result、原稿、binding、verdict和用量。工具8提供商input=18,662/output=906/cache_hit=9,728，固定21 input=62,846/output=2,403/cache_hit=21,504。

最终UI合成检查再次通过4组布局，额外验证checked、repaired_and_checked及核验失败中文状态映射。结果在temp/deep-repair-ui-final。

此前临时审计的manual_critical_cases字段统计了所有confirmed unsupported唯一案例，需与原批准表区分：人物或素材归属关键错误必须0；其他确认的心理、视觉或时间错误影响整体生成至少95%的质量指标；模糊项单列，不把修辞或普通视觉细节称作错误素材归属，不改刺激与阈值。

39d9043来源的六组共240条提供商通过232，明确请求215/220回应。前120条来源复核另确认1项关键人物错绑（转述者被“你们聊”纳入视频参与者），及2项非关键无据视觉内容（非穷尽摘要变为画面只有一把伞、观察失败变空白图）。保留提供商原8项失败再额外扣3项漏判，保守来源复核计229/240；另列caption“去年/今天拍”与未知captured_at的置信冲突，未悄悄改变case或评分。该来源关键错误非0，因此仍不可本机放行。证据temp/deep-repair-accepted-manual-audit.json，实际用户接收和播放均未进行。

## 有限复数绑定后的再次验收

4607c6fde68b4b1f57ba692844cd838f6712492cebe161b046a760d33dd7f7f9版本上，原21、原工具8、独立复数2三项均实际通过（分别22.72、8.99、2.45秒）。复数专项不修改原21/240刺激，坏稿“那你们聊得怎么样？”被拒绝，合法“你说的是乙与父亲刚结束的那通视频”允许。提供商输入/输出/缓存输入分别为67,342/2,419/24,192；20,453/895/10,752；6,019/229/2,304，均保留独立报告，不把这些fixed checker控制当作完整生成通过。

同版本release六组提供商237/240，明确请求217/220回应。前三组120条实施代理来源复核发现：引用动作归错原话作者、上传者被称摄影者，以及空用户原文中的机器观察被称作用户说过；这些是关键来源归属错误，不能归入单纯修辞或拍摄年份置信问题。另有短通话时长与伞开合/蓝色等无据一般细节，作为非关键错误计入总质量。使用自动失败与审查代理逐条确认case ID并集计算为229/240；尽管超过95%，关键归属零错误仍未达到，所以不放行。

固定字段审计在temp/deep-repair-release-manual-audit.json：production_sha256、reviewed_case_ids、confirmed_error_case_ids、key_wrong_person_case_ids、key_wrong_media_attribution_case_ids、ambiguous、findings和逐例来源证据。安全停止计入有效回应/总质量失败，不算已发送的错误归属；双昵称歧义、ASR内容传达与声纹主体的区别保留单独裁决，不悄悄改刺激或分数。

## v6：真实用户与运行时资料的元数据形态

原生输入的真实用户正文现在显式带OriginalText指针（文字为原字面值，纯媒体为已确认的空原文），并与账号、会话、发送者、MessageID=TraceID、服务器编号及原时间一致。v6只同步合成fixture的这一真实形态，问题文字、expected和mustRespond均未改变；媒体观察继续独立，不以运行时任务目标或程序wrapper伪造用户消息。身份碰撞/修订场景在修改真实正文时也同步字面原文指针，避免旧指针把新正文错误解释为观察变化。

独立固定21/复数2/tool8真实人输入同步同样元数据，旧模型报告保留。新报告使用deep-repair-live-v6及新protocol/source哈希；不能将旧v5与v6报告拼接计算通过率。后续须在source/context/relation均冻结后完成构造测试并重新验证模型，此段不声明尚未运行的结果已通过。
