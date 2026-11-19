package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func TestQueueAnswerCommitsMessageAndJobTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exam.db")
	s := openEvaluationStore(t, path)
	bp, qs := sessionEnv(t, s)
	id, err := s.CreateSession(bp, 1, qs[:1])
	if err != nil {
		t.Fatal(err)
	}
	threads, err := s.GetThreadsForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	threadID := threads[0].ID
	if _, err := s.db.Exec(`CREATE TRIGGER fail_job BEFORE INSERT ON evaluation_jobs BEGIN SELECT RAISE(ABORT,'disk error'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueAnswer(id, threadID, 0, "text", false); err == nil {
		t.Fatal("expected enqueue failure")
	}
	messages, err := s.GetMessages(threadID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("failed enqueue appended an answer: %+v, %v", messages, err)
	}
	thread, err := s.GetThread(threadID)
	if err != nil || thread.Status != model.ThreadOpen {
		t.Fatalf("failed enqueue changed the thread: %+v, %v", thread, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_job`); err != nil {
		t.Fatal(err)
	}
	other := openEvaluationStore(t, path)
	start, done := make(chan struct{}), make(chan error, 2)
	for _, conn := range []*Store{s, other} {
		go func() {
			<-start
			done <- conn.QueueAnswer(id, threadID, 0, "text", false)
		}()
	}
	close(start)
	first, second := <-done, <-done
	if second == nil {
		first, second = second, first
	}
	if first != nil || !errors.Is(second, ErrTurnChanged) {
		t.Fatalf("independent connections returned %v and %v", first, second)
	}
	messages, err = s.GetMessages(threadID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("concurrent enqueue: %+v, %v", messages, err)
	}
}

func openEvaluationStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func claimJob(t *testing.T, s *Store) EvaluationJob {
	t.Helper()
	job, err := s.ClaimEvaluation(false)
	if err != nil || job == nil {
		t.Fatalf("missing job: %+v, %v", job, err)
	}
	return *job
}

func TestQueueRecoversFeedbackAndPartiallyGradedExam(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exam.db")
	s := openEvaluationStore(t, path)
	bp, qs := sessionEnv(t, s)
	grading, err := s.CreateSession(bp, 1, qs[:2])
	if err != nil {
		t.Fatal(err)
	}
	answering, err := s.CreateSession(bp, 2, qs[:1])
	if err != nil {
		t.Fatal(err)
	}
	threads, err := s.GetThreadsForSession(answering)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.QueueSubmission(grading); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueSubmission(grading); !errors.Is(err, ErrTurnChanged) {
		t.Fatalf("duplicate submission: %v", err)
	}
	if job, err := s.ClaimEvaluation(true); err != nil || job != nil {
		t.Fatalf("a reserved feedback slot claimed a final grade: %+v, %v", job, err)
	}
	first := claimJob(t, s)
	if err := s.FinishGrading(first, &model.QuestionScore{LLMScore: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueAnswer(answering, threads[0].ID, 0, "saved", false); err != nil {
		t.Fatal(err)
	}
	feedback := claimJob(t, s)
	if !feedback.Feedback || feedback.ThreadID != threads[0].ID {
		t.Fatalf("feedback did not take priority over earlier final grading: %+v", feedback)
	}
	last := claimJob(t, s)
	if grade, err := s.GetScore(first.ThreadID); err != nil || grade != nil {
		t.Fatalf("published partial grade: %+v, %v", grade, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openEvaluationStore(t, path)
	if job, err := s.ClaimEvaluation(false); job != nil || err != nil {
		t.Fatalf("merely opening SQLite released a live claim: %+v, %v", job, err)
	}
	if n, err := s.RecoverInterruptedSessions(); err != nil || n != 0 {
		t.Fatalf("reopened an exam with durable work: %d, %v", n, err)
	}
	if job := claimJob(t, s); job != feedback {
		t.Fatalf("first resumed job: %+v, want %+v", job, feedback)
	}
	if err := s.FinishFeedback(feedback, &model.Message{ThreadID: feedback.ThreadID, Role: model.RoleLLM, Content: "reply"}); err != nil {
		t.Fatal(err)
	}
	if job := claimJob(t, s); job != last {
		t.Fatalf("reran a staged score or lost last claim: %+v, want %+v", job, last)
	}
	if err := s.FinishGrading(last, &model.QuestionScore{LLMScore: 3}); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetSessionView(grading)
	if err != nil || view.Session.Status != model.StatusGraded || view.Grade == nil || view.Threads[0].Score == nil || view.Threads[1].Score == nil {
		t.Fatalf("resumed submission: %+v, %v", view, err)
	}
	maxPoints := view.Threads[0].Question.MaxPoints + view.Threads[1].Question.MaxPoints
	if view.Grade.LLMGrade != 1000.0/float64(maxPoints) || view.Threads[0].Score.LLMScore != 7 || view.Threads[1].Score.LLMScore != 3 {
		t.Fatalf("lost staged scores: %+v", view)
	}
	messages, err := s.GetMessages(feedback.ThreadID)
	if err != nil || len(messages) != 2 || messages[0].Text() != "saved" || messages[1].Text() != "reply" {
		t.Fatalf("recovered conversation: %+v, %v", messages, err)
	}
	if job, err := s.ClaimEvaluation(false); job != nil || err != nil {
		t.Fatalf("duplicate job after completion: %+v, %v", job, err)
	}
}

func TestEditedQuestionIsGradedAgainstCurrentCriteria(t *testing.T) {
	for _, scenario := range []string{"provider is running", "staged score survives restart"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exam.db")
			s := openEvaluationStore(t, path)
			bp, qs := sessionEnv(t, s)
			questions := qs[:1]
			if scenario == "staged score survives restart" {
				questions = qs[:2]
			}
			id, err := s.CreateSession(bp, 1, questions)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.QueueSubmission(id); err != nil {
				t.Fatal(err)
			}
			old := claimJob(t, s)
			original := &model.QuestionScore{LLMScore: 8} // 8 out of 10
			if len(questions) == 2 {
				if err := s.FinishGrading(old, original); err != nil {
					t.Fatal(err)
				}
			}
			q, err := s.GetQuestion(qs[0])
			if err != nil {
				t.Fatal(err)
			}
			q.MaxPoints = 5
			if err := s.InQuestionTx(t.Context(), func(tx *QuestionTx) error { return tx.Update(q) }); err != nil {
				t.Fatal(err)
			}
			if len(questions) == 1 {
				// The provider completed the old 10-point prompt after the edit.
				if err := s.FinishGrading(old, original); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = openEvaluationStore(t, path)
				if _, err := s.RecoverInterruptedSessions(); err != nil {
					t.Fatal(err)
				}
			}
			if grade, err := s.GetGrade(id); err != nil || grade != nil {
				t.Fatalf("published a grade before reevaluation: %+v, %v", grade, err)
			}
			for range questions {
				job := claimJob(t, s)
				score := 8.0
				if job.ThreadID == old.ThreadID {
					if job.ID == old.ID {
						t.Fatal("old claim could still publish a result")
					}
					score = 4 // 4 out of 5 under the new criteria
				}
				if err := s.FinishGrading(job, &model.QuestionScore{LLMScore: score}); err != nil {
					t.Fatal(err)
				}
			}
			view, err := s.GetSessionView(id)
			if err != nil {
				t.Fatal(err)
			}
			if view.Grade == nil || view.Grade.LLMGrade != 80 || view.Threads[0].Score == nil ||
				view.Threads[0].Score.LLMScore != 4 || view.Threads[0].Question.MaxPoints != 5 {
				t.Fatalf("grade and question must use the same maximum: %+v", view)
			}
		})
	}
}

func TestRubricEditRequeuesOnlyUnfinishedFeedback(t *testing.T) {
	s := newTestStore(t)
	bp, qs := sessionEnv(t, s)
	id, err := s.CreateSession(bp, 1, qs[:1])
	if err != nil {
		t.Fatal(err)
	}
	threads, err := s.GetThreadsForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	tid := threads[0].ID
	if err := s.QueueAnswer(id, tid, 0, "saved answer", false); err != nil {
		t.Fatal(err)
	}
	old := claimJob(t, s)
	q, err := s.GetQuestion(qs[0])
	if err != nil {
		t.Fatal(err)
	}
	q.Rubric = "Edited rubric"
	if err := s.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishFeedback(old, &model.Message{ThreadID: tid, Role: model.RoleLLM, Content: "old reply"}); err != nil {
		t.Fatal(err)
	}
	current := claimJob(t, s)
	// A save with the same grading inputs must leave the replacement claim intact.
	if err := s.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishFeedback(current, &model.Message{ThreadID: tid, Role: model.RoleLLM, Content: "new reply"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.GetMessages(tid)
	if err != nil || len(msgs) != 2 || msgs[0].Content != "saved answer" || msgs[1].Content != "new reply" {
		t.Fatalf("conversation after rubric edit: %+v, %v", msgs, err)
	}
}
