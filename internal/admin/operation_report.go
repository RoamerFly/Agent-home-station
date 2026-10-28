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

// Keep failed reconciliation visible even for autonomous shares without a
// parent user task. A group member never inherits an owner's repair identity.
func (m *Manager) queueMediaRepair(in companion.Input, job, reason string) error {
	for _, owner := range m.agent.config().OwnerIDs {
		if !permittedRecipient(m.Config().Config, owner) {
			continue
		}
		id := agentID(in.Account, in.Thread, job, "media-reconciliation-repair")
		for _, old := range m.agent.list(in.Account, "") {
			if old.ID == id {
				return nil
			}
		}
		trusted := (&permittedTools{manager: m, trace: in}).privileged()
		task := agentTask{ID: id, Account: in.Account, Thread: owner, Sender: in.Sender, DeliveryJob: job, OwnerRequested: trusted, ReportOnly: !trusted, ReportSourceThread: in.Thread, OriginTrace: in.TraceID, State: "waiting", Next: time.Now(), Title: "核对附件投递故障"}
		task.Checkpoint = fmt.Sprintf("原目标会话 %s；原job_id %s；同群记录核对或原任务续办失败，原生原因 %s。先核查wechat_delivery_status、真实聊天记录和失败阶段，再修复并续办原任务，已接受部分不重发，不换key创建重复队列。技术说明只在当前主人私聊。", in.Thread, job, diagnosticCode(reason))
		if !trusted {
			task.Sender = ""
			task.Checkpoint += " 当前仅作运行故障报告，不操作其他会话或项目；原发送器继续独立退避核对。"
		}
		return m.agent.task(task)
	}
	return nil
}
