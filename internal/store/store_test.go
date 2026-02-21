package store

import (
	"database/sql"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("newTestStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func insertTestQuestion(t *testing.T, s *Store, text, difficulty, topic string) int64 {
	t.Helper()
	id, err := s.InsertQuestion(model.Question{
		CourseID:    1,
		Text:        text,
		Difficulty:  model.Difficulty(difficulty),
		Topic:       topic,
		Rubric:      "rubric for " + text,
		ModelAnswer: "answer for " + text,
		MaxPoints:   10,
	})
	if err != nil {
		t.Fatalf("insertTestQuestion: %v", err)
	}
	return id
}

func TestQuestionCRUD(t *testing.T) {
	s := newTestStore(t)

	// Empty DB should return zero count and empty list.
	count, err := s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 questions, got %d", count)
	}

	list, err := s.ListQuestions()
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}

	// Insert and retrieve.
	id := insertTestQuestion(t, s, "What is Go?", "easy", "basics")
	q, err := s.GetQuestion(id)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if q.Text != "What is Go?" {
		t.Errorf("expected text 'What is Go?', got %q", q.Text)
	}
	if q.Difficulty != model.DifficultyEasy {
		t.Errorf("expected difficulty easy, got %q", q.Difficulty)
	}
	if q.Topic != "basics" {
		t.Errorf("expected topic 'basics', got %q", q.Topic)
	}

	// Not found.
	_, err = s.GetQuestion(9999)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows, got %v", err)
	}

	// Multiple questions.
	insertTestQuestion(t, s, "What is a goroutine?", "medium", "concurrency")
	list, err = s.ListQuestions()
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(list))
	}

	count, err = s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
}

func TestListQuestionsFiltered(t *testing.T) {
	s := newTestStore(t)
	insertTestQuestion(t, s, "Q1", "easy", "basics")
	insertTestQuestion(t, s, "Q2", "hard", "basics")
	insertTestQuestion(t, s, "Q3", "easy", "concurrency")

	tests := []struct {
		name       string
		difficulty string
		topic      string
		wantCount  int
	}{
		{"no filter", "", "", 3},
		{"by difficulty easy", "easy", "", 2},
		{"by difficulty hard", "hard", "", 1},
		{"by topic basics", "", "basics", 2},
		{"by both", "easy", "basics", 1},
		{"no match", "hard", "concurrency", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qs, err := s.ListQuestionsFiltered(tt.difficulty, tt.topic)
			if err != nil {
				t.Fatalf("ListQuestionsFiltered: %v", err)
			}
			if len(qs) != tt.wantCount {
				t.Errorf("expected %d questions, got %d", tt.wantCount, len(qs))
			}
		})
	}
}

func TestBlueprintCRUD(t *testing.T) {
	s := newTestStore(t)

	bp := model.ExamBlueprint{
		CourseID:     1,
		Name:         "Midterm",
		TimeLimit:    60,
		MaxFollowups: 3,
	}
	id, err := s.CreateBlueprint(bp)
	if err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}

	got, err := s.GetBlueprint(id)
	if err != nil {
		t.Fatalf("GetBlueprint: %v", err)
	}
	if got.Name != "Midterm" {
		t.Errorf("expected name 'Midterm', got %q", got.Name)
	}
	if got.TimeLimit != 60 {
		t.Errorf("expected time limit 60, got %d", got.TimeLimit)
	}
	if got.MaxFollowups != 3 {
		t.Errorf("expected max followups 3, got %d", got.MaxFollowups)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newTestStore(t)

	bpID, err := s.CreateBlueprint(model.ExamBlueprint{
		CourseID: 1, Name: "Test", MaxFollowups: 2,
	})
	if err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}

	q1 := insertTestQuestion(t, s, "Q1", "easy", "t1")
	q2 := insertTestQuestion(t, s, "Q2", "easy", "t2")

	sessID, err := s.CreateSession(bpID, 1, []int64{q1, q2})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sess, err := s.GetSession(sessID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Status != model.StatusInProgress {
		t.Errorf("expected status in_progress, got %q", sess.Status)
	}
	if sess.SubmittedAt != nil {
		t.Errorf("expected nil submitted_at")
	}

	// Submit the session.
	if err := s.UpdateSessionStatus(sessID, model.StatusSubmitted); err != nil {
		t.Fatalf("UpdateSessionStatus: %v", err)
	}
	sess, err = s.GetSession(sessID)
	if err != nil {
		t.Fatalf("GetSession after submit: %v", err)
	}
	if sess.Status != model.StatusSubmitted {
		t.Errorf("expected status submitted, got %q", sess.Status)
	}
	if sess.SubmittedAt == nil {
		t.Error("expected submitted_at to be set")
	}

	// ListSessions returns newest first.
	sessions, err := s.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
}

func TestThreadsAndMessages(t *testing.T) {
	s := newTestStore(t)

	bpID, _ := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "T"})
	q1 := insertTestQuestion(t, s, "Q1", "easy", "t")
	q2 := insertTestQuestion(t, s, "Q2", "easy", "t")
	sessID, _ := s.CreateSession(bpID, 1, []int64{q1, q2})

	threads, err := s.GetThreadsForSession(sessID)
	if err != nil {
		t.Fatalf("GetThreadsForSession: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(threads))
	}
	// Threads should be ordered by ID.
	if threads[0].ID > threads[1].ID {
		t.Error("threads not ordered by ID")
	}

	// GetThread
	thread, err := s.GetThread(threads[0].ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.Status != model.ThreadOpen {
		t.Errorf("expected open status, got %q", thread.Status)
	}

	// UpdateThreadStatus
	if err := s.UpdateThreadStatus(thread.ID, model.ThreadAnswered); err != nil {
		t.Fatalf("UpdateThreadStatus: %v", err)
	}
	thread, _ = s.GetThread(thread.ID)
	if thread.Status != model.ThreadAnswered {
		t.Errorf("expected answered status, got %q", thread.Status)
	}

	// AddMessage and GetMessages
	threadID := threads[0].ID
	for _, msg := range []model.Message{
		{ThreadID: threadID, Role: model.RoleStudent, Content: "My answer"},
		{ThreadID: threadID, Role: model.RoleLLM, Content: "Follow-up?"},
		{ThreadID: threadID, Role: model.RoleStudent, Content: "More detail"},
	} {
		if _, err := s.AddMessage(msg); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	msgs, err := s.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	// Messages ordered by ID.
	if msgs[0].Content != "My answer" {
		t.Errorf("unexpected first message: %q", msgs[0].Content)
	}

	// CountStudentMessages
	count, err := s.CountStudentMessages(threadID)
	if err != nil {
		t.Fatalf("CountStudentMessages: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 student messages, got %d", count)
	}
}

func TestScores(t *testing.T) {
	s := newTestStore(t)

	bpID, _ := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "T"})
	qID := insertTestQuestion(t, s, "Q1", "easy", "t")
	sessID, _ := s.CreateSession(bpID, 1, []int64{qID})
	threads, _ := s.GetThreadsForSession(sessID)
	threadID := threads[0].ID

	// No score yet.
	score, err := s.GetScore(threadID)
	if err != nil {
		t.Fatalf("GetScore: %v", err)
	}
	if score != nil {
		t.Error("expected nil score")
	}

	// Insert score.
	err = s.UpsertScore(model.QuestionScore{
		ThreadID:    threadID,
		LLMScore:    7.5,
		LLMFeedback: "Good answer",
	})
	if err != nil {
		t.Fatalf("UpsertScore: %v", err)
	}

	score, err = s.GetScore(threadID)
	if err != nil {
		t.Fatalf("GetScore: %v", err)
	}
	if score.LLMScore != 7.5 {
		t.Errorf("expected LLM score 7.5, got %f", score.LLMScore)
	}
	if score.LLMFeedback != "Good answer" {
		t.Errorf("expected feedback 'Good answer', got %q", score.LLMFeedback)
	}

	// Update score via upsert.
	err = s.UpsertScore(model.QuestionScore{
		ThreadID:    threadID,
		LLMScore:    8.0,
		LLMFeedback: "Updated feedback",
	})
	if err != nil {
		t.Fatalf("UpsertScore update: %v", err)
	}
	score, _ = s.GetScore(threadID)
	if score.LLMScore != 8.0 {
		t.Errorf("expected updated score 8.0, got %f", score.LLMScore)
	}

	// UpdateTeacherScore
	err = s.UpdateTeacherScore(threadID, 9.0, "Great")
	if err != nil {
		t.Fatalf("UpdateTeacherScore: %v", err)
	}
	score, _ = s.GetScore(threadID)
	if score.TeacherScore == nil || *score.TeacherScore != 9.0 {
		t.Errorf("expected teacher score 9.0, got %v", score.TeacherScore)
	}
	if score.TeacherComment != "Great" {
		t.Errorf("expected teacher comment 'Great', got %q", score.TeacherComment)
	}
}

func TestGrades(t *testing.T) {
	s := newTestStore(t)

	bpID, _ := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "T"})
	qID := insertTestQuestion(t, s, "Q1", "easy", "t")
	sessID, _ := s.CreateSession(bpID, 1, []int64{qID})

	// No grade yet.
	grade, err := s.GetGrade(sessID)
	if err != nil {
		t.Fatalf("GetGrade: %v", err)
	}
	if grade != nil {
		t.Error("expected nil grade")
	}

	// Insert grade.
	err = s.UpsertGrade(model.Grade{SessionID: sessID, LLMGrade: 85.0})
	if err != nil {
		t.Fatalf("UpsertGrade: %v", err)
	}

	grade, err = s.GetGrade(sessID)
	if err != nil {
		t.Fatalf("GetGrade: %v", err)
	}
	if grade.LLMGrade != 85.0 {
		t.Errorf("expected LLM grade 85.0, got %f", grade.LLMGrade)
	}
	if grade.FinalGrade != nil {
		t.Error("expected nil final grade")
	}

	// Update grade via upsert.
	err = s.UpsertGrade(model.Grade{SessionID: sessID, LLMGrade: 90.0})
	if err != nil {
		t.Fatalf("UpsertGrade update: %v", err)
	}
	grade, _ = s.GetGrade(sessID)
	if grade.LLMGrade != 90.0 {
		t.Errorf("expected updated grade 90.0, got %f", grade.LLMGrade)
	}

	// FinalizeGrade
	err = s.FinalizeGrade(sessID, 88.0, 1)
	if err != nil {
		t.Fatalf("FinalizeGrade: %v", err)
	}
	grade, _ = s.GetGrade(sessID)
	if grade.FinalGrade == nil || *grade.FinalGrade != 88.0 {
		t.Errorf("expected final grade 88.0, got %v", grade.FinalGrade)
	}
	if grade.ReviewedAt == nil {
		t.Error("expected reviewed_at to be set")
	}
}

func TestGetSessionView(t *testing.T) {
	s := newTestStore(t)

	bpID, _ := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Final"})
	q1 := insertTestQuestion(t, s, "Q1", "easy", "t1")
	sessID, _ := s.CreateSession(bpID, 1, []int64{q1})
	threads, _ := s.GetThreadsForSession(sessID)
	threadID := threads[0].ID

	if _, err := s.AddMessage(model.Message{ThreadID: threadID, Role: model.RoleStudent, Content: "answer"}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.UpsertScore(model.QuestionScore{ThreadID: threadID, LLMScore: 8, LLMFeedback: "ok"}); err != nil {
		t.Fatalf("UpsertScore: %v", err)
	}
	if err := s.UpsertGrade(model.Grade{SessionID: sessID, LLMGrade: 80}); err != nil {
		t.Fatalf("UpsertGrade: %v", err)
	}

	view, err := s.GetSessionView(sessID)
	if err != nil {
		t.Fatalf("GetSessionView: %v", err)
	}

	if view.Blueprint.Name != "Final" {
		t.Errorf("expected blueprint name 'Final', got %q", view.Blueprint.Name)
	}
	if len(view.Threads) != 1 {
		t.Fatalf("expected 1 thread view, got %d", len(view.Threads))
	}
	tv := view.Threads[0]
	if tv.Question.Text != "Q1" {
		t.Errorf("expected question text 'Q1', got %q", tv.Question.Text)
	}
	if len(tv.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(tv.Messages))
	}
	if tv.Score == nil || tv.Score.LLMScore != 8 {
		t.Error("expected score with LLMScore 8")
	}
	if view.Grade == nil || view.Grade.LLMGrade != 80 {
		t.Error("expected grade with LLMGrade 80")
	}
}

func TestImportedFileHash(t *testing.T) {
	s := newTestStore(t)

	// Missing file returns empty string.
	hash, err := s.GetImportedFileHash("/some/path.json")
	if err != nil {
		t.Fatalf("GetImportedFileHash: %v", err)
	}
	if hash != "" {
		t.Errorf("expected empty hash, got %q", hash)
	}

	// Set hash.
	if err := s.SetImportedFileHash("/some/path.json", "abc123"); err != nil {
		t.Fatalf("SetImportedFileHash: %v", err)
	}
	hash, err = s.GetImportedFileHash("/some/path.json")
	if err != nil {
		t.Fatalf("GetImportedFileHash: %v", err)
	}
	if hash != "abc123" {
		t.Errorf("expected 'abc123', got %q", hash)
	}

	// Update existing.
	if err := s.SetImportedFileHash("/some/path.json", "def456"); err != nil {
		t.Fatalf("SetImportedFileHash update: %v", err)
	}
	hash, _ = s.GetImportedFileHash("/some/path.json")
	if hash != "def456" {
		t.Errorf("expected 'def456', got %q", hash)
	}
}

func TestListDistinctTopics(t *testing.T) {
	s := newTestStore(t)

	// Empty DB.
	topics, err := s.ListDistinctTopics()
	if err != nil {
		t.Fatalf("ListDistinctTopics: %v", err)
	}
	if len(topics) != 0 {
		t.Errorf("expected 0 topics, got %d", len(topics))
	}

	// Single topic.
	insertTestQuestion(t, s, "Q1", "easy", "basics")
	topics, _ = s.ListDistinctTopics()
	if len(topics) != 1 || topics[0] != "basics" {
		t.Errorf("expected [basics], got %v", topics)
	}

	// Multiple distinct (same topic repeated should still be 1).
	insertTestQuestion(t, s, "Q2", "easy", "basics")
	insertTestQuestion(t, s, "Q3", "easy", "concurrency")
	insertTestQuestion(t, s, "Q4", "easy", "advanced")
	topics, _ = s.ListDistinctTopics()
	if len(topics) != 3 {
		t.Errorf("expected 3 distinct topics, got %d: %v", len(topics), topics)
	}
	// Should be ordered alphabetically.
	if topics[0] != "advanced" || topics[1] != "basics" || topics[2] != "concurrency" {
		t.Errorf("expected [advanced basics concurrency], got %v", topics)
	}
}
