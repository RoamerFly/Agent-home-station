package adapter

import "net/http"

const managementConnectionPath = "/api/apps/wechat/connections/{connection}"
const legacyImportPath = "/legacy-import"

// ManagementHandlers binds the station's authorized operations to the
// WeChat management API. Paths and methods are part of the adapter contract.
type ManagementHandlers struct {
	Contacts, Groups, GroupNotification, GroupProfile, SubmitGroupAction http.HandlerFunc
	ReadRecall, SubmitRecall, CancelDelivery, ResumeDelivery             http.HandlerFunc
	AcquireAttachment, DownloadAttachment                                http.HandlerFunc
	ListWorkGrants, SaveWorkGrant, RevokeWorkGrant                       http.HandlerFunc
	ShareSettings, MediaSettings                                         http.HandlerFunc
	PreviewImport, ApplyImport, PreviewMemory, ImportMemory              http.HandlerFunc
	ImportGroupPreference, ImportGroupRule, Activation                   http.HandlerFunc
	Rollback, RollbackAudit, RelocationReadiness                         http.HandlerFunc
	Archive                                                              ArchiveManagementHandlers
}

// ArchiveManagementHandlers exposes saved evidence when the station has no
// live native client. Its routes cannot start a connection or write to WeChat.
type ArchiveManagementHandlers struct {
	ListLegacyReceipts, ReadLegacyReceipt, ReadRelocationReceipt http.HandlerFunc
	ReadLegacySurf                                               http.HandlerFunc
}

func (h ManagementHandlers) Mount(mux *http.ServeMux, protect func(http.HandlerFunc) http.HandlerFunc) {
	mount := managementMount(mux, protect)
	mount("/contacts", h.Contacts, http.MethodGet)
	mount("/groups", h.Groups, http.MethodGet)
	mount("/groups/{group}/notification", h.GroupNotification, http.MethodGet)
	mount("/groups/{group}/profile", h.GroupProfile, http.MethodGet)
	mount("/group-actions", h.SubmitGroupAction, http.MethodPost)
	mount("/receipts/{receipt}/recall", h.ReadRecall, http.MethodGet)
	mount("/receipts/{receipt}/recall", h.SubmitRecall, http.MethodPost)
	mount("/receipts/{key}/cancel", h.CancelDelivery, http.MethodPost)
	mount("/receipts/{key}/resume", h.ResumeDelivery, http.MethodPost)
	mount("/attachments", h.AcquireAttachment, http.MethodPost)
	mount("/attachments/{asset}/content", h.DownloadAttachment, http.MethodGet)
	mount("/work-grants", h.ListWorkGrants, http.MethodGet)
	mount("/work-grants", h.SaveWorkGrant, http.MethodPost)
	mount("/work-grants/{delivery}/revoke", h.RevokeWorkGrant, http.MethodPost)
	mount("/"+ShareReaderToolID, h.ShareSettings, http.MethodGet, http.MethodPut)
	mount("/media-understanding", h.MediaSettings, http.MethodGet, http.MethodPut)
	mount(legacyImportPath, h.PreviewImport, http.MethodGet)
	mount(legacyImportPath, h.ApplyImport, http.MethodPost)
	mount(legacyImportPath+"/memory", h.PreviewMemory, http.MethodGet)
	mount(legacyImportPath+"/memory/{thread}", h.ImportMemory, http.MethodGet, http.MethodPost)
	mount(legacyImportPath+"/memory/{thread}/preference", h.ImportGroupPreference, http.MethodGet, http.MethodPost)
	mount(legacyImportPath+"/groups/{thread}/rule", h.ImportGroupRule, http.MethodGet, http.MethodPost)
	mount(legacyImportPath+"/activation", h.Activation, http.MethodGet, http.MethodPost)
	mount(legacyImportPath+"/rollback", h.Rollback, http.MethodGet, http.MethodPost)
	mount(legacyImportPath+"/rollback/audit", h.RollbackAudit, http.MethodGet, http.MethodPost)
	mount(legacyImportPath+"/relocation/readiness", h.RelocationReadiness, http.MethodGet, http.MethodPost)
	h.Archive.Mount(mux, protect)
}

func (h ArchiveManagementHandlers) Mount(mux *http.ServeMux, protect func(http.HandlerFunc) http.HandlerFunc) {
	mount := managementMount(mux, protect)
	mount(legacyImportPath+"/receipts", h.ListLegacyReceipts, http.MethodGet)
	mount(legacyImportPath+"/receipts/{key}", h.ReadLegacyReceipt, http.MethodGet)
	mount(legacyImportPath+"/relocation/receipts/{key}", h.ReadRelocationReceipt, http.MethodGet)
	mount(legacyImportPath+"/surf", h.ReadLegacySurf, http.MethodGet)
}

func managementMount(mux *http.ServeMux, protect func(http.HandlerFunc) http.HandlerFunc) func(string, http.HandlerFunc, ...string) {
	return func(path string, handle http.HandlerFunc, methods ...string) {
		if protect == nil || handle == nil {
			return
		}
		secured := protect(handle)
		for _, method := range methods {
			mux.HandleFunc(method+" "+managementConnectionPath+path, secured)
		}
	}
}
