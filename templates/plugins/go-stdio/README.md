# Go stdio 合成模板

在项目根目录执行 `go run ./templates/plugins/go-stdio`，使用标准库，无模型或网络调用。

支持 MCP 初始化、健康、echo、任务提交、查询和取消。任务仅保存在内存中，重启后未知任务返回 needs_review；不能将此模板视为生产任务持久化实现。宿主权限、素材登记与隔离执行器必须另行接入。
