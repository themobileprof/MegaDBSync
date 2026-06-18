package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "megadbsync_session"
	sessionTTL    = 24 * time.Hour
)

type Manager struct {
	mu       sync.RWMutex
	password string // bcrypt hash
	sessions map[string]time.Time
}

func NewManager() *Manager {
	return &Manager{sessions: make(map[string]time.Time)}
}

func (m *Manager) SetPasswordHash(hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.password = hash
}

func (m *Manager) HasPassword() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.password != ""
}

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func (m *Manager) CheckPassword(plain string) bool {
	m.mu.RLock()
	hash := m.password
	m.mu.RUnlock()
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

func (m *Manager) Login(w http.ResponseWriter, plain string) bool {
	if !m.CheckPassword(plain) {
		return false
	}
	token := randomToken()
	m.mu.Lock()
	m.sessions[token] = time.Now().Add(sessionTTL)
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return true
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		m.mu.Lock()
		delete(m.sessions, c.Value)
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
}

func (m *Manager) Authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(m.sessions, c.Value)
		return false
	}
	m.sessions[c.Value] = time.Now().Add(sessionTTL)
	return true
}

func (m *Manager) ClearSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = make(map[string]time.Time)
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.Authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
