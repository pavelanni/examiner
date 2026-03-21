package userutil

import (
	"crypto/rand"
	"encoding/csv"
	"fmt"
	"io"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/model"
)

// UserCreator is the interface that both examiner and grader stores satisfy.
type UserCreator interface {
	CreateUser(u model.User) (int64, error)
}

// Credential holds generated credentials for a single imported user.
type Credential struct {
	UserID      string
	DisplayName string
	Username    string
	Password    string
}

// ImportConfig controls how CSV import behaves.
type ImportConfig struct {
	Role           model.UserRole // Role to assign (e.g. UserRoleStudent, UserRoleTeacher)
	PasswordPrefix string         // Prefix for generated passwords (e.g. "phys", "teach")
}

// ImportCSV reads a CSV with columns user_id and display_name,
// generates usernames and passwords, creates users via the store, and returns
// the generated credentials.
func ImportCSV(r io.Reader, store UserCreator, cfg ImportConfig) ([]Credential, error) {
	reader := csv.NewReader(r)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV must have a header row and at least one entry")
	}

	header := records[0]
	idCol, nameCol := -1, -1
	for i, h := range header {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "user_id", "student_id", "teacher_id":
			idCol = i
		case "display_name":
			nameCol = i
		}
	}
	if idCol < 0 {
		return nil, fmt.Errorf("CSV: missing user_id (or student_id/teacher_id) column")
	}
	if nameCol < 0 {
		return nil, fmt.Errorf("CSV: missing display_name column")
	}

	usedUsernames := map[string]bool{"admin": true}
	var creds []Credential

	for _, row := range records[1:] {
		userID := strings.TrimSpace(row[idCol])
		displayName := strings.TrimSpace(row[nameCol])
		if userID == "" {
			continue
		}

		username := DeduplicateUsername(
			UsernameFromDisplayName(displayName), usedUsernames)
		usedUsernames[username] = true

		password, err := RandomPassword(cfg.PasswordPrefix, 5)
		if err != nil {
			return creds, fmt.Errorf("generate password for %s: %w", userID, err)
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return creds, fmt.Errorf("hash password for %s: %w", userID, err)
		}

		if _, err := store.CreateUser(model.User{
			Username:     username,
			ExternalID:   userID,
			DisplayName:  displayName,
			PasswordHash: string(hash),
			Role:         cfg.Role,
			Active:       true,
		}); err != nil {
			return creds, fmt.Errorf("create user %s: %w", userID, err)
		}

		creds = append(creds, Credential{
			UserID:      userID,
			DisplayName: displayName,
			Username:    username,
			Password:    password,
		})
	}

	return creds, nil
}

// UsernameFromDisplayName builds a username from "First Last" as first letter
// of the first name + last name, lowercased and truncated to 8 characters.
func UsernameFromDisplayName(displayName string) string {
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

// DeduplicateUsername ensures uniqueness by replacing the last character with
// an incrementing digit (2, 3, ...) when a collision is found.
func DeduplicateUsername(base string, used map[string]bool) string {
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

// RandomPassword generates a password as prefix-XXXXX with random alphanumeric chars.
func RandomPassword(prefix string, length int) (string, error) {
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

// WriteCredentialsCSV writes credentials to a CSV writer.
func WriteCredentialsCSV(w io.Writer, creds []Credential) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"user_id", "display_name", "username", "password"}); err != nil {
		return err
	}
	for _, c := range creds {
		if err := cw.Write([]string{c.UserID, c.DisplayName, c.Username, c.Password}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
