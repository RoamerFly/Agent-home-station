# Docker 运行与镜像更新

`wechat-agent` 镜像一起启动管理网页和微信原生服务。`wechat-music` 是独立的可选 GPU 音乐镜像；日常聊天不加载 Torch、CUDA 或歌曲模型。

## 首次运行

需要 Docker Engine / Docker Desktop 和 Compose。Windows 使用 Linux containers；音乐容器还需要 NVIDIA GPU 容器支持。保留部署目录中的 `deploy/docker`、启动器、`public_workspace` 和 `pri_workspace`。

```sh
docker load --input wechat-agent-0.35.0.tar
docker compose -f deploy/docker/compose.agent.yaml up -d --pull never
```

网页默认在 <http://127.0.0.1:8081/>，新数据卷的初始密码为 `admin`；已有数据保留原密码。登录后在账号页面扫码。需要远程访问时设置 `WECHAT_WEB_BIND`、`WECHAT_ADMIN_ORIGIN`，使用自己的反向代理与访问控制。

首次启动会创建 `wechat-agent-data` 数据卷。**不要用 `docker compose down -v` 更新**，`-v` 会删除数据卷。工作区挂载路径相对于 Compose 文件，不依赖命令运行目录。

## 现有数据迁移

先排空任务并停止旧实例，备份原生数据，再复制到一个新的数据卷。不能让两个实例同时运行同一账号。`scripts/prepare_docker_data.py` 在停止的副本上重定位原生资料库、独立的密钥清单及已知控制路径，可用 `--pause` 暂停自动回复直到检查完成。

登录态、数据库、API Key 和管理密码在 `/data` 数据卷中，不进入镜像。公共程序/模型和账号私有文件分别挂载至 `/workspace/public_workspace`、`/workspace/pri_workspace`。更新保留这些目录及 Agent 的项目修改，迁移保留旧目录以便回滚。

## 后续只更新镜像

Windows 双击「更新Docker镜像.vbs」选择 `.tar` 文件。启动器核对同名 `.sha256`（若交付）、导入镜像、排空任务、重建服务并检查网页健康。镜像包统一携带 `wechat-agent:local` 标签，可选音乐镜像携带 `wechat-music:local`。部署参数在本机私有的 `secrets/docker-deployment.json`，不随版本覆盖。

Linux 先在管理端暂停/排空任务，再执行：

```sh
sha256sum -c wechat-agent-0.35.0.tar.sha256
docker load --input wechat-agent-0.35.0.tar
docker compose -f deploy/docker/compose.agent.yaml up -d --pull never
docker compose -f deploy/docker/compose.agent.yaml ps
```

更新程序无需重拷模型和用户数据。更新异常时保留数据卷和旧镜像，停止新实例后切回旧标签；不要同时启动新旧账号实例。

## 可选音乐服务

本机音乐推理需要 GPU 资源，不能假定它能在 2 核 2 GB / 4 核 4 GB 无 GPU 云主机上正常生歌。主镜像可以独立运行，音乐服务在有 GPU 的机器运行；模型和声线按清单单独保留。

设置 `WECHAT_ACCOUNT_DIRECTORY` 为当前账号的 SHA-256 目录名，准备该账号私有目录和 `secrets/yue-bridge-token.txt` 后启动：

```sh
docker compose -f deploy/docker/compose.agent.yaml -f deploy/docker/compose.music.yaml up -d --pull never
```

音乐 Worker 只挂载一个账号的私有目录，公共项目与模型只读。推理在 Bubblewrap 中运行，通过私有 Unix Socket 与可信桥接通信，不开放 ComfyUI TCP 端口，不挂载微信/API 密钥。多账号应分别建立 Worker。

## 隔离与验收边界

主服务使用 UID 10001、只读根文件系统、capability 清空和 no-new-privileges，不使用 privileged、宿主 PID/network 或 Docker Socket。非特权 Bubblewrap 需要用户命名空间，Compose 为此解除该容器的默认 seccomp/AppArmor 过滤；隔离不可用时工具报错，不回退到宿主 Shell。

网页健康、微信连线、文件哈希、微信接受回执、手机播放和 GPU 音质是不同验收项。镜像构建成功不代表真实消息已经送达，实际通过范围以交付记录为准。
