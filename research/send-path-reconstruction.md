# 闭源样本：最小文字发送调用链静态恢复

## 结论

本轮仅离线反编译 `research/samples/main.elf.bin`；没有运行 ELF、库或连接网络。样本中存在清晰的调用顺序：`wechatdll/models/Msg.SendNewMsg`（ELF `0xb61620`）→ `wechatdll/comm.SendRequest`（`0xb08a80`）→ `wechatdll/Algorithm.Pack`（`0x7d3e20`）→ 依会话标志选择 `Mmtls.POST`（`0x8f5640`）或 `Mmtls.MMtlsPost`（`0x8f3060`）→ 对返回包做 `Algorithm.UnpackBusinessPacket`（`0x7d4880`），对特殊 CGI 路径另走 AES-GCM 解包（`0x7d4c60`）。这足以证明文字发送并非直接 HTTP protobuf POST，而是依赖已建立会话的自定义封包和传输状态。

历史公开参考与样本字符串共同支持文本 CGI 路径 `/cgi-bin/micromsg-bin/newsendmsg`；历史版本使用 CGI `522`、`encryptType=5`、`UseCompress=false`。样本的 `SendNewMsg` 确实调用同一 `SendRequest`，ELF 中也包含上述 CGI 字符串。不过，受 Go ABI 恢复和结构字段布局不完整的限制，本轮**没有从新样本机器码单独证明 522、encryptType=5 与 UseCompress=false 的具体实参值**；它们应列为待与受控会话抓包核实的候选，不应作为已验证协议常量。

## 请求体和调用边界

根代理从同一样本的 `mm.proto` 描述符恢复的请求体为：`NewSendMsgRequest{cnt#1=1, info#2=ChatInfo}`。文本 `ChatInfo` 至少需要 `toid#1`、`content#2`、`type#3`、`utc#4`、`clientMsgId#5`；`msgSource#6` 在普通文本可省略，群 @ 使用 XML 格式来源字段是历史实现行为，尚未由该样本完全验证。

样本的 SendNewMsg 会先建请求 protobuf，再复制登录/设备会话相关字段到其调用 `SendRequest` 的 Go 栈参数区域。由于未完成整文件自动分析，Ghidra 的伪代码未能可靠还原命名结构体字段。它能明确看到在 `0xb61c53` 调用 `0xb08a80`，以及 SendRequest 内部在 `0xb08be1` 调用 Pack。

`SendRequest` 中存在两个传输分支：一个标志为零时于 `0xb08e2a` 调 `MMtlsPost`；否则于 `0xb08dcb` 调 `POST`。在 HTTP/MMTLS 返回成功且长度大于约 31 字节后，普通路径在 `0xb09089` 调 `UnpackBusinessPacket`；仅当 CGI 字符串匹配特殊常量时才在 `0xb090cf` 使用 `UnpackBusinessPacketWithAesGcm`。样本中 `newsendmsg` 仅由证据证明为字符串存在，尚未证明其是否触发该特殊分支；历史实现将该特殊路径限定为 `newsync`，故最合理的当前工作假设是文本发送走普通 Unpack，但仍待实测。

## 封包与传输

`Algorithm.Pack` 在入口处按 CGI 值及一个会话/布尔标志选择多条封装分支；反编译显示 CGI `0x8a` 有专用逻辑，不能把该特例外推给 CGI 522。封包函数的入参来自 SendRequest 栈上的请求 protobuf、CGI、UIN、会话键/登录 ECDH 相关字节串和多个控制字段。它明显不只是 protobuf 编码。`MMtlsPost` 则构造请求数据并进一步调用 `POST` 与 MMTLS 加密/解密辅助函数。仅以静态调用图无法恢复一个可互通的 MMTLS 会话：会话密钥、cookie、UIN、服务端地址、握手状态和可能的设备绑定均是必要输入。

## 最短独立重写路径

1. 仅独立实现 protobuf `NewSendMsgRequest`，生成上述字段，能覆盖“业务请求体”层。
2. 从受控、合法建立的会话中显式注入 `UIN`、session key、cookie、client-session key、登录 ECDH/其他传输状态；不应试图从样本猜测或硬编码。
3. 实现并以已知明文/密文包或隔离测试会话验证 `Algorithm.Pack` 的 CGI 522 分支、响应解包和 MMTLS 握手，再接真实传输。
4. 在验证前，发送服务应停在“生成请求包”或“注入已验证 transport”接口层。独立 protobuf 代码本身不足以发送消息。

## 已知限制与证据

Ghidra 将 Go 函数标为 `FUN_*`，伪代码是近似表达，不是源码。主程序的全自动分析未完成；本次以现有函数边界执行了 `-noanalysis` 的有限导出，故不能据此确认全部错误处理、重试或字段语义。公开历史源码无明确许可证，只用于与样本交叉理解，未复制到实现。

- [send-ghidra.txt](evidence/send-ghidra.txt)：六个函数的伪代码和调用边。
- [send-functions.tsv](evidence/send-functions.tsv)：导出地址清单。
- [send-ghidra.log](evidence/send-ghidra.log)：Ghidra 运行日志。
- [send-schema.json](evidence/send-schema.json)：由根代理恢复的当前样本 protobuf 描述，本文仅引用，不重新提取。

## 追加：RQT 与直接调用证据

对 Pack 的原始指令反汇编并按 `main-functions.json` 映射，确认 `0x7d44eb` 是直接 `call 0x7bd5a0`，目标为 `wechatdll/clientsdk/v08.Rqtx`。它位于 `0x7d44e6` 对 `wechatdll/clientsdk/baseutils.Md5ValueByte` 的调用之后；Pack 在普通（非 CGI 138）未压缩分支于 `0x7d3fa1` 调 `wechatdll/Algorithm.AesEncrypt`。因此当前样本的封包路径是“请求 protobuf → AES 密文 body → RQT”，RQT 不能省略。

`v08.Rqtx` (`0x7bd5a0`) 是 Go 包装层：初始化一次后在 `0x7bd690` 经全局函数对象间接调用后端。当前有限静态分析没有将该间接目标解析为 libv08 的 `rqtx` 导出，故“该调用确实进入 libv08”仍为合理但**未证实**的推断。静态边界足以说明纯 Go 重写需要与该后端输出逐样本校验。

使用 `objdump` 检查 `SendNewMsg` 调用 SendRequest 前的 `0xb61af8..0xb61c53`，该段复制了大量登录数据/结构字段并调用 `runtime.duffcopy`，没有出现 `$0x20a`（522）或 `$0x5` 的直接立即数写入。由此不能从该小段指令独立验证 522/5/false；这些常量可能被前段结构构造、全局只读数据或受 Ghidra 未恢复的 Go 结构布局承载。证据见 `evidence/send-msg-objdump.txt`。

`evidence/send-crypto-ghidra.txt` 另包含 `v08.Rqtx`、`Algorithm.AesEncrypt`、`MmtlsEncryptData`、`MmtlsDecryptData2` 的有限反编译。它确认了加密函数调用边界，但尚不足以提供可互通的 MMTLS 实现。

## 更正：v08 后端绑定

此前“当前有限导出未解析 v08.Rqtx 的间接后端”的表述只描述本次函数级伪代码；结合已有 `main-ghidra-selected.txt`、`main-ghidra-followup.txt` 与 `main-exact-literals.json`，该边界已被更完整地恢复：`dynlib.init.0` 的全局对象经 `NewLinuxLibrary` 加载，`v08.init.0` 传入 `lib/libv08.so`，`v08.Rqtx` 使用常量 `rqtx` 查找并通过 `linuxLibrary.Call` 的 dlsym/CGO 路径调用。没有运行时网络观察，但“样本设计上将 Rqtx 绑定到 lib/libv08.so 的 rqtx 导出”已是现有静态证据支持的结论。
