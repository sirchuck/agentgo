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
		if err := a.createWebSession(w, r); err != nil {
			http.Error(w, "could not create startup session", http.StatusInternalServerError)
			return
		}
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

func writeAgentGOPasswordPage(w http.ResponseWriter, passwordRequired bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	passwordControl := `<input class="password" id="password" type="password" autocomplete="current-password" autofocus placeholder="Password">`
	if !passwordRequired {
		passwordControl = `<div class="no-password-note" role="note">You can set a password in your <strong>config.json</strong> file.</div>`
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentGO Login</title><link rel="icon" href="/favicon.ico" type="image/x-icon"><style>
html,body{height:100%%;margin:0;color:#f7fbff;font-family:Arial,Helvetica,sans-serif}body{background-color:#071322;background-image:linear-gradient(rgba(3,10,22,.14),rgba(3,10,22,.24)),url('/assets/yetiagent.png');background-position:center center;background-size:cover;background-repeat:no-repeat;background-attachment:fixed}.yeti-stars-canvas{position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none;z-index:0}.wrap{position:relative;z-index:1;min-height:100%%;display:flex;align-items:center;justify-content:center;padding:28px;box-sizing:border-box}.box{box-sizing:border-box;width:min(430px,calc(100vw - 42px));padding:34px 34px 30px;text-align:center;background:rgba(7,18,37,.76);border:1px solid rgba(173,208,255,.24);border-radius:24px;box-shadow:0 24px 70px rgba(0,0,0,.52),inset 0 1px 0 rgba(255,255,255,.08);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}.title{font-size:31px;font-weight:800;letter-spacing:.01em;margin-bottom:24px;color:#fff;text-shadow:0 2px 12px rgba(0,0,0,.55)}.password{box-sizing:border-box;width:100%%;height:48px;border:1px solid rgba(184,208,240,.72);border-radius:10px;padding:0 14px;font-size:17px;background:rgba(255,255,255,.97);color:#13233b;outline:none;box-shadow:0 7px 20px rgba(0,0,0,.16)}.password:focus{border-color:#7db8ff;box-shadow:0 0 0 3px rgba(72,151,245,.25),0 7px 20px rgba(0,0,0,.16)}.no-password-note{box-sizing:border-box;width:100%%;padding:14px 16px;border:1px solid rgba(164,199,238,.28);border-radius:12px;background:rgba(12,33,61,.76);color:#eef7ff;font-size:15px;line-height:1.45;text-align:left;box-shadow:inset 0 1px 0 rgba(255,255,255,.05)}.no-password-note strong{color:#fff}.button{margin-top:14px;width:100%%;height:46px;border:1px solid rgba(137,190,255,.28);border-radius:10px;background:linear-gradient(180deg,#2d78c5,#16528f);color:white;font-size:16px;font-weight:700;cursor:pointer;box-shadow:0 11px 28px rgba(0,34,75,.42),inset 0 1px 0 rgba(255,255,255,.18)}.button:hover{filter:brightness(1.08)}.button:focus-visible{outline:3px solid rgba(126,190,255,.48);outline-offset:2px}.error{min-height:20px;margin-top:12px;color:#ffb8b8;font-size:14px;font-weight:600;text-shadow:0 1px 4px rgba(0,0,0,.5)}@media(max-width:560px){.wrap{padding:18px}.box{padding:28px 22px 24px}.title{font-size:28px}}
</style></head><body><canvas class="yeti-stars-canvas" data-yeti-stars aria-hidden="true"></canvas><div class="wrap"><form class="box" id="loginForm"><div class="title">AgentGO</div>%s<button class="button" type="submit">Enter</button><div class="error" id="error"></div></form></div><script>
const form=document.getElementById('loginForm'),input=document.getElementById('password'),error=document.getElementById('error');
form.addEventListener('submit',async(e)=>{e.preventDefault();error.textContent='';try{const r=await fetch('/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:input?input.value:''})});const data=await r.json().catch(()=>({}));if(r.ok){location.reload();return;}if(data.locked){location.reload();return;}error.textContent='Incorrect password.';if(input){input.value='';input.focus();}}catch(_){error.textContent='Could not contact AgentGO.';}});
</script><script src="/assets/yeti-stars.js"></script></body></html>`, passwordControl)
}

func writeAgentGOLockedPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusLocked)
	_, _ = fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentGO Locked</title><link rel="icon" href="/favicon.ico" type="image/x-icon"><style>html,body{height:100%;margin:0;background:#fff;color:#151515;font-family:Arial,Helvetica,sans-serif}.wrap{height:100%;display:flex;align-items:center;justify-content:center;padding:24px;box-sizing:border-box}.msg{max-width:620px;text-align:center;font-size:20px;line-height:1.5}</style></head><body><div class="wrap"><div class="msg">AgentGO access is locked.<br>Set <strong>failed_password_count</strong> to <strong>0</strong> in config.json and restart AgentGO.</div></div></body></html>`)
}

// withWebPasswordProtection gates the browser-facing AgentGO interface. The
// existing /outfits/ public API remains unchanged in r450; API authentication
// is intentionally reserved for a separate patch.
func (a *App) withWebPasswordProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The startup artwork must remain reachable before a web session exists.
		if r.URL.Path == "/favicon.ico" || r.URL.Path == "/assets/yetiagent.png" || r.URL.Path == "/assets/yeti-stars.js" {
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

		passwordEnabled := a.passwordProtectionEnabled()
		if !passwordEnabled {
			// Even password-free installs get the startup splash so users discover
			// where password protection is configured. This acknowledgement does
			// not turn on API authentication when no password is configured.
			if r.Method == http.MethodGet && r.URL.Path == "/" && !a.hasValidWebSession(r) {
				writeAgentGOPasswordPage(w, false)
				return
			}
			next.ServeHTTP(w, r)
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
				writeAgentGOPasswordPage(w, true)
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
