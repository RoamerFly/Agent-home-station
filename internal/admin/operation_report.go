package admin

import (
	"fmt"
	"time"
	"wechat-bot/internal/companion"
)

// Operational notifications are created by the backend, never by granting the
// originating group member the owner's execution identity.
func (m *Manager) queueOperationReport(in companion.Input, reason string) error {
	if in.ReportOnly {
		return nil
	}
	for _, owner := range m.agent.config().OwnerIDs {
		if !permittedRecipient(m.Config().Config, owner) {
			continue
		}
		id := agentID(in.Account, in.Thread, in.TraceID, reason, "private-operation-report")
		for _, old := range m.agent.list(in.Account, "") {
			if old.ID == id {
				return nil
			}
		}
		return m.agent.task(agentTask{ID: id, Account: in.Account, Thread: owner, ReportOnly: true, ReportSourceThread: in.Thread, State: "waiting", Next: time.Now(), Title: "运维状态报告", OriginTrace: in.TraceID, Checkpoint: fmt.Sprintf("这是给主人私聊的系统运维状态报告：来源会话 %s；处理请求未完成，状态代码 %s。只用一条简短文本说明这次处理出现异常，详情保留在后台日志。不要向来源群发送技术说明，不声称结果已生成或已送达，不承诺未安排的自动操作。当前仅作报告，没有工具执行权限。", in.Thread, reason)})
	}
	m.log("operation_report_no_authorized_private_owner")
	return nil
}
