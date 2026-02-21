package store

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/pavelanni/examiner/internal/model"
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

// GetUserByUsername returns a user by username.
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

// GetUserByID returns a user by ID.
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

// ListUsers returns all users.
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
