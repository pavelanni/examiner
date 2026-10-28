// Package access holds the permission rules for questions. Every handler
// and import path asks it instead of comparing roles and owners itself.
package access

import (
	"strings"

	"github.com/pavelanni/examiner/internal/model"
)

// CanEditQuestion reports whether u may change or delete q: an admin may
// change any question, a teacher only their own. Everyone else, and a nil
// user, may not. A question without an owner is editable by admins only.
func CanEditQuestion(u *model.User, q model.Question) bool {
	if u == nil {
		return false
	}
	switch u.Role {
	case model.UserRoleAdmin:
		return true
	case model.UserRoleTeacher:
		return q.OwnerID != 0 && q.OwnerID == u.ID
	default:
		return false
	}
}

// QuestionFileOwner returns the owner of a teacher question file named
// teacher_<username>_<name>.json: the teacher or admin whose username gives
// the longest matching prefix, or nil. Usernames and file names may both
// contain "_", so "teacher_ivan_mid_exam.json" belongs to ivan_mid when
// that user exists. Students own no question files.
func QuestionFileOwner(users []model.User, name string) *model.User {
	var best *model.User
	for i := range users {
		u := &users[i]
		if u.Role != model.UserRoleTeacher && u.Role != model.UserRoleAdmin {
			continue
		}
		if !strings.HasPrefix(name, "teacher_"+u.Username+"_") {
			continue
		}
		if best == nil || len(u.Username) > len(best.Username) {
			best = u
		}
	}
	return best
}
