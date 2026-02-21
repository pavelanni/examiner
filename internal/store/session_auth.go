package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

const authSessionTTL = 24 * time.Hour

// CreateAuthSession creates a new auth session token for a user.
func (s *Store) CreateAuthSession(userID int64) (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, err = s.db.Exec(
		`INSERT INTO auth_sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, now, now.Add(authSessionTTL),
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// GetAuthSession returns the auth session for the given token, or nil if not found/expired.
func (s *Store) GetAuthSession(token string) (*model.AuthSession, error) {
	var sess model.AuthSession
	err := s.db.QueryRow(
		`SELECT id, user_id, created_at, expires_at FROM auth_sessions WHERE id = ?`, token,
	).Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = s.DeleteAuthSession(token)
		return nil, nil
	}
	return &sess, nil
}

// DeleteAuthSession removes a session token.
func (s *Store) DeleteAuthSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE id = ?`, token)
	return err
}

// CleanupExpiredSessions removes all expired auth sessions.
func (s *Store) CleanupExpiredSessions() error {
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE expires_at < ?`, time.Now())
	return err
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
