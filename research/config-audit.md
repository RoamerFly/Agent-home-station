# 配置与接口静态核查

> 本文是配置阶段记录。实际鉴权及部分请求路径已由二进制静态分析补充，见 [最终报告](closed-protocol-final-2026-10-23.md)，不能继续据本文的未知项推断无鉴权。

范围：已提取镜像 entrypoint.sh 和 swagger/swagger.json，以及 openchat-wx 部署配置；未运行程序、登录或访问外部地址。证据类型为配置和接口声明，不代表运行时行为。

- entrypoint.sh 第 5–6 行将服务配置为 0.0.0.0:9000；实际对宿主机的暴露取决于容器网络和端口映射。第 12–14 行将 Redis 连接和密码写入 conf/app.conf，文件实际权限与存续情况未知。第 21–22 行配置明文 HTTP 回调，包含消息同步和退出通知。第 47–48 行配置外部 HTTP 验证地址，第 50 行从 UUID_URL 读取地址；是否调用及传输内容尚未知。第 53–55 行配置 pprof 地址和端口，实际监听及认证未知。
- Swagger 顶层 securityDefinitions 和 security 均缺失，共 166 个路径；例如 /Login/Get62Data、/Login/GetCacheInfo、/Friend/GetContractList、/Friend/Delete、/Group/GetQRCode。缺少声明不能证明实际接口无鉴权，也不能证明对应端点实际可用。
- openchat-wx .deploy/server/docker-compose.yml 第 220 行给管理服务挂载 Docker socket；第 226 行向管理服务传入 OPENAI_API_KEY，第 228、231 行配置协议服务 token 和滑块 token。仅凭此不能证明模型 API 密钥传入协议容器；动态创建容器的代码与实际环境仍需检查。第 264–269 行 Redis/MySQL secret 来自宿主文件；示例环境包含示例密码，实际生产配置未知。
- 完成程度：启动配置和公开接口声明已检查；实际鉴权、密钥持久化、动态注入和网络请求尚未证实。机器可读证据见 evidence/config-audit.json。
