package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const agentGOSessionCookieName = "agentgo_session"

// ensureWebSecurityConfigFields migrates older config.json files without
// disturbing fields that this AgentGO build does not know about.
func ensureWebSecurityConfigFields(filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	changed := false
	if _, ok := raw["password"]; !ok {
		raw["password"] = ""
		changed = true
	}
	if _, ok := raw["failed_password_count"]; !ok {
		raw["failed_password_count"] = 0
		changed = true
	}
	if !changed {
		return nil
	}
	encoded, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return atomicWriteFile(filename, encoded, 0o644)
}

func persistWebSecurityConfigFields(filename, password string, failedCount int) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	raw["password"] = password
	raw["failed_password_count"] = failedCount
	encoded, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return atomicWriteFile(filename, encoded, 0o644)
}

func securePasswordEqual(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func newWebSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func resolveConfiguredWebPassword(configured string) string {
	if configured == "" {
		return ""
	}
	if envValue, ok := os.LookupEnv(configured); ok && envValue != "" {
		return envValue
	}
	return configured
}

func (a *App) webPasswordState() (password string, failedCount int) {
	a.mu.RLock()
	configured := a.cfg.Password
	failedCount = a.cfg.FailedPasswordCount
	a.mu.RUnlock()
	return resolveConfiguredWebPassword(configured), failedCount
}

func (a *App) passwordProtectionEnabled() bool {
	password, _ := a.webPasswordState()
	return password != ""
}

func (a *App) webPasswordLocked() bool {
	password, failedCount := a.webPasswordState()
	return password != "" && failedCount > 3
}

func (a *App) hasValidWebSession(r *http.Request) bool {
	cookie, err := r.Cookie(agentGOSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	a.authMu.RLock()
	_, ok := a.authSessions[cookie.Value]
	a.authMu.RUnlock()
	return ok
}

func (a *App) createWebSession(w http.ResponseWriter, r *http.Request) error {
	token, err := newWebSessionToken()
	if err != nil {
		return err
	}
	a.authMu.Lock()
	if a.authSessions == nil {
		a.authSessions = map[string]struct{}{}
	}
	a.authSessions[token] = struct{}{}
	a.authMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     agentGOSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (a *App) setFailedPasswordCount(value int) error {
	if value < 0 {
		value = 0
	}
	a.mu.Lock()
	password := a.cfg.Password
	a.cfg.FailedPasswordCount = value
	a.mu.Unlock()
	return persistWebSecurityConfigFields(a.configPath, password, value)
}

func (a *App) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	password, failedCount := a.webPasswordState()
	if password == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "password_required": false})
		return
	}
	if failedCount > 3 {
		writeJSON(w, http.StatusLocked, map[string]any{"ok": false, "locked": true})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	if !securePasswordEqual(req.Password, password) {
		failedCount++
		if err := a.setFailedPasswordCount(failedCount); err != nil {
			http.Error(w, "could not update password attempt count", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "locked": failedCount > 3, "failed_password_count": failedCount})
		return
	}
	if err := a.setFailedPasswordCount(0); err != nil {
		http.Error(w, "could not reset password attempt count", http.StatusInternalServerError)
		return
	}
	if err := a.createWebSession(w, r); err != nil {
		http.Error(w, "could not create login session", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeAgentGOPasswordPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentGO Login</title><style>
html,body{height:100%;margin:0;background:#fff;color:#151515;font-family:Arial,Helvetica,sans-serif}.wrap{height:100%;display:flex;align-items:center;justify-content:center}.box{width:min(360px,calc(100vw - 48px));text-align:center}.title{font-size:25px;font-weight:700;margin-bottom:22px}.password{box-sizing:border-box;width:100%;height:44px;border:1px solid #aaa;border-radius:6px;padding:0 12px;font-size:17px}.button{margin-top:12px;width:100%;height:42px;border:0;border-radius:6px;background:#171717;color:white;font-size:16px;cursor:pointer}.error{min-height:20px;margin-top:12px;color:#a00000;font-size:14px}</style></head><body><div class="wrap"><form class="box" id="loginForm"><div class="title">AgentGO</div><input class="password" id="password" type="password" autocomplete="current-password" autofocus placeholder="Password"><button class="button" type="submit">Enter</button><div class="error" id="error"></div></form></div><script>
const form=document.getElementById('loginForm'),input=document.getElementById('password'),error=document.getElementById('error');
form.addEventListener('submit',async(e)=>{e.preventDefault();error.textContent='';try{const r=await fetch('/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:input.value})});const data=await r.json().catch(()=>({}));if(r.ok){location.reload();return;}if(data.locked){location.reload();return;}error.textContent='Incorrect password.';input.value='';input.focus();}catch(_){error.textContent='Could not contact AgentGO.';}});
</script></body></html>`)
}

func writeAgentGOLockedPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusLocked)
	_, _ = fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentGO Locked</title><style>html,body{height:100%;margin:0;background:#fff;color:#151515;font-family:Arial,Helvetica,sans-serif}.wrap{height:100%;display:flex;align-items:center;justify-content:center;padding:24px;box-sizing:border-box}.msg{max-width:620px;text-align:center;font-size:20px;line-height:1.5}</style></head><body><div class="wrap"><div class="msg">AgentGO access is locked.<br>Set <strong>failed_password_count</strong> to <strong>0</strong> in config.json and restart AgentGO.</div></div></body></html>`)
}

// withWebPasswordProtection gates the browser-facing AgentGO interface. The
// existing /outfits/ public API remains unchanged in r450; API authentication
// is intentionally reserved for a separate patch.
func (a *App) withWebPasswordProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.passwordProtectionEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/outfits/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/auth/login" {
			a.handleWebLogin(w, r)
			return
		}
		if a.webPasswordLocked() {
			if r.Method == http.MethodGet && r.URL.Path == "/" {
				writeAgentGOLockedPage(w)
				return
			}
			writeJSON(w, http.StatusLocked, map[string]any{"error": "AgentGO access is locked. Set failed_password_count to 0 in config.json and restart AgentGO."})
			return
		}
		if !a.hasValidWebSession(r) {
			if r.Method == http.MethodGet && r.URL.Path == "/" {
				writeAgentGOPasswordPage(w)
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "AgentGO is shutting down."})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		select {
		case a.shutdownCh <- struct{}{}:
		default:
		}
	}()
}

func isExpectedServerClose(err error) bool {
	return err == nil || errors.Is(err, http.ErrServerClosed)
}
