# 动态创建协议容器：静态核查

公开仓库 hp0912/wechat-robot-admin-backend，提交 edcc5c42d0b089832593af2079a03bc6b0800dc2。源码和所审计发布镜像的构建对应关系未知。未执行服务。

service/robot_manage.go:254–265 的 DockerStartWeChatServer 明确设置 WECHAT_PORT、REDIS_HOST、REDIS_PORT、REDIS_PASSWORD、REDIS_DB、WECHAT_CLIENT_HOST 和 UUID_URL；该环境变量列表没有 OPENAI_API_KEY。此结论限于该创建路径；镜像自身默认环境及其他运行方式另计。

同文件 266–290 行的 HostConfig 没有 PortBindings 或 PublishAllPorts，容器连接 vars.DockerNetwork。因而该创建代码未显式发布宿主端口；同网络容器及其他网络配置仍可能访问。配置注释声称隔离，但实际此处接入 vars.DockerNetwork，不能从注释认定每个机器人独立隔离。

此创建路径未传递内部鉴权密钥，仓库对 transportauth、HMAC、AUTH_SECRET 的有限搜索无匹配；无法确定鉴权密钥如何生成或分发。镜像二进制已发现 VerifyClientRequest 和 InitAuthRuntime 符号，因此不得据 Swagger 未声明鉴权推断无鉴权。源码与镜像差异可能解释这一缺口，须追踪镜像代码及运行时配置。
