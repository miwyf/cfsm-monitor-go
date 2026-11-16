package main

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"net/http"
	"strings"
	"time"
)

func (a *app) setupRequired() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.passwordHash) == 0
}

func accountValidation(username, password string) string {
	if username == "" || len(username) > 64 || strings.ContainsAny(username, "\r\n\t") {
		return "账号长度为 1–64 字符"
	}
	if len(password) < 6 || len(password) > 1024 {
		return "密码长度为 6–1024 字符"
	}
	return ""
}

func (a *app) setup(w http.ResponseWriter, r *http.Request) {
	if !a.setupRequired() {
		problem(w, 409, "管理员已创建，请登录")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Confirm  string `json:"confirm_password"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if message := accountValidation(input.Username, input.Password); message != "" {
		problem(w, 400, message)
		return
	}
	if input.Password != input.Confirm {
		problem(w, 400, "两次密码不一致")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.passwordHash) != 0 {
		problem(w, 409, "管理员已创建，请登录")
		return
	}
	salt := randomToken()
	hash, err := pbkdf2.Key(sha256.New, input.Password, []byte(salt), 600000, 32)
	if err != nil {
		problem(w, 500, "账号创建失败")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `INSERT OR IGNORE INTO administrator(id,username,password_hash,salt) VALUES(1,?,?,?)`, input.Username, hash, salt)
	if err != nil {
		problem(w, 500, "账号保存失败")
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		problem(w, 409, "管理员已创建，请登录")
		return
	}
	a.username, a.passwordHash, a.salt = input.Username, hash, salt
	token := randomToken()
	a.sessions[token] = session{time.Now().Add(12 * time.Hour)}
	a.cookie(w, token, 12*60*60)
	respond(w, 201, map[string]bool{"admin": true})
}
