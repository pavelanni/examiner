package main

import (
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/pavelanni/examiner/internal/model"
)

func configFrom(t *testing.T, doc string) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(doc)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	return v
}

func TestExamSelection(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		want    model.Distribution
		wantErr string
	}{
		{name: "not set", doc: "num-questions: 3\ndifficulty: easy\n"},
		{name: "set", doc: "question_selection:\n  easy: 2\n  medium: 2\n  hard: 1\n",
			want: model.Distribution{"easy": 2, "medium": 2, "hard": 1}},
		{name: "num-questions 0 is no conflict", doc: "num-questions: 0\nquestion_selection:\n  hard: 1\n",
			want: model.Distribution{"hard": 1}},
		{name: "conflicts with num-questions", doc: "num-questions: 5\nquestion_selection:\n  easy: 1\n",
			wantErr: "cannot be combined with num-questions or difficulty"},
		{name: "conflicts with difficulty", doc: "difficulty: easy,medium\nquestion_selection:\n  easy: 1\n",
			wantErr: "cannot be combined with num-questions or difficulty"},
		{name: "invalid value", doc: "question_selection:\n  hard: 1.5\n",
			wantErr: "question_selection: hard: must be a whole number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := examSelection(configFrom(t, tt.doc))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, n := range tt.want {
				if got[k] != n {
					t.Errorf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}
