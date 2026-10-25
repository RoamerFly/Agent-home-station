# 闭源微信协议镜像审计交付

网页版交付已增加：`Dockerfile.web`、`compose.web.yaml`、`cmd/wechat-admin` 和内嵌管理界面。见 [网页部署](../DEPLOY-WEB.md)、[Ubuntu 容器检查](evidence/web-container-validation.json)；导出包位于根目录 `dist/releases/`，镜像不携带运行卷或本机账号凭据。

后续机器人阶段：已经通过真实 DeepSeek 自动回复、文件传输助手触发、白名单测试群只对真实 @ 回复，并由用户确认。见 [机器人说明](../BOT.md)、[脱敏验收](evidence/bot-deepseek-acceptance.json) 和 [Docker 启动检查](evidence/bot-docker-smoke.json)。

**2026-10-24 后续进展：第一阶段后台文字发送已真实通过。** 当前使用固定公开源码构建的 Linux 原生客户端，不运行原闭源主程序或官方桌面程序；测试小号的文件传输助手已收到“后台发送测试”，用户已确认。入口见 [运行说明](../README.md)、[验收核对](acceptance-status.md)、[脱敏真实回执](evidence/linux-native-acceptance.json)、[构建哈希](evidence/linux-native-build.json) 和 [Docker 本地检查](evidence/linux-stack-smoke.json)。下文关于 iPad 尚未完成发送的描述保留为该路线的历史状态。

闭源镜像审计先读 [最终报告](closed-protocol-final-2026-10-23.md)，其结论以报告所列时间和检查范围为准。之后的独立实现已有额外证据：RQTX 原库曾在无网络隔离环境做计算向量对照；自写 Go 登录程序已取得真实二维码及扫码确认，授权返回 -106。完整原闭源主程序未执行；自写协议尚未完成真实发送。实时开发状态见根目录 README，脱敏登录记录见 `evidence/login-live-attempt-20261024.json`。

## 证据索引

后续登录恢复新增：`evidence/login-go-struct-layouts.json`（静态类型布局和描述符
交叉验证）、`evidence/device-v8-*`（设备资料依赖的定点反编译）、
`auth-field-audit-20261024.md`（授权字段映射及未解决算法）。这些属于开发中的恢复
记录，不替代先前镜像审计的时间范围，也不表示登录或发送已经成功。
后续实现与互通：`sae08-reconstruction.md`、`v08-primitives.md`、`running-v8.md`、
`spam-v8-reconstruction.md`、`ipad-auth-profile.md` 记录源码重建；
`evidence/fingerprint-live-20261024.json` 记录真实设备令牌成功，
`evidence/login-qr3-attempt.json` 记录随后二维码未扫描便过期。之后 login-4 已完成全部手机确认，
携带恢复后的设备附加信息和真实令牌提交一次授权，仍未成功；
`evidence/login-fullproof-rejection-20261024.json` 保存脱敏的“应用版本过低”响应，
`version-upgrade-evidence.md` 与 `evidence/version-update/profile-comparison.json` 记录新镜像核对及其限制。

| 文件 | 用途及限制 |
| --- | --- |
| `evidence/manifest.json`、`image-config.json` | 冻结的镜像元数据；manifest 文件是工具包装，Raw 为原始 manifest Base64，OCIManifest 为解析值 |
| `evidence/layer-verification.json` | 原始 manifest、config、14 层及四个提取文件的校验结果 |
| `samples/main.elf.bin`、`samples/libv08.so.bin` | 本次审计的二进制字节副本；仅更改文件名，哈希不变；不是可运行交付件 |
| `evidence/rea-go-evidence.json` | REA 原始 Go 元数据记录；Evidence ID 见最终报告 |
| `evidence/rea-session-bundle.json` | 空会话导出，不用于支持目标行为 |
| `evidence/entrypoint.sh`、`swagger.json` | 提取的启动脚本及接口声明；不要执行脚本 |
| `evidence/main-functions.json` | Go pclntab 函数地址表；29,150 条，含运行库和依赖 |
| `evidence/main-selected.tsv`、`main-followup.tsv`、`main-uuid.tsv` | 三批定点分析的入口地址及原始函数名 |
| `evidence/main-selected-disassembly.txt` | objdump 指令及直接调用注释；BYTES 字段只是原始相邻字节候选，不是精确 Go 字面量 |
| `evidence/main-exact-literals.json` | 按明确指针和长度重新读取的字符串，及函数指针链 |
| `evidence/uuid-direct-callers.json` | 经 enclosing-function objdump 确认的 UUID 函数直接调用点；不证明所有间接调用或动态可达性 |
| `evidence/main-ghidra-selected.txt`、`main-ghidra-followup.txt`、`main-ghidra-uuid.txt` | 独立 Ghidra 近似伪代码，不是原始源码或 REA Evidence |
| `evidence/main-ghidra-*.log`、`main-followup.log`、`main-uuid.log` | 分析日志；主程序全自动分析达到 240 秒限制，其后定点脚本导出成功 |
| `evidence/libv08-*` | 库的符号、字符串、反汇编、伪代码、哈希及日志；详见 native-library-audit.md |
| `evidence/admin-robot_manage.go` | hp0912/wechat-robot-admin-backend 提交 edcc5c42d0b089832593af2079a03bc6b0800dc2 的容器创建源码 |
| `evidence/openchat-server-compose.yml` | openchat-wx 提交 319fa8769fc871a303c423232d064effce70eb5c 的服务器部署配置 |
| `evidence/SHA256SUMS.txt` | 最终证据、样本、脚本、报告的内容校验表，不是发布者签名 |

`main-rqtx-disassembly.txt`、`config-audit.json`、`admin-container-config.json` 作为补充记录保留。所有“未发现”的结论均限于声明的检查范围。

## 复核方式

原始压缩层和逐层目录清单保留在 Windows `C:\Temp\wechat-bot-research-20261023\wechat-ipad-static`。分析脚本使用这些明确本地路径；如迁移样本目录，先修改脚本路径。样本副本在本目录 samples 中，便于原始临时目录失效后继续进行二进制阅读。

在现有 WSL Ubuntu-New 下，以下命令只运行分析工具，不运行目标：

```powershell
wsl -d Ubuntu-New -- python3 /mnt/e/A-Importance/wechat-bot/research/scripts/verify_artifacts.py
wsl -d Ubuntu-New -- python3 /mnt/e/A-Importance/wechat-bot/research/scripts/extract_main_literals.py
wsl -d Ubuntu-New -- python3 /mnt/e/A-Importance/wechat-bot/research/scripts/static_calls.py
```

- `verify_artifacts.py` 验证 manifest 包装内的 Raw 原始字节、config、全部层、最终写入样本。若发现 OCI whiteout，会主动停止，要求重新核对文件覆盖关系。
- `extract_main_literals.py` 按 ELF PT_LOAD 映射读取静态字节，并用 objdump 从函数入口解码确认特定直接调用点。
- `static_calls.py` 为 TSV 中的函数导出 objdump；其宽字节候选不用于直接推断字符串长度。
- `go_inventory.go` 使用 Go 标准库 debug/elf、debug/gosym 从 pclntab 提取函数记录。textStart 使用 pclntab 头值（本样本 0x4024e0），不能直接替换为 .text 段起点。
- `MainAudit.java`、`LibAudit.java` 是 Ghidra 静态导出脚本，不加载执行样本。Ghidra 项目分别位于 WSL `/tmp/wechat-main-audit-20261023/MainAudit` 及库分析临时目录；项目可能随临时目录清理失效，文本导出已保留。

本机 Windows REA 的 native provider 拒绝该 ELF；独立 WSL Ghidra 分析结果不能冒称 REA session 或 Evidence。Linux 分析器使用临时解包的完整 OpenJDK 21，不修改系统 JDK 安装。主程序全自动分析未全部完成，参阅最终报告的限制。

## 原始公开来源

- [hp0912/wechat-robot-client](https://github.com/hp0912/wechat-robot-client)
- [管理后端固定提交的容器创建文件](https://github.com/hp0912/wechat-robot-admin-backend/blob/edcc5c42d0b089832593af2079a03bc6b0800dc2/service/robot_manage.go)
- [openchat-wx 固定提交的服务器 Compose](https://github.com/xiaoguiwucan/openchat-wx/blob/319fa8769fc871a303c423232d064effce70eb5c/.deploy/server/docker-compose.yml)
- [Docker 官方 daemon 权限说明](https://docs.docker.com/engine/security/#docker-daemon-attack-surface)

公开源码与所审计闭源镜像的构建对应关系未知。这里的引用用于版本与配置审计，不构成可复现构建证明。
