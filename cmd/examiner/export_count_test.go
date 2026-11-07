package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	graderstore "github.com/pavelanni/examiner/internal/grader/store"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// TestExportQuestionCountIntegration verifies the CLI JSON and the grader import.
func TestExportQuestionCountIntegration(t *testing.T) {
	for _, scenario := range []string{"prepared only", "stale metadata", "mixed attempts"} {
		t.Run(scenario, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile("examiner.yaml", []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := store.New("source.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.SetExamInfo(model.ExamInfo{ExamID: "export-test", Subject: "Physics", Date: "2026-11-07", NumQuestions: 10}); err != nil {
				t.Fatal(err)
			}
			if scenario != "prepared only" {
				bp, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam"})
				if err != nil {
					t.Fatal(err)
				}
				var ids []int64
				for i := range 2 {
					id, err := s.InsertQuestion(model.Question{CourseID: 1, Text: fmt.Sprint("Q", i), Topic: "t", Difficulty: "easy", MaxPoints: 5})
					if err != nil {
						t.Fatal(err)
					}
					ids = append(ids, id)
				}
				if _, err := s.CreateSession(bp, 7, ids); err != nil {
					t.Fatal(err)
				}
				if scenario == "mixed attempts" {
					if _, err := s.CreateSession(bp, 7, ids[:1]); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exportCmd()
			if err := cmd.ParseFlags([]string{"--db=source.db", "--output=exam.json"}); err != nil {
				t.Fatal(err)
			}
			err = runExport(cmd, nil)
			if scenario == "mixed attempts" {
				if err == nil || !strings.Contains(err.Error(), "mixed question counts") {
					t.Fatalf("must reject an ambiguous export: %v", err)
				}
				if _, err := os.Stat("exam.json"); !os.IsNotExist(err) {
					t.Fatalf("invalid export was written: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile("exam.json")
			if err != nil {
				t.Fatal(err)
			}
			var exp model.ExamExport
			if err := json.Unmarshal(data, &exp); err != nil {
				t.Fatal(err)
			}
			want := 2
			if scenario == "prepared only" {
				want = 10
			}
			if exp.NumQuestions != want {
				t.Fatalf("export count=%d, want %d", exp.NumQuestions, want)
			}
			grader, err := graderstore.New(filepath.Join(t.TempDir(), "grader.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = grader.Close() })
			if err := grader.ImportExam(exp); err != nil {
				t.Fatal(err)
			}
			exams, err := grader.ListExams()
			if err != nil || len(exams) != 1 || exams[0].NumQuestions != want {
				t.Fatalf("grader metadata=%+v, error=%v", exams, err)
			}
		})
	}
}
