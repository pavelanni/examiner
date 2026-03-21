package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/pavelanni/examiner/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// CreateUser inserts a new user.
func (s *Store) CreateUser(u model.User) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO users (username, display_name, password_hash, role, active, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, u.PasswordHash, u.Role, u.Active, time.Now(),
	)
	if err != nil {
		slog.Error("failed to create user", "username", u.Username, "error", err)
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	slog.Info("created user", "id", id, "username", u.Username, "role", u.Role)
	return id, nil
}

// GetUserByUsername returns a user by username, or nil if not found.
func (s *Store) GetUserByUsername(username string) (*model.User, error) {
	var u model.User
	err := s.db.QueryRow(
		`SELECT id, username, display_name, password_hash, role, active, created_at
		 FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &u.Active, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByID returns a user by ID, or nil if not found.
func (s *Store) GetUserByID(id int64) (*model.User, error) {
	var u model.User
	err := s.db.QueryRow(
		`SELECT id, username, display_name, password_hash, role, active, created_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &u.Active, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ListUsers returns all users ordered by ID.
func (s *Store) ListUsers() ([]model.User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, display_name, password_hash, role, active, created_at
		 FROM users ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &u.Active, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ToggleUserActive flips the active flag on a user.
func (s *Store) ToggleUserActive(id int64) error {
	_, err := s.db.Exec(`UPDATE users SET active = NOT active WHERE id = ?`, id)
	return err
}

// UserCount returns the total number of users.
func (s *Store) UserCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// UserCredential holds generated credentials for a single imported user.
type UserCredential struct {
	TeacherID   string
	DisplayName string
	Username    string
	Password    string
}

// ImportUsersCSV reads a CSV with columns teacher_id, display_name
// and creates teacher accounts with generated usernames and passwords.
// Returns the generated credentials for output.
func (s *Store) ImportUsersCSV(r io.Reader) ([]UserCredential, error) {
	reader := csv.NewReader(r)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV must have a header row and at least one user")
	}

	// Find column indices (case-insensitive, whitespace-tolerant).
	header := records[0]
	idCol, nameCol := -1, -1
	for i, h := range header {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "teacher_id":
			idCol = i
		case "display_name":
			nameCol = i
		}
	}
	if idCol < 0 {
		return nil, fmt.Errorf("CSV: missing teacher_id column")
	}
	if nameCol < 0 {
		return nil, fmt.Errorf("CSV: missing display_name column")
	}

	usedUsernames := map[string]bool{"admin": true}
	var creds []UserCredential

	for _, row := range records[1:] {
		teacherID := strings.TrimSpace(row[idCol])
		displayName := strings.TrimSpace(row[nameCol])
		if teacherID == "" {
			continue
		}

		username := deduplicateUsername(
			usernameFromDisplayName(displayName), usedUsernames)
		usedUsernames[username] = true

		password, err := randomPassword("teach", 5)
		if err != nil {
			return creds, fmt.Errorf("generate password for %s: %w", teacherID, err)
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return creds, fmt.Errorf("hash password for %s: %w", teacherID, err)
		}

		if _, err := s.CreateUser(model.User{
			Username:     username,
			DisplayName:  displayName,
			PasswordHash: string(hash),
			Role:         model.UserRoleTeacher,
			Active:       true,
		}); err != nil {
			slog.Warn("skipping user", "teacher_id", teacherID, "error", err)
			continue
		}

		creds = append(creds, UserCredential{
			TeacherID:   teacherID,
			DisplayName: displayName,
			Username:    username,
			Password:    password,
		})
	}

	return creds, nil
}

// usernameFromDisplayName builds a username from "First Last" as first letter
// of the first name + last name, lowercased and truncated to 8 characters.
func usernameFromDisplayName(displayName string) string {
	parts := strings.Fields(displayName)
	if len(parts) == 0 {
		return "user"
	}
	first := []rune(strings.ToLower(parts[0]))
	if len(parts) == 1 {
		if len(first) > 8 {
			return string(first[:8])
		}
		return string(first)
	}
	last := []rune(strings.ToLower(parts[len(parts)-1]))
	username := append(first[:1], last...)
	if len(username) > 8 {
		username = username[:8]
	}
	return string(username)
}

// deduplicateUsername ensures uniqueness by replacing the last character with
// an incrementing digit (2, 3, ...) when a collision is found.
func deduplicateUsername(base string, used map[string]bool) string {
	if !used[base] {
		return base
	}
	runes := []rune(base)
	for n := 2; n <= 99; n++ {
		suffix := []rune(fmt.Sprintf("%d", n))
		prefixLen := len(runes) - len(suffix)
		if prefixLen < 0 {
			prefixLen = 0
		}
		candidate := string(runes[:prefixLen]) + string(suffix)
		if !used[candidate] {
			return candidate
		}
	}
	return base
}

// randomPassword generates a password as prefix-XXXXX with random alphanumeric chars.
func randomPassword(prefix string, length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return prefix + "-" + string(b), nil
}
