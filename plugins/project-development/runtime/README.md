# 运行时

main.py 是受管 Python MCP stdio 入口。catalog.json 是经过包哈希校验的工具目录。三个能力 project.workspace_read、project.workspace_write、project.workspace_execute 分别声明 workspace.read、workspace.write、process.execute。

每次调用只产生一次 wechat/workspace_request；宿主绑定原参数并预留副作用，插件不能改写命令或路径，也不能重试一个不确定的宿主操作。公共登记的原始快照由宿主固定根句柄读取，JSON 有效性、项目清单与失败条目由插件解释。宿主核验注册内容源自实际读取的记录，核验文件/进程结果与实际执行结果完全一致。

独立进程的初始化和健康检查没有代理权。真实代理权限只存在于 Controller 绑定的调用上下文中。隔离或插件不可用时关闭能力，不回退宿主 shell。
