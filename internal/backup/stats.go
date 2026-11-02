package backup

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// Stats counts the records that must survive a backup and restore.
type Stats struct {
	Users       int            `json:"users"`
	Questions   int            `json:"questions"`
	Sessions    map[string]int `json:"sessions"` // by status
	Messages    int            `json:"messages"`
	Scores      int            `json:"scores"`       // per-question scores
	Grades      int            `json:"grades"`       // per-session grades
	FinalGrades int            `json:"final_grades"` // grades confirmed by a teacher
}

// Count reads Stats from an examiner database.
func Count(ctx context.Context, db *sql.DB) (Stats, error) {
	var s Stats
	for _, c := range []struct {
		dst   *int
		query string
	}{
		{&s.Users, `SELECT COUNT(*) FROM users`},
		{&s.Questions, `SELECT COUNT(*) FROM questions`},
		{&s.Messages, `SELECT COUNT(*) FROM messages`},
		{&s.Scores, `SELECT COUNT(*) FROM question_scores`},
		{&s.Grades, `SELECT COUNT(*) FROM grades`},
		{&s.FinalGrades, `SELECT COUNT(*) FROM grades WHERE final_grade IS NOT NULL`},
	} {
		if err := db.QueryRowContext(ctx, c.query).Scan(c.dst); err != nil {
			return Stats{}, fmt.Errorf("%s: %w", c.query, err)
		}
	}

	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM exam_sessions GROUP BY status`)
	if err != nil {
		return Stats{}, err
	}
	defer func() { _ = rows.Close() }()
	s.Sessions = map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return Stats{}, err
		}
		s.Sessions[status] = n
	}
	return s, rows.Err()
}

// String renders the counts one per line in a fixed order, so two
// outputs can be compared with diff.
func (s Stats) String() string {
	var b strings.Builder
	line := func(k string, v int) { fmt.Fprintf(&b, "%-24s %d\n", k, v) }
	line("users", s.Users)
	line("questions", s.Questions)
	total := 0
	statuses := make([]string, 0, len(s.Sessions))
	for st, n := range s.Sessions {
		statuses = append(statuses, st)
		total += n
	}
	slices.Sort(statuses)
	line("sessions", total)
	for _, st := range statuses {
		line("sessions."+st, s.Sessions[st])
	}
	line("messages", s.Messages)
	line("scores", s.Scores)
	line("grades", s.Grades)
	line("grades.final", s.FinalGrades)
	return b.String()
}
