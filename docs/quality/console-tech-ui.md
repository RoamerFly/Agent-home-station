# 科技工作台本地验收（2026-10-31）

## 本轮变化

- 项目内安装 web-animation-design、fixing-motion-performance、aceternity-ui，设计来源与范围见 `docs/guide/console-design.md`。未引入 React/Tailwind 依赖或付费组件。
- 统一浅深色 token、导航轨、连续指标条、列表分隔、选中光边；角色设置改为完整工作区域；桌面宽度的回合筛选收为一行。
- 服务插件选择与外部接口选择合并进已有工具栏，保留顶部保存和草稿。
- 导航标记只在导航/尺寸改变时测量，浏览器过渡可中途改变目标；进场只使用短时透明度/位移，没有常驻动画循环。减少动态效果时禁用。
- 修复联网/MCP、设备连接的二级导航错误高亮。
- 视觉复查发现服务插件面板可出现在其他页面的问题，补充 active-page 限定，以及每次导航只能有一个可见页面的回归断言。
- 修复声明式密钥字段嵌套 label；深色插件标题明确继承文本颜色。
- Windows 启停画面采用墨蓝底；进度环定时器仅使环所在矩形失效，避免每帧重绘整窗；隐藏/完成时停止计时器。

## 已运行

| 验证 | 结果 |
| --- | --- |
| `scripts/test_console_ui.cjs` | 19 页 × 2 主题 × 5 尺寸，通过；额外 512×384 窗口与服务插件 3 尺寸检查通过 |
| 快速导航、减少动态效果 | 最终选中与页面一致，减少动态效果下过渡为 0 |
| 顶部保存、长文、文档/表单草稿、配置兼容 | 原有断言通过 |
| 浏览器异常 | 0 pageErrors |
| 截图 | 38 张桌面截图保存于本机忽略目录，逐页复查并修正上述问题 |
| `Build-Desktop.ps1 -OutputDirectory temp/console-tech-desktop -Test` | 55 控制器断言、86 窗口断言；8 次真实 WinForms/WebView 启停、12 个并发重复启动探测，通过 |
| 管理页面/插件 API Go 定向测试 | 通过 |
| `go vet ./internal/admin ./internal/pluginhost` | 通过 |
| 修改 JS 的 `node --check` | 通过 |
| `python scripts/check_repository.py` | findings 为空 |

浏览器测试拦截写入、使用合成会话与插件，账号配置结构只读用于兼容性断言。公开预览使用合成内容；截图和日志不进仓库。未运行付费模型测试、真实微信投递或真实 GPU 服务。

## 交付边界

新桌面测试构建：`temp/console-tech-desktop/WechatAgent.exe`，SHA256 `189e63f18f06c3c7836bdc1302fe8a12d53cf2fda4a4543e841f3d4603076f9a`。

当前更改为源码与隔离测试构建，未替换正在使用的生产程序。插件实际执行器及业务迁移仍未完成，不能将本轮界面通过视为插件化整体完成。未测量生产 WebView 的帧率，不承诺恒定 60 FPS；16 ms 为重绘定时器目标，不是帧率证据。
