package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Watchlist is a local-only user interest filter; it never modifies the upstream mailbox.
const (
	mailWatchlistMaxTerms     = 100
	mailWatchlistMaxTermRunes = 160
	mailWatchlistMaxBodyBytes = 2 * 1024 * 1024
)

func normalizeMailWatchlistTerms(items []MailWatchlistTerm) []MailWatchlistTerm {
	clean := make([]MailWatchlistTerm, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		item.Term = strings.TrimSpace(item.Term)
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		if item.Type != "positive" && item.Type != "negative" {
			continue
		}
		if item.Term == "" || utf8.RuneCountInString(item.Term) > mailWatchlistMaxTermRunes {
			continue
		}
		key := item.Type + "\x00" + strings.ToLower(item.Term)
		if seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, item)
		if len(clean) >= mailWatchlistMaxTerms {
			break
		}
	}
	return clean
}

func validateMailWatchlistTerms(items []MailWatchlistTerm) error {
	if len(items) > mailWatchlistMaxTerms {
		return fmt.Errorf("Watchlist supports at most %d terms", mailWatchlistMaxTerms)
	}
	for _, item := range items {
		if strings.TrimSpace(item.Term) == "" || utf8.RuneCountInString(strings.TrimSpace(item.Term)) > mailWatchlistMaxTermRunes {
			return errors.New("Watchlist terms must contain 1-160 characters")
		}
		if item.Type != "positive" && item.Type != "negative" {
			return errors.New("Watchlist term type must be positive or negative")
		}
	}
	return nil
}

// At least one positive substring must match subject/body, with no negative matches.
func mailWatchlistMatches(subject, body string, terms []MailWatchlistTerm) bool {
	if len(terms) == 0 {
		return false
	}
	haystack := strings.ToLower(subject + "\n" + body)
	positive := false
	for _, item := range terms {
		phrase := strings.ToLower(strings.TrimSpace(item.Term))
		if phrase == "" || !strings.Contains(haystack, phrase) {
			continue
		}
		if item.Type == "negative" {
			return false
		}
		if item.Type == "positive" {
			positive = true
		}
	}
	return positive
}

func (m *mailManager) watchlistBodyDir() string { return filepath.Join(m.root, "watchlist") }
func (m *mailManager) watchlistBodyPath(id string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return filepath.Join(m.watchlistBodyDir(), fmt.Sprintf("%x.gz", digest[:]))
}

// Store full cleaned body text separately from the lightweight JSON index,
// compressed, on the local machine. File name is a hash of the local message ID.
func (m *mailManager) writeWatchlistBody(id, body string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	if len(body) > mailWatchlistMaxBodyBytes {
		body = string([]rune(body)[:min(utf8.RuneCountInString(body), mailWatchlistMaxBodyBytes/4)])
	}
	if err := ensureDir(m.watchlistBodyDir()); err != nil {
		return err
	}
	path := m.watchlistBodyPath(id)
	// Write via a private temporary file then rename, avoiding partial gzip files.
	f, err := os.CreateTemp(m.watchlistBodyDir(), "watchlist-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	z := gzip.NewWriter(f)
	_, err = io.WriteString(z, body)
	if zerr := z.Close(); err == nil {
		err = zerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *mailManager) readWatchlistBody(id string) (string, bool) {
	f, err := os.Open(m.watchlistBodyPath(id))
	if err != nil {
		return "", false
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return "", false
	}
	defer z.Close()
	b, err := io.ReadAll(io.LimitReader(z, mailWatchlistMaxBodyBytes+1))
	if err != nil || len(b) > mailWatchlistMaxBodyBytes {
		return "", false
	}
	return string(b), true
}

// Legacy r497 messages may only have an excerpt; use any temporarily cached
// full body where possible, with the excerpt as an offline fallback.
func (m *mailManager) watchlistTextForRecord(record MailMessageRecord, cacheDays int) string {
	if body, ok := m.readWatchlistBody(record.ID); ok {
		return body
	}
	if body, ok, err := m.readBodyCache(record.ID, cacheDays); err == nil && ok {
		return body
	}
	return record.Excerpt
}

func (m *mailManager) ingestWatchlistBodies(index *mailIndexFile, terms []MailWatchlistTerm) error {
	if index == nil {
		return nil
	}
	for i := range index.Messages {
		item := &index.Messages[i]
		if item.watchlistBody == "" {
			continue
		}
		if err := m.writeWatchlistBody(item.ID, item.watchlistBody); err != nil {
			return err
		}
		item.WatchlistMatch = mailWatchlistMatches(item.Subject, item.watchlistBody, terms)
		item.watchlistBody = ""
	}
	return nil
}

func (m *mailManager) rebuildWatchlistMatches(index *mailIndexFile, terms []MailWatchlistTerm, cacheDays int) {
	if index == nil {
		return
	}
	for i := range index.Messages {
		item := &index.Messages[i]
		if len(terms) == 0 {
			item.WatchlistMatch = false
			continue
		}
		item.WatchlistMatch = mailWatchlistMatches(item.Subject, m.watchlistTextForRecord(*item, cacheDays), terms)
	}
}

func (m *mailManager) updateWatchlistBodyForMessage(id, body string, terms []MailWatchlistTerm) error {
	if err := m.writeWatchlistBody(id, body); err != nil {
		return err
	}
	index, err := readMailIndex(m.root)
	if err != nil {
		return err
	}
	for i := range index.Messages {
		if index.Messages[i].ID != id {
			continue
		}
		newMatch := mailWatchlistMatches(index.Messages[i].Subject, body, terms)
		if index.Messages[i].WatchlistMatch != newMatch {
			index.Messages[i].WatchlistMatch = newMatch
			return m.saveIndex(index)
		}
		break
	}
	return nil
}

// This endpoint changes only AgentGO's persisted keyword configuration and
// local watchlist flags. No IMAP commands or AI requests are issued.
func (a *App) handleMailWatchlist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.mail == nil {
		http.Error(w, "AgentGO E-Mail unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Terms []MailWatchlistTerm `json:"terms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid Watchlist terms", http.StatusBadRequest)
		return
	}
	if err := validateMailWatchlistTerms(req.Terms); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.mail.mu.Lock()
	busy := a.mail.syncRunning || a.mail.aiState.Running || len(a.mail.callCancels) > 0
	a.mail.mu.Unlock()
	if busy {
		http.Error(w, "Finish the active E-Mail operation before editing Watchlist", http.StatusConflict)
		return
	}
	cfg := a.mailConfigSnapshot()
	cfg.WatchlistTerms = normalizeMailWatchlistTerms(req.Terms)
	index, err := readMailIndex(a.mail.root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.mail.rebuildWatchlistMatches(&index, cfg.WatchlistTerms, cfg.BodyCacheDays)
	if err := a.saveMailConfig(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.mail.saveIndex(index); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, a.mail.state())
}
