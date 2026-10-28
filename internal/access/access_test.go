package access

import (
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func TestCanEditQuestion(t *testing.T) {
	admin := &model.User{ID: 1, Role: model.UserRoleAdmin}
	teacher := &model.User{ID: 2, Role: model.UserRoleTeacher}
	student := &model.User{ID: 2, Role: model.UserRoleStudent}
	tests := []struct {
		name  string
		user  *model.User
		owner int64
		want  bool
	}{
		{"admin, other owner", admin, 2, true},
		{"admin, no owner", admin, 0, true},
		{"teacher, own", teacher, 2, true},
		{"teacher, other owner", teacher, 3, false},
		{"teacher, no owner", teacher, 0, false},
		{"student with matching id", student, 2, false},
		{"nil user", nil, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanEditQuestion(tt.user, model.Question{OwnerID: tt.owner}); got != tt.want {
				t.Errorf("CanEditQuestion = %v, want %v", got, tt.want)
			}
		})
	}
}
