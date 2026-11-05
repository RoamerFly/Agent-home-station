# 插件 ZIP 导入验收

本轮只处理安装控制面的本地包导入，未宣称其他业务插件已迁出，未更新生产程序。

## 行为

插件中心 → 导入本机插件 → 选择 ZIP 或填写服务器目录 → 检查插件 → 确认权限并安装。只检查不会安装；安装后保持未启用。ZIP 的 `plugin.json` 位于根目录或唯一顶层目录内。

上传为原始 ZIP 请求体，服务端在账号私有目录暂存，展开后复用已有目录验证及管理员确认流程。包摘要不是发布者签名，用户仍应仅导入可信来源。

导入限制：ZIP 64 MiB、展开后 256 MiB、4096 条目。任何限制超出均整包失败，无部分安装。模型和环境不放入代码包；对应依赖安装流程尚未完成。

## 离线证据

- `internal/pluginhost/archive_test.go`：根目录/单目录导入、与目录快照摘要一致、确认后默认停用、原上传不再参与执行、临时文件清理；越界路径、链接、Windows 名称及大小写别名、重复文件、文件/目录冲突、声明展开体积超限、取消、无效 ZIP、文件摘要篡改拒绝。
- `internal/admin/plugin_archive_test.go`：真实 HTTP 路由上的未登录/缺少 CSRF 拒绝、内容类型检查、坏 ZIP 拒绝、导入不安装、管理员确认安装且不启用。使用合成管理员、临时存储与项目内插件代码，不使用真实会话。
- `scripts/test_plugin_import_ui.cjs`：实际前端脚本与 CSS、模拟接口；检查原始文件上传、确认之前无安装请求、所选权限、来源变更移除旧确认、切换账号不提交旧令牌。浅深色两主题、375/1440 两种宽度无弹窗横向溢出、无脚本异常。截图仅存于忽略目录 `temp/plugin-import-ui`；已人工查看窄窗口截图。
- 初次浏览器测试因夹具未切换活动页及选择器匹配隐藏控件而失败；修正夹具后通过。不是生产接口故障，也不将初次失败写成一次通过。

本轮未进行真实模型、微信发送、第三方包下载或 Linux 隔离运行测试。Windows/Linux 编译不能代替隔离进程实机验收。

## 复现

```text
go test ./internal/pluginhost -count=1 -timeout 120s
go test ./internal/admin -run 'TestPluginArchive|TestPluginPackage' -count=1 -timeout 120s
go vet ./internal/pluginhost ./internal/admin
node --check internal/admin/static/plugin-packages.js
node --check scripts/test_plugin_import_ui.cjs
python scripts/check_repository.py
```

浏览器脚本使用现有 Playwright，通过 `PLAYWRIGHT_MODULE` 指定本地模块；`PLAYWRIGHT_CHANNEL` 默认 msedge。可选 `WECHAT_ADMIN_UI_SCREENSHOTS` 写入本地截图。不需要启动后台或提供管理员口令。
