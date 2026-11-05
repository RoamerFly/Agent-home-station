# 项目开发

独立 Python MCP stdio 工具，提供 project_list/read/create、server_tools、server_status/list/read/search/write/edit、server_exec、server_process 与 server_windows_process。工具描述、schema、命令映射和项目登记 JSON 的解释保存在 runtime/catalog.json 与 runtime/main.py 中。

必须安装、配置并启用本工具，部署受管 Python 与平台隔离执行器后才能实际调用；未安装或隔离不可用时没有主体实现回退。安装不会自动启用。共享代码、模型、运行库和当前账号会话目录继续使用宿主管理的原路径，不迁移或复制用户数据。

工具只通过 wechat/workspace_request 请求本次已绑定的操作；没有工作区挂载、网络、宿主凭证或直接进程执行权。文件与命令由宿主按真实来源、账号、会话、权限、原参数执行并保存实际结果。Markdown workspace_* 记忆与其编辑器仍为主体核心功能。

`server_tools` 是受限的已打包运行库索引：可按 `media` 或 `python` 类别、或精确 ID 发现 FFmpeg、FFprobe、CLI Python 和声明版本。返回的是公共受管运行库的实际路径及版本探针命令；探针和实际工作仍通过现有隔离进程工具运行，因此有真实进程 ID、状态、取消和安全的再次执行语义。它不会枚举目录、控制面、桌面服务、微信/账号数据、凭据或隔离 broker；这些能力始终走各自的宿主代理。

离线验证：python -B -m unittest discover -s plugins/project-development/tests。Go 的 TestProjectPlugin* 验证宿主安全边界；TestLiveProjectPluginIsolatedRuntimeWithSyntheticWorkspace 使用 WECHAT_PLUGIN_TEST_PYTHON 显式选择隔离 Python，仅操作临时合成工作区，不调用模型或微信。
