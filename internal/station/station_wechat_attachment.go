package station

import (
	"net/http"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/localasr"
	"wechat-bot/internal/stationhost"
	"wechat-bot/internal/stationservices"
	"wechat-bot/internal/websession"
)

func (s *stationWechat) attachment(identity stationservices.OwnerReader) stationhost.Attachment {
	return stationhost.Attachment{
		Mount: func(owner *websession.Store, mux *http.ServeMux) {
			routes := wechatapp.ManagementHandlers{
				Contacts: s.contactHandler(owner, "contacts"), Groups: s.contactHandler(owner, "groups"),
				GroupNotification: s.stationGroupDetails("mute"), GroupProfile: s.stationGroupDetails("rename", "my_name"),
				SubmitGroupAction: s.submitGroupActionHandler(owner, identity),
				ReadRecall:        s.readRecallHandler, SubmitRecall: s.submitRecallHandler(owner, identity),
				CancelDelivery: s.cancelDelivery, ResumeDelivery: s.requestDeliveryResume,
				AcquireAttachment: s.acquireAttachment, DownloadAttachment: s.downloadAttachment,
				ListWorkGrants: s.workGrants, SaveWorkGrant: s.saveWorkGrant, RevokeWorkGrant: s.revokeWorkGrant,
				ShareSettings: func(w http.ResponseWriter, r *http.Request) {
					s.stationMediaSettings(w, r, wechatapp.ShareReaderToolID)
				},
				MediaSettings: func(w http.ResponseWriter, r *http.Request) {
					s.stationMediaSettings(w, r, localasr.PluginID)
				},
				PreviewImport: s.previewLegacyImportHandler, ApplyImport: s.applyLegacyImportHandler,
				PreviewMemory: s.previewLegacyMemoryHandler, ImportMemory: s.legacyMemoryImportHandler,
				ImportGroupPreference: s.legacyGroupSettingHandler(false), ImportGroupRule: s.legacyGroupSettingHandler(true),
				Activation: s.legacyActivationHandler, Rollback: s.legacyRollbackHandler,
				RollbackAudit: s.legacyRollbackAuditHandler, RelocationReadiness: s.legacyRelocationReadinessHandler,
				Archive: s.archiveManagementHandlers(),
			}
			routes.Mount(mux, owner.Protect)
		},
		Restore:   s.restore,
		Lifecycle: s.lifecycle,
	}
}
