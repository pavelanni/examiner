// Package access holds the permission rules for questions. Every handler
// and import path asks it instead of comparing roles and owners itself.
package access

import "github.com/pavelanni/examiner/internal/model"

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
