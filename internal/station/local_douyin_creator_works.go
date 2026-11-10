package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

type localCreatorWorksCursor struct {
	Request          stationbrowser.DouyinCreatorWorksRequest
	PageKey, Account string
	Offset, Limit    int
}

type localCreatorWorkSnapshot struct {
	work     stationbrowser.DouyinCreatorWork
	grant    string
	captured time.Time
}

type localCreatorWorksIndex struct {
	mu                              sync.Mutex
	items                           map[string]localCreatorWorkSnapshot
	profileAccount, platformAccount string
}

func (i *localCreatorWorksIndex) add(profileAccount, platformAccount, grant string, rows []stationbrowser.DouyinCreatorWork, now time.Time) ([]string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.profileAccount == profileAccount && i.platformAccount != platformAccount {
		clear(i.items)
		return nil, apphost.ErrObservationWebSnapshotChanged
	}
	if i.items == nil || i.profileAccount != profileAccount {
		i.items = map[string]localCreatorWorkSnapshot{}
		i.profileAccount, i.platformAccount = profileAccount, platformAccount
	}
	if len(rows) > 1000 {
		return nil, apphost.ErrObservationAccountWorksResponseSize
	}
	for id, row := range i.items {
		if row.grant != grant || now.Sub(row.captured) >= 20*time.Minute {
			delete(i.items, id)
		}
	}
	for len(i.items)+len(rows) > 1000 && len(i.items) > 0 {
		oldest := ""
		for id, item := range i.items {
			if oldest == "" || item.captured.Before(i.items[oldest].captured) {
				oldest = id
			}
		}
		delete(i.items, oldest)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		token, err := localDouyinWebID()
		if err != nil {
			return nil, err
		}
		id := douyinapp.WebCreatorWorkPrefix + token
		ids = append(ids, id)
		i.items[id] = localCreatorWorkSnapshot{work: row, grant: grant, captured: now}
	}
	return ids, nil
}

func (i *localCreatorWorksIndex) get(id, grant string) (localCreatorWorkSnapshot, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	row, ok := i.items[id]
	return row, ok && row.grant == grant && time.Since(row.captured) < 20*time.Minute
}

func (p *localDouyinWebObservation) readCreatorWorks(ctx context.Context, access localDouyinWebAccess, in apphost.ObservationRequest, authorize func() error) (apphost.ObservationPage, error) {
	var cursor localCreatorWorksCursor
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil || len(raw) > 2048 || strictLocalMemoryDecode(raw, &cursor) != nil || cursor.Offset < 0 ||
			cursor.Limit < 1 || cursor.Request.Page < 0 || in.Limit != 0 && cursor.Limit != in.Limit {
			return apphost.ObservationPage{}, apphost.ErrObservationAccountWorksPageSize
		}
	} else {
		cursor.Limit = in.Limit
		if cursor.Limit == 0 {
			cursor.Limit = 20
		}
	}
	manager := p.home.douyinWeb
	manager.mu.Lock()
	read, service := manager.readWorks, manager.ctx
	manager.mu.Unlock()
	if read == nil {
		return apphost.ObservationPage{}, apphost.ErrObservationBrowserUnavailable
	}
	readCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(service, cancel)
	defer stop()
	if err := authorize(); err != nil {
		return apphost.ObservationPage{}, err
	}
	result, err := read(readCtx, access.profile, cursor.Request, authorize)
	if authErr := authorize(); authErr != nil {
		return apphost.ObservationPage{}, authErr
	}
	switch {
	case errors.Is(err, stationbrowser.ErrCreatorWorksChanged):
		return apphost.ObservationPage{}, apphost.ErrObservationWebSnapshotChanged
	case errors.Is(err, stationbrowser.ErrCreatorWorksNextUnavailable), errors.Is(err, stationbrowser.ErrCreatorWorksUnavailable) && !errors.Is(err, context.DeadlineExceeded):
		return apphost.ObservationPage{}, errors.Join(apphost.ErrObservationContentUnavailable, err)
	case err != nil:
		return apphost.ObservationPage{}, localDouyinWebReadError(err)
	}
	if result.Account == "" || result.PageKey == "" || cursor.Offset > len(result.Works) ||
		cursor.Account != "" && cursor.Account != result.Account || cursor.PageKey != "" && cursor.PageKey != result.PageKey {
		return apphost.ObservationPage{}, apphost.ErrObservationWebSnapshotChanged
	}
	end := cursor.Offset + min(cursor.Limit, len(result.Works)-cursor.Offset)
	works := result.Works[cursor.Offset:end]
	ids, err := p.creatorWorks.add(access.connection.AccountID, result.Account, access.version, works, time.Now().UTC())
	if err != nil {
		return apphost.ObservationPage{}, err
	}
	coverage := fmt.Sprintf("已登录创作者管理页 · 第 %d 页 · 本次返回 %d 条可核对的本人作品", cursor.Request.Page+1, len(works))
	if !result.CoverageKnown {
		coverage += "。平台未报告后续页，列表总覆盖待核对"
	}
	page := apphost.ObservationPage{Coverage: coverage, Complete: end == len(result.Works) && !result.HasMore}
	mediaFields := p.mediaFollowUps(ctx, access.record)
	for index, work := range works {
		title := work.Title
		if len([]rune(title)) > 160 {
			title = string([]rune(title)[:160]) + "…"
		}
		if title == "" {
			title = "作品 " + work.ID
		}
		fields := []apphost.ObservationField{apphost.ObservationPageText}
		for _, field := range mediaFields {
			if p.webObservationAllowed(access, ids[index], field) {
				fields = append(fields, field)
			}
		}
		page.Chunks = append(page.Chunks, apphost.ObservationChunk{Position: cursor.Offset + index, Text: title + " · " + work.Visibility,
			SourceID: work.ID, SourceURL: stationbrowser.DouyinCreatorManageURL,
			FollowUpContentID: ids[index], FollowUpField: apphost.ObservationPageText, FollowUpFields: fields})
	}
	if !page.Complete {
		if end < len(result.Works) {
			cursor.Offset, cursor.Account, cursor.PageKey = end, result.Account, result.PageKey
		} else {
			cursor = localCreatorWorksCursor{Limit: cursor.Limit,
				Request: stationbrowser.DouyinCreatorWorksRequest{Page: cursor.Request.Page + 1, PreviousAccount: result.Account, PreviousPageKey: result.PageKey}}
		}
		raw, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

func creatorWorkPage(snapshot localCreatorWorkSnapshot, limit int) apphost.ObservationPage {
	row := snapshot.work
	lines := []string{"作品编号 " + row.ID, "可见范围 " + row.Visibility}
	if row.Title != "" {
		lines = append(lines, "标题 "+row.Title)
	}
	if row.Caption != "" {
		lines = append(lines, "简介 "+row.Caption)
	}
	if row.CreatedAt != "" {
		lines = append(lines, "创建时间 "+row.CreatedAt)
	}
	if row.Reviewing {
		lines = append(lines, "平台记录显示审核中")
	}
	coverage := "本人作品信息 · 创作者管理页快照 " + snapshot.captured.Format(time.RFC3339) + " · 不含图文或视频画面理解"
	if limit == 0 {
		limit = 2048
	}
	text := []rune(strings.Join(lines, "\n"))
	if len(text) > limit {
		coverage += fmt.Sprintf(" · 显示 %d 字，共 %d 字，可增大 limit 重读", limit, len(text))
		text = text[:limit]
	}
	return apphost.ObservationPage{Complete: true, Coverage: coverage,
		Chunks: []apphost.ObservationChunk{{Text: string(text), SourceID: row.ID, SourceURL: stationbrowser.DouyinCreatorManageURL}}}
}
