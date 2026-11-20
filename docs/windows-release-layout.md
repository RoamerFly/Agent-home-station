# Windows 开发包目录

根目录只保留 AgentHomeStation.exe、applications、publictools、env、data。

- applications：应用包代码、清单、专用工具；微信、抖音等应用私有数据放 data/applications。
- publictools：公共工具包，每个工具一个带用途后缀的目录。包 ID 不随物理目录名变化；models/project/runtime 分别为模型、后端工程和执行入口。
- env：Python、Windows Agent 运行器、微信原生运行环境等共享环境。
- data/applications：应用登录、记忆、会话规则、回合、应用任务和媒体记录。
- data/publictools：工具配置、登记、任务、产物索引；catalog 存单一工作站工具登记和按内容摘要保存的安装版本。摘要目录是版本/完整性索引，不是多个大肥鱼。
- data/workspace/home：大肥鱼的唯一家目录；diary 日记、projects 项目、works 作品、scratch 草稿、Project Tools 自建工具、Connected Apps 应用工作资料。
- data/main_setting：工作站身份、管理密码、主记忆、日志、浏览器登录资料和桌面服务。

启动时将旧 fish_home/<身份>/home 原目录改名为 workspace/home，将旧工具登记的鱼身份目录改名为 publictools/catalog。内部身份、工具登记内容、应用登录资料保持不变；目标冲突时保留双方内容并报告错误。空的旧容器仅用非递归删除处理。

Build-Desktop 的 LocalStation 构建把经过清单验证的工具放入带用途后缀的 publictools。请使用独立 staging 目录构建，停止正式实例后部署，不直接合并真实模型和私有资料。

在 Windows 源码工作树执行以下命令，路径指向实际发行包。

~~~powershell
python -X utf8 scripts/update_windows_release.py --release E:\Agent\agent-home-workstation\dist\release
python -X utf8 scripts/update_windows_release.py --release E:\Agent\agent-home-workstation\dist\release --candidate <输出的候选目录> --apply
~~~

默认构建并校验候选。脚本复用经过清单验证的微信原生运行时并重新编译微信管理端、工作站 EXE 和服务。应用与工具按现有包 ID 更新程序清单所列文件。正式实例退出后，脚本逐文件进行同盘原子替换，更新发行与迁移清单，再启动工作站。健康门槛覆盖两个管理服务、原生就绪和账号在线状态。失败时有序退出候选进程并恢复程序文件及清单。证据和回滚清单留在脚本打印的事务目录。遇到意外断电时，保留锁和事务目录，先核对运行状态与回滚清单。

回合清理事务备份使用系统临时目录。YuE 中间文件归档在任务状态目录下，LPAC 退出后清理自身的空设备替代文件。音乐成品删除只核对仍处于活动资料目录的相关文件。待删除区中的历史文件由主人手动删除。

Clean-ReleaseDuplicates.ps1 在停机后核对旧无后缀工具的每个文件摘要，只整理有完全对应副本的目录；嵌套 env/env 保留外层环境，忽略旧运行清单差异；唯一/不同文件会使清理停止。所有冗余内容移到 E:\待删除，不直接删除。根目录历史服务 EXE 仅在规范服务文件存在且实例停止后移到待删除目录。

旧安装仍可能有 data/main_setting/runtime/runtimes。部分音乐配置直接引用此路径，因此必须同步迁移配置后才可整理；不得仅按同名判断可删除。当前服务已能从 release/env 发现公共工具运行环境；本机已同步音乐配置并将旧运行环境移到待删除目录。

权限页面通过当前通讯录分页读取可信账号昵称、微信号；原生身份 ID 保留在折叠管理区，不用昵称替换发送和鉴权主键。
