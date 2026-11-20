package views

import (
	"context"
	"strings"

	"github.com/pavelanni/examiner/internal/model"
)

// sessionStatusLabel translates a session status. An unknown status is
// shown as it is: it is data, not a UI string.
func sessionStatusLabel(ctx context.Context, status model.SessionStatus) string {
	switch status {
	case model.StatusInProgress:
		return t(ctx, "StatusInProgress")
	case model.StatusSubmitted:
		return t(ctx, "StatementSubmitted")
	case model.StatusGrading:
		return t(ctx, "StatementGrading")
	case model.StatusGradingFailed:
		return t(ctx, "SessionGradingFailed")
	case model.StatusGraded:
		return t(ctx, "StatementGraded")
	case model.StatusReviewed:
		return t(ctx, "StatementReviewed")
	case model.StatusAnnulled:
		return t(ctx, "AttemptAnnulled")
	}
	return string(status)
}

// roleLabel translates a user role.
func roleLabel(ctx context.Context, role model.UserRole) string {
	switch role {
	case model.UserRoleStudent:
		return t(ctx, "RoleStudent")
	case model.UserRoleTeacher:
		return t(ctx, "RoleTeacher")
	case model.UserRoleAdmin:
		return t(ctx, "RoleAdmin")
	}
	return string(role)
}

// DifficultyLabel translates a question difficulty. An unknown difficulty
// is shown as it is.
func DifficultyLabel(ctx context.Context, d model.Difficulty) string {
	switch d {
	case model.DifficultyEasy:
		return t(ctx, "DifficultyEasy")
	case model.DifficultyMedium:
		return t(ctx, "DifficultyMedium")
	case model.DifficultyHard:
		return t(ctx, "DifficultyHard")
	}
	return string(d)
}

// difficultyListLabel translates a comma-separated difficulty filter such
// as "easy,medium".
func difficultyListLabel(ctx context.Context, list string) string {
	parts := strings.Split(list, ",")
	for i, p := range parts {
		parts[i] = DifficultyLabel(ctx, model.Difficulty(strings.TrimSpace(p)))
	}
	return strings.Join(parts, ", ")
}

// builderTitle is the constructor's title: the set being edited, or a new set.
func builderTitle(ctx context.Context, b TeacherBuilder) string {
	if b.Filename != "" {
		return td(ctx, "QSetEditTitle", map[string]any{"Name": b.SetName})
	}
	return t(ctx, "QSetNewTitle")
}
