package linuxnative

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type CatalogSyncOperation struct {
	ID        string `json:"id"`
	AccountID string `json:"accountId"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Result    struct {
		RecordCount     int   `json:"recordCount"`
		Complete        *bool `json:"complete"`
		CatalogComplete *bool `json:"catalogComplete"`
		ResumeAvailable bool  `json:"resumeAvailable"`
		FailedItems     int   `json:"failedItems"`
	} `json:"result"`
}

func (s *Sender) catalogOperation(value CatalogSyncOperation) (CatalogSyncOperation, error) {
	if !safeID(value.ID) || value.AccountID != s.account || value.Kind != "metadata" {
		return CatalogSyncOperation{}, errors.New("catalog operation scope mismatch")
	}
	return value, nil
}

func (s *Sender) startCatalogMetadata(ctx context.Context, route string) (CatalogSyncOperation, error) {
	r, err := s.do(ctx, http.MethodPost, s.accountPath()+route, []byte(`{}`), "")
	if err != nil {
		return CatalogSyncOperation{}, errors.New("目录同步提交未确认，请先查询目录，未自动重复")
	}
	defer r.Body.Close()
	var out CatalogSyncOperation
	if r.StatusCode != http.StatusAccepted || !decodeBounded(r.Body, &out) {
		return out, errors.New("目录同步未能启动，请检查微信登录状态")
	}
	return s.catalogOperation(out)
}

func (s *Sender) StartCatalogSync(ctx context.Context, kind string) (CatalogSyncOperation, error) {
	switch kind {
	case "groups":
		return s.startCatalogMetadata(ctx, "/group-sync")
	case "contacts":
		return s.startCatalogMetadata(ctx, "/contact-sync")
	}
	return CatalogSyncOperation{}, errors.New("unsupported catalog sync")
}
func (s *Sender) RefreshGroupMetadata(ctx context.Context, id string) (CatalogSyncOperation, error) {
	if !safeThreadID(id) || !strings.HasSuffix(id, "@chatroom") {
		return CatalogSyncOperation{}, errors.New("invalid group identity")
	}
	return s.startCatalogMetadata(ctx, "/groups/"+url.PathEscape(id)+"/refresh")
}
func (s *Sender) RefreshGroupMembers(ctx context.Context, id string) (CatalogSyncOperation, error) {
	if !safeThreadID(id) || !strings.HasSuffix(id, "@chatroom") {
		return CatalogSyncOperation{}, errors.New("invalid group identity")
	}
	return s.startCatalogMetadata(ctx, "/groups/"+url.PathEscape(id)+"/members/refresh")
}
func (s *Sender) CatalogSyncStatus(ctx context.Context, id string) (CatalogSyncOperation, error) {
	if !safeID(id) {
		return CatalogSyncOperation{}, errors.New("invalid catalog operation")
	}
	var out CatalogSyncOperation
	if err := s.readJSON(ctx, s.accountPath()+"/operations/"+url.PathEscape(id), &out); err != nil {
		return out, errors.New("无法读取目录同步状态")
	}
	if out.ID != id {
		return CatalogSyncOperation{}, errors.New("catalog operation identity mismatch")
	}
	return s.catalogOperation(out)
}
