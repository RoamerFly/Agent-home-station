package station

import (
	"context"
	"encoding/json"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/linuxnative"
)

func (t *stationAttachmentTools) historyAllowed(ctx context.Context) bool {
	return ctx.Err() == nil && t.store != nil && t.native != nil && t.scopeAllowed(t.trace, t.reads)
}

func (t *stationAttachmentTools) historyAccess() wechatapp.HistoryAccess {
	return wechatapp.HistoryAccess{
		Native: t.native, Account: t.trace.Account, Thread: t.trace.Thread, Grant: t.reads[t.trace.Thread],
		Authorized: t.historyAllowed, RecallEpoch: t.store.RecallEpoch, RecallAllowed: t.store.NativeRecallAllowed,
		ProtectMessage: t.protectCrossReply,
	}
}

func (t *stationAttachmentTools) historyMessage(ctx context.Context, id string) (linuxnative.Message, bool, error) {
	return t.historyAccess().Message(ctx, id)
}

func (t *stationAttachmentTools) readHistory(ctx context.Context, raw json.RawMessage) (string, error) {
	return t.historyAccess().ReadCatalog(ctx, raw, &t.readCursors)
}

func (t *stationAttachmentTools) readMessage(ctx context.Context, raw json.RawMessage) (string, error) {
	return t.historyAccess().ReadMessage(ctx, raw)
}
