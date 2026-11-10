package station

import (
	"context"
	"errors"
	"net/http"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/localasr"
	"wechat-bot/internal/stationhost"
	"wechat-bot/internal/stationservices"
	"wechat-bot/internal/websession"
)

func (s *stationWechat) attachment(identity stationservices.OwnerReader) stationhost.Attachment {
	return stationhost.Attachment{
		Mount: func(owner *websession.Store, mux *http.ServeMux) {
			actions := s.managementActions(owner, identity)
			routes := wechatapp.ManagementHandlers{
				Contacts: s.contactHandler(owner, "contacts"), Groups: s.contactHandler(owner, "groups"),
				GroupNotification: actions.GroupDetails("mute"), GroupProfile: actions.GroupDetails("rename", "my_name"),
				SubmitGroupAction: actions.SubmitGroup,
				ReadRecall:        actions.ReadRecall, SubmitRecall: actions.SubmitRecall,
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

func (s *stationWechat) managementActions(session *websession.Store, readOwner stationservices.OwnerReader) wechatapp.ActionManagement {
	return wechatapp.ActionManagement{
		Shutdown: s.ctx,
		Owner: func(r *http.Request) (wechatapp.ManagementOwner, error) {
			record, err := readOwner()
			if err != nil {
				return wechatapp.ManagementOwner{}, err
			}
			return wechatapp.ManagementOwner{ID: record.ID, Active: func() bool {
				_, _, ok := session.Lookup(r)
				current, err := readOwner()
				return ok && err == nil && current == record
			}}, nil
		},
		ApplyGroup: func(ctx context.Context, id string, action linuxnative.AdminAction, key string, owner wechatapp.ManagementOwner) (apphost.ActionReceipt, error) {
			out, err := s.submitStationGroupAction(ctx, id, action, key, stationGroupActionAuthority{
				requester: owner.ID, active: func(context.Context) bool { return owner.Active() },
			})
			return out, wechatManagementError(err)
		},
		ReadGroup: s.stationGroupDetails,
		LookupRecall: func(ctx context.Context, id, key, server, cursor string) (wechatapp.RecallView, error) {
			source, err := s.stationRecallEvidence(ctx, id, key, server)
			if err != nil {
				return wechatapp.RecallView{}, wechatManagementError(err)
			}
			out, err := s.readStationRecall(ctx, id, source, cursor)
			return out, wechatManagementError(err)
		},
		ApplyRecall: func(ctx context.Context, id, key, server, message string, owner wechatapp.ManagementOwner) (apphost.ActionReceipt, error) {
			source, err := s.stationRecallEvidence(ctx, id, key, server)
			if err != nil {
				return apphost.ActionReceipt{}, wechatManagementError(err)
			}
			out, err := s.submitStationRecall(ctx, id, source, message, owner.ID, owner.Active)
			return out, wechatManagementError(err)
		},
	}
}

func wechatManagementError(err error) error {
	if errors.Is(err, wechatapp.ErrDirectoryLogin) {
		return apphost.ErrLifecycleConflict
	}
	return err
}
