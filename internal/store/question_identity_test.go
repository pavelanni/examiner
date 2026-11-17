package store

import (
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// TestActiveQuestionIdentityMigration preserves historical threads and enforces
// uniqueness only within the same course and topic.
func TestActiveQuestionIdentityMigration(t *testing.T) {
	s := newTestStore(t)
	original := insertTestQuestion(t, s, "What is Go?", "easy", "basics")
	if _, err := s.db.Exec("DROP INDEX idx_questions_active_identity"); err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Exec(`INSERT INTO questions
		(course_id, text, difficulty, topic, rubric, model_answer, max_points)
		VALUES (1, 'what is go!', 'hard', 'basics', 'historical rubric', '', 17)`)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	bp, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Exam"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateSession(bp, 1, []int64{historical})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	view, err := s.GetSessionView(session)
	if err != nil {
		t.Fatal(err)
	}
	q := view.Threads[0].Question
	if q.ID != historical || q.MaxPoints != 17 || q.Rubric != "historical rubric" {
		t.Fatalf("historical question changed: %+v", q)
	}
	active, err := s.ListQuestionsFiltered("", "")
	if err != nil || len(active) != 1 || active[0].ID != original {
		t.Fatalf("active questions: %+v, %v", active, err)
	}
	duplicate := model.Question{CourseID: 1, Text: "what is go", Topic: "basics", Difficulty: "easy", MaxPoints: 1}
	if id, err := s.InsertQuestion(duplicate); err != nil || id != 0 {
		t.Fatalf("duplicate insert: id=%d, err=%v", id, err)
	}
	if _, err := s.db.Exec(`INSERT INTO questions
		(course_id, text, text_norm, difficulty, topic, rubric, model_answer, max_points)
		VALUES (1, 'What is Go', 'what is go', 'easy', 'basics', '', '', 1)`); err == nil {
		t.Fatal("SQLite accepted a duplicate active identity")
	}
	duplicate.CourseID = 2
	if id, err := s.InsertQuestion(duplicate); err != nil || id == 0 {
		t.Fatalf("separate course: id=%d, err=%v", id, err)
	}
	duplicate.CourseID, duplicate.Topic = 1, "history"
	if id, err := s.InsertQuestion(duplicate); err != nil || id == 0 {
		t.Fatalf("separate topic: id=%d, err=%v", id, err)
	}
}
