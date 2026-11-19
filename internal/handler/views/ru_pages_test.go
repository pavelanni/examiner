package views

import (
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// englishLabels are labels that once leaked into the Russian UI.
var englishLabels = []string{
	"Question text", "Difficulty", ">easy<", ">medium<", ">hard<", "(easy,", "(medium,", "(hard,", "Remove", ">Edit<", "Max points",
	"Model answer", "Rubric (", "Create Questions", "Edit Questions", "Exam Topic", "Questions List",
	"Add Question", "TeacherProfile", "Your Questions List", "graded<", "reviewed<", "in_progress<",
	"(student)", "(teacher)", "(admin)", ">student<", ">teacher<", ">admin<", "Follow-up question",
	"Time's up", "Time&#39;s up",
}

func assertNoEnglish(t *testing.T, page, html string) {
	t.Helper()
	for _, label := range englishLabels {
		if strings.Contains(html, label) {
			t.Errorf("%s: English %q on the Russian page", page, label)
		}
	}
}

func TestRussianPagesHaveNoEnglishLabels(t *testing.T) {
	teacher := &model.User{DisplayName: "Анна", Role: model.UserRoleTeacher}
	admin := &model.User{DisplayName: "Админ", Role: model.UserRoleAdmin}
	student := &model.User{DisplayName: "Иван", Role: model.UserRoleStudent}

	assertNoEnglish(t, "new set", renderRU(t, teacher, TeacherCreateTestPage("Анна", "c", TeacherBuilder{Published: true, Example: "[]"})))
	assertNoEnglish(t, "edit set with duplicates", renderRU(t, teacher, TeacherCreateTestPage("Анна", "c", TeacherBuilder{
		Filename: "teacher_anna_mid.json", SetName: "mid",
		Questions: []BuilderQuestion{{ID: 3, QuestionImport: model.QuestionImport{Text: "Вопрос", Difficulty: model.DifficultyHard}}},
		Dups:      []DupChoice{{N: 1, Msg: "Вопрос №1 совпадает", CanUpdate: true, UpdateID: 3}}, DupToken: "x",
	})))
	assertNoEnglish(t, "profile", renderRU(t, teacher, TeacherProfilePage(
		[]QuestionSet{{File: "teacher_anna_mid.json", Name: "mid"}}, &SaveSummary{Name: "mid", Inserted: 2})))
	assertNoEnglish(t, "admin questions", renderRU(t, admin, AdminQuestionsPage("", false, nil, nil, nil, "[]")))
	assertNoEnglish(t, "admin users", renderRU(t, admin, AdminUsersPage([]model.User{*admin, *teacher, *student}, "")))
	for _, u := range []*model.User{teacher, admin, student} {
		assertNoEnglish(t, "layout "+string(u.Role), renderRU(t, u, Layout("x")))
	}
	for _, status := range []model.SessionStatus{model.StatusInProgress, model.StatusGraded, model.StatusReviewed} {
		view := model.SessionView{
			Session: model.ExamSession{ID: 1, Status: status},
			Threads: []model.ThreadView{{
				Question: model.Question{Text: "Вопрос", Difficulty: model.DifficultyMedium, MaxPoints: 5},
				Messages: []model.Message{{Role: model.RoleLLM, Content: "Хорошо", Followup: "Почему?"}},
			}},
		}
		assertNoEnglish(t, "results "+string(status), renderRU(t, student, ResultsPage(view, model.ResultsPolicy{})))
	}
}
