package selection

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/pavelanni/examiner/internal/model"
)

// fromYAML returns the question_selection value as viper reads it from a
// YAML config file.
func fromYAML(t *testing.T, doc string) any {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(doc)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	return v.Get("question_selection")
}

func TestParseValid(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want model.Distribution
	}{
		{"all levels", "question_selection:\n  easy: 2\n  medium: 2\n  hard: 1\n", model.Distribution{"easy": 2, "medium": 2, "hard": 1}},
		{"one level", "question_selection:\n  hard: 3\n", model.Distribution{"hard": 3}},
		{"zero allowed", "question_selection:\n  easy: 0\n  medium: 1\n", model.Distribution{"easy": 0, "medium": 1}},
		{"case-insensitive keys", "question_selection:\n  Easy: 1\n", model.Distribution{"easy": 1}},
		{"whole float", "question_selection:\n  easy: 2.0\n", model.Distribution{"easy": 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(fromYAML(t, tt.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseNotSet(t *testing.T) {
	d, err := Parse(fromYAML(t, "num-questions: 3\n"))
	if d != nil || err != nil {
		t.Errorf("got %v, %v; want nil, nil", d, err)
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string // substrings of the error
	}{
		{"negative", "question_selection:\n  easy: -1\n  hard: 1\n", []string{"easy: must not be negative"}},
		{"fraction", "question_selection:\n  easy: 1.5\n", []string{"easy: must be a whole number, got 1.5"}},
		{"text", "question_selection:\n  medium: два\n", []string{`medium: must be a whole number, got "два"`}},
		{"quoted number", "question_selection:\n  medium: \"2\"\n", []string{"medium: must be a whole number"}},
		{"empty value", "question_selection:\n  hard:\n  easy: 1\n", []string{"hard: must be a whole number"}},
		{"bool", "question_selection:\n  hard: true\n", []string{"hard: must be a whole number"}},
		{"total zero", "question_selection:\n  easy: 0\n  hard: 0\n", []string{"total number of questions must be greater than 0"}},
		{"empty map", "question_selection: {}\n", []string{"total number of questions must be greater than 0"}},
		{"unknown level", "question_selection:\n  expert: 1\n  easy: 1\n", []string{"expert: unknown difficulty"}},
		{"scalar", "question_selection: 5\n", []string{"must map difficulties"}},
		{"list", "question_selection: [1, 2]\n", []string{"must map difficulties"}},
		{"every error reported", "question_selection:\n  easy: -1\n  expert: 2\n  hard: x\n",
			[]string{"easy: must not be negative", "expert: unknown difficulty", "hard: must be a whole number"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := Parse(fromYAML(t, tt.doc))
			if err == nil {
				t.Fatalf("got %v, want an error", d)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestParseJSONNumbers(t *testing.T) {
	d, err := Parse(map[string]any{"easy": float64(2), "hard": float64(1)})
	if err != nil || d["easy"] != 2 || d["hard"] != 1 {
		t.Errorf("got %v, %v", d, err)
	}
}

// bank returns n questions of each difficulty in one topic, IDs from 1.
func bank(n int) []model.Question {
	var qs []model.Question
	id := int64(0)
	for _, level := range model.Difficulties {
		for i := range n {
			id++
			text := fmt.Sprintf("%s question %d", level, i)
			qs = append(qs, model.Question{ID: id, Text: text, Difficulty: level, Topic: "t"})
		}
	}
	return qs
}

func ids(qs []model.Question) []int64 {
	out := make([]int64, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}

func TestDrawExactDistribution(t *testing.T) {
	d := model.Distribution{"easy": 2, "medium": 2, "hard": 1}
	pools := Pools(bank(10))
	sets := map[string]bool{}
	for seed := range uint64(50) {
		got, err := Draw(pools, d, rand.New(rand.NewPCG(seed, 1)))
		if err != nil {
			t.Fatal(err)
		}
		count := map[model.Difficulty]int{}
		for _, q := range got {
			count[q.Difficulty]++
		}
		if fmt.Sprint(count) != fmt.Sprint(map[model.Difficulty]int(d)) {
			t.Errorf("seed %d: distribution %v, want %v", seed, count, d)
		}
		if u := Unique(got); len(u) != len(got) {
			t.Errorf("seed %d: repeats in %v", seed, ids(got))
		}
		if !slices.IsSortedFunc(got, func(a, b model.Question) int { return levelRank(a.Difficulty) - levelRank(b.Difficulty) }) {
			t.Errorf("seed %d: not easiest first: %v", seed, got)
		}
		sets[fmt.Sprint(ids(got))] = true
	}
	if len(sets) < 40 {
		t.Errorf("50 draws gave only %d different sets", len(sets))
	}
}

func TestDrawShortage(t *testing.T) {
	pools := Pools(bank(1))
	got, err := Draw(pools, model.Distribution{"easy": 1, "medium": 3, "hard": 2}, rand.New(rand.NewPCG(1, 1)))
	var se *ShortageError
	if !errors.As(err, &se) || got != nil {
		t.Fatalf("got %v, %v; want *ShortageError and no questions", got, err)
	}
	want := []Shortage{{"medium", 3, 1}, {"hard", 2, 1}}
	if !slices.Equal(se.Shortages, want) {
		t.Errorf("shortages = %v, want %v", se.Shortages, want)
	}
}

func TestUniqueDropsRepeats(t *testing.T) {
	qs := []model.Question{
		{ID: 3, Text: "What is X?", Difficulty: "easy"},
		{ID: 1, Text: "what is x", Difficulty: "hard"},
		{ID: 2, Text: "Other", Difficulty: "easy"},
		{ID: 2, Text: "Other", Difficulty: "easy"},
	}
	if got := ids(Unique(qs)); !slices.Equal(got, []int64{1, 2}) {
		t.Errorf("Unique = %v, want [1 2]", got)
	}
	pools := Pools(qs)
	if len(pools["hard"]) != 1 || len(pools["easy"]) != 1 {
		t.Errorf("pools = %v", pools)
	}
}

func TestDrawN(t *testing.T) {
	qs := bank(3) // 9 questions
	rng := rand.New(rand.NewPCG(7, 7))
	if got := DrawN(qs, 0, rng); len(got) != 9 {
		t.Errorf("n=0: %d questions, want all 9", len(got))
	}
	if got := DrawN(qs, 20, rng); len(got) != 9 {
		t.Errorf("n=20: %d questions, want all 9", len(got))
	}
	sets := map[string]bool{}
	for range 30 {
		got := DrawN(qs, 4, rng)
		if len(got) != 4 || len(Unique(got)) != 4 {
			t.Fatalf("n=4: %v", ids(got))
		}
		sets[fmt.Sprint(ids(got))] = true
	}
	if len(sets) < 2 {
		t.Error("DrawN always returns the same questions; the set must be random")
	}
}
