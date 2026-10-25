# MMTLS 初始握手候选恢复

`internal/mmtls/handshake.go` 只实现握手字节构造和验证，不建立 HTTP 连接，也不运行原始样本。入口为 `NewHandshake(now, entropy)`，输出候选 client hello record；`Complete(response)` 仅在记录边界、P-256 公钥、三条 AES-GCM 记录、session ticket 格式和 server-finished HMAC 都通过时返回 `Session`。网络传输由调用方单独决定。

固定样本的有限静态证据为 `Mmtls.InitMmtlsShake`（`0x8f0fa0..0x8f3060`）及 `research/evidence/send-crypto-ghidra.txt` 中 MMTLS AES-GCM 调用。它与历史参考 `C:/Temp/wechat-bot-research-20261023/wechatdll-public/Mmtls/InitMmtlsShake.go` 的一致候选步骤是：两个未压缩 P-256 点、明文 server hello 中 offset 52 的 key-pair 序号、offset 58 的服务端 P-256 点；以 `SHA256(ECDH)` 作 HKDF-expand PRK；`handshake key expansion || SHA256(clientHello || serverHello)` 导出 56 字节记录材料；然后以 server sequence 1、2、3 解三条 AES-GCM record。公开历史实现只在序号等于 `2` 时选择第二把客户端私钥，其他值使用第一把；实现因此兼容 `0`、`1`、`2`，拒绝大于 `2` 的值。`PSK_ACCESS || SHA256(clientHello || serverHello || part2)` 导出短连接 32 字节 PSK。part3 offset 6 的 uint32 长度字段承载 early-data ticket；历史实现按该嵌入长度取段，没有要求它消耗整条 part3 明文，故当前实现也只拒绝越界长度。

历史初始握手代码会计算 `HMAC(HKDF-expand(secret, "server finished"), SHA256(clientHello || serverHello || part2 || part3))`，但没有实际比较。候选实现要求最后一条解密数据是受较新 MMTLS 参考格式支持的 `uint32 total || 0x14 || uint16 macLength || 32-byte MAC`，并以恒定时间比较该 MAC。没有这个验证绝不返回 session。

虽然完成流程会检查 ephemeral ECDH、AEAD 和 finished MAC，当前样本恢复范围没有建立可复核的服务端长期公钥或 certificate-verify 签名验证。主动攻击者仍可能选择自己的 ephemeral key 并生成正确 finished MAC。因此 `Complete` 返回的 session 始终带 `Experimental: true`，它不是已认证服务器会话，不能自动作为“可登录”或“ready”状态使用。

`CompleteWithPinnedServerKey(response, key)` 提供了一个显式的额外校验点：它把 part2 解析为 `uint32 total || 0x0f || uint16 ASN.1-DER-length || ECDSA signature`，用调用方提供的 P-256 公钥校验 `SHA256(clientHello || serverHello)`。该结构与 `C:/Temp/wechat-bot-research-20261023/gommtls-public/mmtls/signature.go` 及其长连接握手顺序一致；该仓库不是微信发布者，且其静态 `ServerEcdh` 点没有出现在本次固定样本的原始字节中，所以本项目不把该点内置为可信根。调用方若通过独立渠道获得和轮换 pin，可用此接口防止相对该 pin 的中间人；即使验证成功，`Experimental` 仍为真，直到 pin 的来源被单独证明。

在 `research/evidence/login-handshake-probe-2.json` 记录了一次受限匿名结构探测：对 `short.weixin.qq.com` 的请求不含账号、会话或消息数据，收到四条 `0x16` record，负载长度为 `123/95/293/55`，并通过了本实现的 P-256、三条 AEAD、ticket 边界与 finished HMAC 检查。该证据证明此一次响应与恢复出来的握手路径相容；它不证明微信账号登录、消息发送、服务器长期身份，亦不能把两个同时修复（selector 兼容与 part3 尾随字节）拆分为各自的因果结论。

独立的 QR 获取请求元数据进一步确认短连接响应顺序为 `0x16:46, 0x16:55, 0x17:17221, 0x15:23`：第一条是明文 server hello，第二条是加密 server finished，随后才是业务响应与 close。`Exchange.DecodeResponse` 因此固定要求这四条记录，按 server sequence `1/2/3` 对后三条逐一 AEAD 验证，并以 `HMAC(HKDF-expand(PSK, "server finished"), SHA256(clientHello || clientExtensions || serverHello))` 校验第二条的 `0x14` finished envelope。该短连接 finished 公式由同一公开实现的 key label、其 hello transcript 顺序以及实际 55 字节记录长度共同支持；它仍不提供服务端长期身份认证。

测试用独立 P-256 synthetic peer 覆盖正常派生、损坏 AES-GCM tag、损坏 finished MAC、截断和非法服务端公钥、selector 为 `0`、part3 尾随字节，以及使用本地生成身份密钥的 pin 校验与错误 pin 拒绝。这些只是离线密码学和格式一致性测试，不证明与微信服务互通。
