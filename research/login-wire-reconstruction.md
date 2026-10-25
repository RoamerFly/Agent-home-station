# 扫码登录 protobuf 子集

`internal/loginwire` 是基于固定镜像的 `mm.proto` 描述符
`research/evidence/send-mm-descriptor.bin` 重写的标准库实现。它不包含、链接或
调用闭源 `main`、`libv08.so`，也不会建立网络连接。

本文件的字段编号和类型来自描述符；流程分层同时由当前镜像的静态调用图和只读
历史参考 `wechatdll-public/models/Login/GetQRCODE.go`、`CheckUuid.go` 交叉确认。
历史参考仅用于理解结构，未拷贝进入本实现。

## API

```go
func BuildQRRequest(base loginwire.BaseRequest, randomKey []byte,
    opt loginwire.QRRequestOptions) ([]byte, error)
func ParseQRResponse(raw []byte) (loginwire.QRResponse, error)

func BuildPollRequest(base loginwire.BaseRequest, randomKey []byte,
    uuid string, timestamp uint32, opt loginwire.PollRequestOptions) ([]byte, error)
func ParsePollResponse(raw []byte) (loginwire.PollResponse, error)
func ParseNotify(decrypted []byte) (loginwire.Notify, error)
```

`BaseRequest` 对应 `mm.BaseRequest`：`sessionKey`(1)、`uin`(2)、`deviceId`(3)、
`clientVersion`(4)、`deviceType`(5)、`scene`(6)。可选标量使用指针；字节字段使用
`Has*` 标志。因此，解码到的“字段不存在”和“字段明确为零/空字节”不会混淆。

当前固定样本的 ELF 静态画像给出 iPad `clientVersion=402668320` 和
`deviceType="iPad Air iPadOS18.4.1"`，证据见
`research/evidence/login-static-profile.json`。这不是运行时互通证明；调用方必须
持久化稳定的设备资料。QR 入口应生成 16 字节、首字节为 `0x49` 的 DeviceID，
及 16 字节随机 `randomKey`，再把它们填入 `BaseRequest` 和 `BuildQRRequest`。

`QRRequestOptions` 精确对应 `GetLoginQRCodeRequest` 中除 `baseRequest` 和
`randomEncryKey` 以外的字段：`opcode`(3)、`deviceName`(4)、`userName`(5)、
`extDevLoginType`(6)、`hardwareExtra`(7)、`softType`(8)、`msgContextPubKey`(9)。
描述符没有 DeviceToken 字段。因此 `fpinitnl` 获取的 token 或设备证明不能被偷偷
塞入本 protobuf；它属于尚待恢复的 Hybrid-ECDH/MMTLS 包装层。

`ParseQRResponse` 保留二维码字节、UUID、notifyKey、checkTime、expiredTime 和
BaseResponse 的 presence。`response.Code()` 在 BaseResponse 或 `ret` 缺失时返回
错误，绝不把缺失的字段解释成 `ret=0` 成功。

轮询响应有两层：`ParsePollResponse` 只读取
`CheckLoginQRCodeResponse.notifyPkg`(字段 3) 中的 `LoginQRCodeNotifyPkg.notifyData`
(字段 1)。该字节串按流程仍是使用 QR 返回 notifyKey 加密的密文。仅在调用方完成
已验证的解密后，才可调用 `ParseNotify` 解析 `LoginQRCodeNotify` 的 UUID、status、
username、临时 password、头像和昵称。临时 password 不应写日志或持久化到普通日志。

## 边界与验证

编码与解析都限制消息总长 2 MiB、每层 256 字段、嵌套深度 12，跳过可安全跳过的
未知 protobuf 字段，拒绝 legacy group、截断 varint、非法长度、Buffer `iLen` 与
bytes 长度不一致、以及字符串字段的非法 UTF-8。

`internal/loginwire/login_test.go` 使用手工、描述符导出的离线 fixture 覆盖：

- 二维码/轮询请求字段号与 wire type；
- QR response 的 explicit `ret=0`、未知字段和 presence；
- poll 的加密 `NotifyPkg` 与解密 `Notify` 的分层；
- 非法 UTF-8、Buffer 长度不匹配、缺失 ret 和无效 UUID。

这些 fixture 并非服务端流量，不含真实登录数据。通过单元测试只证明本子集按已恢复
schema 编解码；尚未证明 MMTLS、Hybrid ECDH、fpinitnl、AES 的具体模式，或真实
微信服务会接受请求。后续授权阶段需再恢复 `secmanualauth` 的 protobuf 与封装，
并由 transport 层提供已认证会话后才可能进入文字发送。
