# 迁移与兼容

项目工具只有插件入口；旧 project/server 工具不再直接执行。项目登记解析、业务工具目录与操作映射已迁入插件。宿主保留通用路径约束、CAS 文件编辑、Linux Bubblewrap / Windows LPAC、真实进程记录和原会话任务续办。

既有 public_workspace/manifests、projects、models、runtimes 以及账号工作区保持原位置。无需备份、迁移或读取生产数据即可完成源代码切换。Markdown 笔记、账号记忆和管理界面不属于可选插件。

后台进程沿用原 request_key、scope、process ID 和任务归属。禁用、替换包、撤销授权或来源变化会撤销宿主执行授权；已提交但结果未知的记录不重放。重启后先查询原进程或检查文件，不能根据插件声明推定执行成功。

foreground 命令沿用宿主管理的单次执行时限；长任务应使用后台进程工具及可调整 timeout_seconds，0 表示不增加执行截止时间。列表、读取与搜索保留显式分页参数和实际续接位置。单个项目注册元数据仍采用原来的 64 KiB 格式边界；源码和文档通过 server_read 分页，不受该元数据格式边界限制。
