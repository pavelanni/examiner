package store

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
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

// ImportUsersCSV reads a CSV with columns username, display_name, password
// and creates teacher accounts. Returns the number of users created.
func (s *Store) ImportUsersCSV(r io.Reader) (int, error) {
	reader := csv.NewReader(r)
	records, err := reader.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("parse CSV: %w", err)
	}
	if len(records) < 2 {
		return 0, fmt.Errorf("CSV must have a header row and at least one user")
	}

	// Find column indices (case-insensitive, whitespace-tolerant).
	header := records[0]
	userCol, nameCol, passCol := -1, -1, -1
	for i, h := range header {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "username":
			userCol = i
		case "display_name":
			nameCol = i
		case "password":
			passCol = i
		}
	}
	if userCol < 0 {
		return 0, fmt.Errorf("CSV: missing username column")
	}
	if nameCol < 0 {
		return 0, fmt.Errorf("CSV: missing display_name column")
	}
	if passCol < 0 {
		return 0, fmt.Errorf("CSV: missing password column")
	}

	created := 0
	for _, row := range records[1:] {
		username := strings.TrimSpace(row[userCol])
		displayName := strings.TrimSpace(row[nameCol])
		password := strings.TrimSpace(row[passCol])
		if username == "" || password == "" {
			continue
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return created, fmt.Errorf("hash password for %s: %w", username, err)
		}

		if _, err := s.CreateUser(model.User{
			Username:     username,
			DisplayName:  displayName,
			PasswordHash: string(hash),
			Role:         model.UserRoleTeacher,
			Active:       true,
		}); err != nil {
			slog.Warn("skipping user", "username", username, "error", err)
			continue
		}
		created++
	}

	return created, nil
}
