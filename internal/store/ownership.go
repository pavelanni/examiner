package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// QuestionKey identifies a question within a topic by its normalized text.
type QuestionKey struct {
	Topic, TextNorm string
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func firstAdminID(q queryer) (int64, error) {
	var id sql.NullInt64
	if err := q.QueryRow(`SELECT MIN(id) FROM users WHERE role = 'admin'`).Scan(&id); err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, errors.New("no admin user")
	}
	return id.Int64, nil
}

// FirstAdminID returns the ID of the oldest admin user. Imports without a
// logged-in user (--questions, prep) and unclaimed questions belong to it.
func (s *Store) FirstAdminID() (int64, error) {
	return firstAdminID(s.db)
}

// FindUnownedQuestionIDs returns the questions without an owner that have
// this topic and normalized text.
func (s *Store) FindUnownedQuestionIDs(topic, textNorm string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM questions WHERE owner_id IS NULL AND topic = ? AND text_norm = ? ORDER BY id`,
		topic, textNorm)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimQuestionOwners assigns owners to questions that have none. claims
// maps a question ID to the users whose files contain it. A question
// claimed by exactly one user gets that owner; every other question
// without an owner, including ones claimed by several users, goes to the
// first admin. Rows that already have an owner are never changed, so a
// second run is a no-op. All changes happen in one transaction.
func (s *Store) ClaimQuestionOwners(claims map[int64][]int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	admin, err := firstAdminID(tx)
	if err != nil {
		return fmt.Errorf("claim question owners: %w", err)
	}

	ids := make([]int64, 0, len(claims))
	for id := range claims {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		users := slices.Clone(claims[id])
		slices.Sort(users)
		users = slices.Compact(users)
		switch len(users) {
		case 0:
			continue
		case 1:
			if _, err := tx.Exec(`UPDATE questions SET owner_id = ? WHERE id = ? AND owner_id IS NULL`, users[0], id); err != nil {
				return err
			}
		default:
			slog.Warn("question claimed by several teachers", "id", id, "users", users)
		}
	}
	if _, err := tx.Exec(`UPDATE questions SET owner_id = ? WHERE owner_id IS NULL`, admin); err != nil {
		return err
	}
	return tx.Commit()
}

// AllPublished reports whether every question owned by ownerID that
// matches one of keys is published. No matching question means true.
func (s *Store) AllPublished(ownerID int64, keys []QuestionKey) (bool, error) {
	if len(keys) == 0 {
		return true, nil
	}
	conds := make([]string, 0, len(keys))
	args := []any{ownerID}
	for _, k := range keys {
		conds = append(conds, `(topic = ? AND text_norm = ?)`)
		args = append(args, k.Topic, k.TextNorm)
	}
	var unpublished int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM questions WHERE owner_id = ? AND published = 0 AND (`+
		strings.Join(conds, " OR ")+`)`, args...).Scan(&unpublished)
	if err != nil {
		return false, err
	}
	return unpublished == 0, nil
}
