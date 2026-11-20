package station

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const (
	aliyunBalanceRoute = "/api/local/project-tools/media-understanding/aliyun-balance"
	aliyunBillingHost  = "business.aliyuncs.com"
	aliyunBillingFile  = "aliyun-billing.json"
)

type aliyunBillingCredential struct {
	ID     string `json:"access_key_id"`
	Secret string `json:"access_key_secret"`
}

func (c aliyunBillingCredential) valid() bool {
	return len(c.ID) > 0 && len(c.ID) <= 128 && !strings.ContainsAny(c.ID, ",;= \t") &&
		localVisionText(c.ID, 128) && len(c.Secret) > 0 && localVisionText(c.Secret, 256) && strings.TrimSpace(c.Secret) == c.Secret
}

type aliyunBalance struct {
	Cash      string `json:"cash_balance"`
	Available string `json:"available_amount"`
	Currency  string `json:"currency"`
}

var (
	aliyunMoneyPattern   = regexp.MustCompile(`^-?[0-9]{1,24}(\.[0-9]{1,12})?$`)
	errAliyunPermission  = errors.New("阿里云拒绝了余额查询，请核对 RAM 用户的 bssapi:QueryAccountBalance 权限")
	errAliyunCredential  = errors.New("阿里云未接受财务凭据，请核对 RAM AccessKey ID 和 Secret")
	errAliyunUnavailable = errors.New("阿里云余额暂不可查，请稍后重试")
)

func aliyunBalanceRequest(ctx context.Context, cred aliyunBillingCredential, now time.Time, nonce string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+aliyunBillingHost+"/", nil)
	if err != nil {
		return nil, err
	}
	// QueryAccountBalance is a bodyless RPC. ACS3 signs its fixed host and headers.
	const algorithm = "ACS3-HMAC-SHA256"
	empty := sha256.Sum256(nil)
	contentHash := hex.EncodeToString(empty[:])
	headers := map[string]string{
		"host": aliyunBillingHost, "x-acs-action": "QueryAccountBalance", "x-acs-version": "2017-12-14",
		"x-acs-date": now.UTC().Format(time.RFC3339), "x-acs-signature-nonce": nonce, "x-acs-content-sha256": contentHash,
		"content-type": "application/x-www-form-urlencoded",
	}
	names := []string{"content-type", "host", "x-acs-action", "x-acs-content-sha256", "x-acs-date", "x-acs-signature-nonce", "x-acs-version"}
	var canonical strings.Builder
	for _, name := range names {
		canonical.WriteString(name + ":" + headers[name] + "\n")
		if name != "host" {
			req.Header.Set(name, headers[name])
		}
	}
	signed := strings.Join(names, ";")
	digest := sha256.Sum256([]byte("POST\n/\n\n" + canonical.String() + "\n" + signed + "\n" + contentHash))
	mac := hmac.New(sha256.New, []byte(cred.Secret))
	mac.Write([]byte(algorithm + "\n" + hex.EncodeToString(digest[:])))
	req.Header.Set("Authorization", algorithm+" Credential="+cred.ID+",SignedHeaders="+signed+",Signature="+hex.EncodeToString(mac.Sum(nil)))
	return req, nil
}

func queryAliyunBalance(ctx context.Context, client *http.Client, cred aliyunBillingCredential) (aliyunBalance, error) {
	var nonce [16]byte
	rand.Read(nonce[:])
	req, err := aliyunBalanceRequest(ctx, cred, time.Now(), hex.EncodeToString(nonce[:]))
	if err != nil {
		return aliyunBalance{}, errAliyunUnavailable
	}
	response, err := client.Do(req)
	if err != nil {
		return aliyunBalance{}, errAliyunUnavailable
	}
	defer response.Body.Close()
	const limit = 64 << 10
	var body struct {
		Code    string
		Success bool
		Data    struct{ AvailableCashAmount, AvailableAmount, Currency string }
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &body) != nil {
		return aliyunBalance{}, errAliyunUnavailable
	}
	switch body.Code {
	case "NoPermission", "NotAuthorized", "Forbidden", "UnauthorizedOperation":
		return aliyunBalance{}, errAliyunPermission
	case "InvalidAccessKeyId.NotFound", "InvalidAccessKeyId", "InvalidAccessKeyId.Inactive", "SignatureDoesNotMatch":
		return aliyunBalance{}, errAliyunCredential
	}
	if response.StatusCode != http.StatusOK || !body.Success || !slices.Contains([]string{"CNY", "USD", "JPY"}, body.Data.Currency) ||
		!aliyunMoneyPattern.MatchString(body.Data.AvailableCashAmount) || !aliyunMoneyPattern.MatchString(body.Data.AvailableAmount) {
		return aliyunBalance{}, errAliyunUnavailable
	}
	return aliyunBalance{Cash: body.Data.AvailableCashAmount, Available: body.Data.AvailableAmount, Currency: body.Data.Currency}, nil
}

func readAliyunBilling(rootPath string) (aliyunBillingCredential, error) {
	var credential aliyunBillingCredential
	root, err := workspacefs.OpenPath(rootPath)
	if err != nil {
		return credential, err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, aliyunBillingFile)
	if err != nil {
		return credential, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &credential) != nil || !credential.valid() {
		return aliyunBillingCredential{}, errors.New("invalid billing credential record")
	}
	return credential, nil
}

func (s *localProjectTools) aliyunBillingRoutes(owner *websession.Store, mux *http.ServeMux) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
		mux.HandleFunc(method+" "+aliyunBalanceRoute, owner.Protect(s.aliyunBilling))
	}
}

func (s *localProjectTools) aliyunBilling(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, err := s.home.applicationOwner(); err != nil {
		websession.Fail(w, 409, "请先准备本机主人身份")
		return
	}
	dir := filepath.Join(stationToolDataDir(s.home.dataDir), "media-understanding")
	if r.Method == http.MethodPut {
		var input struct {
			aliyunBillingCredential
			Clear bool `json:"clear"`
		}
		if !websession.Decode(w, r, &input) {
			return
		}
		if input.Clear && (input.ID != "" || input.Secret != "") || !input.Clear && !input.aliyunBillingCredential.valid() {
			websession.Fail(w, 400, "请填写一对有效的 AccessKey ID 和 Secret，或单独选择清除凭据")
			return
		}
		s.update.Lock()
		err := saveAliyunBilling(dir, input.aliyunBillingCredential, input.Clear)
		s.update.Unlock()
		if err != nil {
			websession.Fail(w, 503, "财务凭据未更新，请检查工作站数据目录")
			return
		}
		websession.Respond(w, 200, map[string]bool{"configured": !input.Clear})
		return
	}
	cred, err := readAliyunBilling(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		websession.Fail(w, 503, "财务凭据不可读，请重新填写配置")
		return
	}
	if r.Method == http.MethodGet {
		websession.Respond(w, 200, map[string]bool{"configured": err == nil})
		return
	}
	if err != nil {
		websession.Fail(w, 409, "请先保存阿里云财务凭据")
		return
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	result, err := queryAliyunBalance(r.Context(), client, cred)
	if err != nil {
		websession.Fail(w, 502, err.Error())
		return
	}
	if current, err := readAliyunBilling(dir); err != nil || current != cred {
		websession.Fail(w, 409, "财务凭据已变更，请重新查询")
		return
	}
	websession.Respond(w, 200, result)
}

func saveAliyunBilling(dir string, credential aliyunBillingCredential, clear bool) error {
	if clear {
		root, err := workspacefs.OpenPath(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer root.Close()
		err = root.Remove(aliyunBillingFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	return hostfile.WritePrivate(filepath.Join(root.Name(), aliyunBillingFile), raw)
}
