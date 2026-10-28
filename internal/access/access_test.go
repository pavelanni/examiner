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

func TestQuestionFileOwner(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "ivan", Role: model.UserRoleTeacher},
		{ID: 2, Username: "ivan_mid", Role: model.UserRoleTeacher},
		{ID: 3, Username: "olga", Role: model.UserRoleStudent},
		{ID: 4, Username: "admin", Role: model.UserRoleAdmin},
	}
	tests := []struct {
		file string
		want int64 // 0 = nobody
	}{
		{"teacher_ivan_x.json", 1},
		{"teacher_ivan_mid_exam.json", 2},
		{"teacher_ivan_middle.json", 1},
		{"teacher_olga_x.json", 0}, // students own no question files
		{"teacher_admin_x.json", 4},
		{"teacher_ghost_x.json", 0},
		{"other.json", 0},
	}
	for _, tt := range tests {
		var got int64
		if u := QuestionFileOwner(users, tt.file); u != nil {
			got = u.ID
		}
		if got != tt.want {
			t.Errorf("QuestionFileOwner(%q) = %d, want %d", tt.file, got, tt.want)
		}
	}
}
