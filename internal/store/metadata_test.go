package store

import (
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// TestConfigureQuestionCount preserves attempts while correcting stale prep metadata.
func TestConfigureQuestionCount(t *testing.T) {
	for _, actual := range []int{0, 2, 3} {
		t.Run(string(rune('0'+actual)), func(t *testing.T) {
			s := newTestStore(t)
			if err := s.SetExamInfo(model.ExamInfo{ExamID: "exam", NumQuestions: 10}); err != nil {
				t.Fatal(err)
			}
			bp, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam"})
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for i := range actual {
				ids = append(ids, insertTestQuestion(t, s, string(rune('a'+i)), "easy", "t"))
			}
			if actual > 0 {
				if _, err := s.CreateSession(bp, 7, ids); err != nil {
					t.Fatal(err)
				}
			}
			err = s.ConfigureQuestionCount(2)
			if actual == 3 {
				if err == nil || !strings.Contains(err.Error(), "conflicts with session") {
					t.Fatalf("must reject incompatible historical attempt: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			info, err := s.GetExamInfo()
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if actual == 3 {
				want = 10
			}
			if info.NumQuestions != want || info.ExamID != "exam" {
				t.Fatalf("incorrect metadata: %+v", info)
			}
			if actual > 0 {
				threads, err := s.GetThreadsForSession(1)
				if err != nil || len(threads) != actual {
					t.Fatalf("existing questions changed: %v, %v", threads, err)
				}
			}
		})
	}
}
