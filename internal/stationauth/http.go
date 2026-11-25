package stationauth

import (
	"errors"
	"net/http"
	"wechat-bot/internal/websession"
)

func RegisterPassword(mux *http.ServeMux, owner *websession.Store, dir string) {
	mux.HandleFunc("POST /api/password", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Current string `json:"current_password"`
			New     string `json:"new_password"`
		}
		if !websession.Decode(w, r, &input) {
			return
		}
		err := owner.ChangePassword(r, input.Current, input.New, func() error { return SavePassword(dir, input.New) })
		switch {
		case errors.Is(err, websession.ErrPassword):
			websession.Fail(w, 400, "新密码应为5至128字节，首尾不能有空白或换行")
		case errors.Is(err, websession.ErrCurrent):
			websession.Fail(w, 400, "当前管理密码不正确")
		case errors.Is(err, websession.ErrSession):
			websession.Fail(w, 401, "请重新登录")
		case err != nil:
			websession.Fail(w, 500, "密码未保存，原密码仍然有效")
		default:
			owner.ClearCookie(w, r)
			websession.Respond(w, 200, map[string]bool{"ok": true, "reauthenticate": true})
		}
	}))
}
