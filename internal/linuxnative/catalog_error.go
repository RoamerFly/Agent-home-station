package linuxnative

type CatalogError struct {
	Code       string
	HTTPStatus int
}

func (e *CatalogError) Error() string { _, message := e.SafeToolFailure(); return message }
func (e *CatalogError) SafeToolFailure() (string, string) {
	message := "微信目录读取失败，尚未取得成员资料"
	switch e.Code {
	case "catalog_network_unavailable":
		message = "微信目录接口连接暂时失败；已补试一次只读查询，未改动账号或发送消息"
	case "catalog_cancelled":
		message = "目录查询已被新消息取消；这不表示微信断线，可按最新请求重新查询"
	case "catalog_timeout":
		message = "读取微信目录超时，尚未取得完整结果"
	case "catalog_auth_denied":
		message = "微信目录读取权限或登录状态无效，请检查后台授权"
	case "catalog_not_found":
		message = "本地还没有该群的成员目录，请先刷新群聊和群成员资料"
	case "catalog_cursor_invalid":
		message = "目录分页游标已失效或格式不正确，请从首批重新查询，不要自行编写cursor"
	case "catalog_response_invalid":
		message = "微信目录返回结构或大小无效，尚未取得可靠成员资料"
	case "catalog_kind_invalid":
		message = "wechat_read的kind只支持members（群成员）或messages（当前会话消息）"
	case "catalog_group_invalid":
		message = "当前会话不是有效群聊，不能读取群成员名单"
	case "catalog_query_invalid":
		message = "目录搜索或分页参数超过限制，请使用工具返回的原始cursor"
	case "catalog_upstream_unavailable":
		message = "微信目录后台暂时不可用，请稍后查询"
	case "catalog_request_rejected":
		message = "微信目录请求被拒绝，请核对参数与群资料同步状态"
	}
	return e.Code, message
}
