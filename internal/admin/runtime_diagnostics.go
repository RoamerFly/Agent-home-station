package admin

import (
	"encoding/json"
	"strings"
)

// RuntimeFailure contains only trusted local admission diagnostics. Provider
// payloads and child output are never copied into this safe error envelope.
type RuntimeFailure struct {
	Code           string `json:"code"`
	Stage          string `json:"stage"`
	Message        string `json:"message"`
	NotStarted     bool   `json:"not_started"`
	Retryable      bool   `json:"retryable"`
	EffectiveScope string `json:"effective_scope,omitempty"`
	Continuation   string `json:"continuation"`
}

func (e *RuntimeFailure) Error() string { return e.Message }
func (e *RuntimeFailure) SafeToolFailure() (string, string) {
	b, _ := json.Marshal(e)
	return e.Code, string(b)
}
func processAdmissionFailure(reason, scope string) error {
	e := &RuntimeFailure{Code: "process_admission_rejected", Stage: "admission", Message: "程序未启动：参数或执行环境不符合要求", NotStarted: true, EffectiveScope: scope, Continuation: "检查原操作的拒绝原因，更正参数后再提交；拒绝不代表结果未知"}
	switch {
	case strings.Contains(reason, "路径不在已授权"):
		e.Code = "path_scope_denied"
		e.Stage = "scope"
		e.Message = "程序未启动：路径不在已授权工作目录内"
		e.Continuation = "先project_list/project_read查受管项目入口，使用server_status返回的账号工作区；源码仓库根目录不是账号工作区"
	case strings.Contains(reason, "权限") || strings.Contains(strings.ToLower(reason), "access is denied"):
		e.Code = "filesystem_access_denied"
		e.Stage = "filesystem"
		e.Message = "程序未启动：执行资源访问被拒绝"
		e.Continuation = "核对本次有效挂载和资源ACL，不扩大到其他账号或整个磁盘"
	case strings.Contains(reason, "不存在") || strings.Contains(reason, "not found"):
		e.Code = "runtime_resource_missing"
		e.Stage = "resource"
		e.Message = "程序未启动：登记的执行资源缺失"
		e.Continuation = "使用project_read核对登记入口及解释器，修复资源后再提交"
	}
	return e
}
