# 媒体理解协议与模型代理验收

2026-10-31，Windows amd64，Go 1.25.3；Python 协议测试不请求网络，隔离进程使用项目内 Windows Python。模型响应全部由测试策略提供，没有使用真实密钥、模型余额或微信消息。

## 已实现

`media-understanding` 0.1.1 新增独立 stdio 图片/视频帧观察协议。请求只包含受控素材引用、提示与明确参数，不传入宿主路径。模型来自配置，工具参数不能擅自切换型号。支持现有 DeepSeek 思考协议；兼容接口不能默认视为支持同一协议，不支持时明确失败。

返回观察仅对应提供的图片，不声称听过音轨或看完视频；模型截断标记 partial。实际 Token、缓存 Token 与未知用量分开，不输出隐藏推理或将工具调用当作观察。输出额度和图片数量不以原默认值静默截断。

宿主新增独立模型路由权限、管理员配置目的地址、仅宿主持有的认证密钥，以及必须提供的来源/素材绑定器。代理在展开前后验证配置与权限，拒绝重复 JSON 字段和调用参数中的目标/认证覆盖；网络权限不能越过模型权限，模型权限也不是普通联网授权。

## 实际验证

- 7 组 Python 合成回归：受控引用、明确输出/时间参数、12 张图像而非默认数量截断、思考开关与强度、来源字段与参数拒绝、模型截断、用量未知、缓存 Token、隐藏推理/工具调用排除和完整 stdio 往返。
- Go 路由回归：非法配置接口拒绝、宿主持有的密钥不进入初始化、Bearer 前缀、模型与网络权限隔离、旧配置版本拒绝、双重/未声明目的地拒绝和重复 JSON 拒绝。
- HTTP 合成传输：宿主展开的 2 MiB 模型请求完整传送，普通接口仍拒绝超过 1 MiB；沿用不跟随凭据重定向、不重复 POST、错误正文不泄漏的回归。
- 真实 Windows LPAC 运行实际插件代码，模型请求由合成绑定器处理；验证指定模型、配置密钥、图片引用替换、同键不重放、伪造来源拒绝和调用后撤权拒绝结果。原分享和已安装控制器的隔离测试同时通过。

合成绑定器只替换测试引用为虚构图片字节，**不能证明生产素材校验、实际图片解码或模型识别质量通过**。模型响应是模拟的，费用为测试统计语义，未产生真实模型账单。

## 复现

```powershell
python -B -m unittest discover -s plugins/media-understanding/tests
go test ./internal/pluginhost ./internal/pluginruntime ./internal/mcptools -count=1 -timeout 90s
$env:WECHAT_PLUGIN_TEST_PYTHON=(Resolve-Path dist/windows-native/runtimes/windows-python-cli/python.exe).Path
go test ./internal/pluginruntime -run 'TestLiveMediaPlugin|TestLiveShareParser|TestLiveInstalledController' -v -count=1 -timeout 90s
go run ./cmd/wechat-plugin verify plugins/media-understanding
node --check internal/admin/static/plugin-packages.js
```

正式包仍不可安装，仅临时测试副本提升观察能力状态。12 个包文件包含在摘要清单中。本轮没有将插件装到真实账号，未替换 EXE 或修改登录、聊天、模型和声线数据。

完整 `go test ./... -count=1 -timeout 300s`、`go vet ./...`、Windows/Linux amd64 管理程序构建及相关静态 JavaScript 语法检查通过。临时日志与构建位于 `temp/plugin-media-proxy-validation`，不提交运行产物。

## 包摘要的跨平台修复

提交前对比实际 Git 暂存内容发现：旧清单曾按工作目录的 CRLF 计算摘要，但 `.gitattributes` 会把包内文本入库为 LF。本机验证不能证明重新克隆后的包可验证。已统一九个官方包的文本换行并重算清单，不改变私有安装快照或自动升级已安装插件。

`scripts/check_repository.py` 现在直接核对 Git 索引中每个包文件的摘要、缺失与未登记文件，不读取工作目录内容代替提交内容。新增 4 项合成回归覆盖正确摘要、CRLF/LF 不一致、缺失/额外文件和无效清单。九个包的索引检查及相关 Go 包测试通过；私有值扫描 0 项发现。此修复不改变各插件的可安装状态，也不表示尚未迁移的服务已实现。

## 下一阶段

接入真实来源/素材绑定、模型用量入账、图片/表情/分享图像与视频帧入口，迁移视频解码、ASR 与本地识别运行时，再执行配置预演及旧代码删除。当前通道和独立协议不是完整媒体插件或轻主体交付；性能、最终界面和生产迁移尚未验收。
