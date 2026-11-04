package admin

import "errors"

type yueCallError struct{ code string }

func (e *yueCallError) Error() string {
	switch e.code {
	case "yue_bridge_upgrade_required":
		return "本地YuE桥接版本较旧，不能确认参考模式和固定种子已生效；请升级桥接，本次未创建任务"
	case "yue_bridge_unavailable":
		return "本地YuE桥接连接失败；用music_service_status核查；本机受管服务停止时可信主人可用music_service_start启动，再核查就绪状态，按需读取共享project-yue.md及项目启动文档，在实际运行节点修复并验证。当前请求未取得任务编号，原任务与音频保留；不要盲目重复生成"
	case "yue_bridge_auth_failed":
		return "YuE桥接鉴权失败，请检查桥接令牌"
	case "yue_account_scope_mismatch":
		return "音乐服务的任务目录属于另一个微信账号；请核对服务路由和账号工作目录。工具程序、模型与令牌可共用，私人任务及素材分别保存；原任务保留"
	case "yue_backend_unavailable":
		return "YuE桥接在线，ComfyUI生成后端不可用；本次未提交新任务"
	case "yue_submission_unknown":
		return "YuE未取得有效任务编号；提交结果未确认，请核对原记录，不重复提交"
	case "yue_job_scope_denied":
		return "该歌曲任务不属于当前会话；请先通过agent_tasks查询本会话任务编号，不使用素材ID或其他群任务ID"
	case "yue_job_not_found":
		return "YuE桥接没有这个任务编号；请查询本会话原任务，不把素材编号当生成任务编号"
	case "yue_response_limit":
		return "YuE响应不完整或超过大小限制；尚未准备附件"
	default:
		return "YuE拒绝请求或任务结果尚未可用，请核对原任务状态"
	}
}
func yueError(code string) error                          { return &yueCallError{code: code} }
func (e *yueCallError) SafeToolFailure() (string, string) { return e.code, e.Error() }
func safeToolFailure(err error) (string, string) {
	var safe interface{ SafeToolFailure() (string, string) }
	if errors.As(err, &safe) {
		code, message := safe.SafeToolFailure()
		return message, code
	}
	var yue *yueCallError
	if errors.As(err, &yue) {
		return yue.Error(), yue.code
	}
	return "", ""
}
