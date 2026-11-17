package userutil

import (
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

type importedUsers struct{ users []model.User }

func (s *importedUsers) CreateUser(user model.User) (int64, error) {
	s.users = append(s.users, user)
	return int64(len(s.users)), nil
}

// TestImportCSVGroup supports both new rosters and the existing two-column format.
func TestImportCSVGroup(t *testing.T) {
	for _, tc := range []struct{ input, group string }{
		{"user_id,display_name,group\n123,Иванов Иван, Г-1 \n", "Г-1"},
		{"user_id,display_name\n123,Иванов Иван\n", ""},
	} {
		s := &importedUsers{}
		if _, err := ImportCSV(strings.NewReader(tc.input), s, ImportConfig{Role: model.UserRoleStudent}); err != nil {
			t.Fatal(err)
		}
		if len(s.users) != 1 || s.users[0].GroupName != tc.group {
			t.Fatalf("imported users = %+v", s.users)
		}
	}
}
