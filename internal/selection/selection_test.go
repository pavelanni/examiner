package selection

import (
	"errors"
	"fmt"
	"math"
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

// TestParseValid accepts supported distributions decoded from YAML.
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

// TestParseNotSet keeps legacy mode when the distribution key is absent.
func TestParseNotSet(t *testing.T) {
	d, err := Parse(fromYAML(t, "num-questions: 3\n"))
	if d != nil || err != nil {
		t.Errorf("got %v, %v; want nil, nil", d, err)
	}
}

// TestParseInvalid rejects malformed counts and reports all invalid entries.
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

// TestParseJSONNumbers accepts integral numbers decoded from JSON.
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

// ids extracts IDs for set and order comparisons.
func ids(qs []model.Question) []int64 {
	out := make([]int64, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}

// TestDrawExactDistribution checks exact counts, uniqueness and varied sets across seeds.
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

// TestDrawShortage rejects incomplete variants with per-difficulty diagnostics.
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

// TestUniqueDropsRepeats keeps canonical IDs without discarding other difficulty alternatives.
func TestUniqueDropsRepeats(t *testing.T) {
	qs := []model.Question{
		{ID: 3, Text: "what is x", Difficulty: "easy"},
		{ID: 1, Text: "what is x", Difficulty: "hard"},
		{ID: 2, Text: "Other", Difficulty: "easy"},
		{ID: 2, Text: "Other", Difficulty: "easy"},
	}
	if got := ids(Unique(qs)); !slices.Equal(got, []int64{1, 2}) {
		t.Errorf("Unique = %v, want [1 2]", got)
	}
	pools := Pools(qs)
	if len(pools["hard"]) != 1 || len(pools["easy"]) != 2 {
		t.Errorf("pools = %v", pools)
	}
}

// TestUniqueNormalizedByTopic deduplicates normalized text within the same topic
// while keeping identical texts in different topics distinct.
func TestUniqueNormalizedByTopic(t *testing.T) {
	qs := []model.Question{
		{ID: 1, Topic: "go", Text: "What is a channel?", Difficulty: "easy"},
		{ID: 2, Topic: "go", Text: "  what is a   channel  ", Difficulty: "medium"},
		{ID: 3, Topic: "concurrency", Text: "What is a channel?", Difficulty: "easy"},
	}
	if got := ids(Unique(qs)); !slices.Equal(got, []int64{1, 3}) {
		t.Errorf("Unique = %v, want [1 3]", got)
	}
}

// TestParseTotalBounds checks the same raw values on both 32- and 64-bit builds.
func TestParseTotalBounds(t *testing.T) {
	d, err := Parse(map[string]any{"easy": int64(math.MaxInt32), "medium": 1})
	if int64(int(^uint(0)>>1)) == math.MaxInt32 {
		if err == nil || !strings.Contains(err.Error(), "too large for this platform") {
			t.Fatalf("overflow must be rejected: %v, %v", d, err)
		}
	} else if err != nil || int64(d.Total()) != int64(math.MaxInt32)+1 {
		t.Fatalf("representable total must be preserved: %v, %v", d, err)
	}
}

// TestDrawAllocatesDuplicateDifficulties exercises alternatives and impossible overlaps.
func TestDrawAllocatesDuplicateDifficulties(t *testing.T) {
	bank := []model.Question{
		{ID: 1, Text: "shared", Difficulty: "easy"},
		{ID: 2, Text: "shared", Difficulty: "hard"},
		{ID: 3, Text: "alternative", Difficulty: "easy"},
	}
	for _, test := range []struct {
		name string
		qs   []model.Question
		d    model.Distribution
		ok   bool
	}{
		{"zero easy", bank[:2], model.Distribution{"easy": 0, "hard": 1}, true},
		{"must reassign", bank, model.Distribution{"easy": 1, "hard": 1}, true},
		{"shared text cannot fill two slots", bank[:2], model.Distribution{"easy": 1, "hard": 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pools := Pools(test.qs)
			if got := len(Shortages(pools, test.d)) == 0; got != test.ok {
				t.Fatalf("availability=%v, want %v", got, test.ok)
			}
			for seed := range uint64(20) {
				qs, err := Draw(pools, test.d, rand.New(rand.NewPCG(seed, 1)))
				if (err == nil) != test.ok {
					t.Fatalf("seed %d: %v", seed, err)
				}
				if !test.ok {
					if qs != nil {
						t.Fatal("shortage returned partial questions")
					}
					continue
				}
				if len(qs) != test.d.Total() || len(Unique(qs)) != len(qs) {
					t.Fatalf("invalid draw: %v", qs)
				}
				counts := make(model.Distribution)
				for _, q := range qs {
					counts[q.Difficulty]++
				}
				for level, need := range test.d {
					if counts[level] != need {
						t.Fatalf("distribution %v, want %v", counts, test.d)
					}
				}
			}
		})
	}
}

// TestAllocationAgainstExhaustiveChoices compares availability with every small valid set.
func TestAllocationAgainstExhaustiveChoices(t *testing.T) {
	for a := 1; a < 8; a++ {
		for b := 1; b < 8; b++ {
			for c := 1; c < 8; c++ {
				masks := []int{a, b, c}
				var qs []model.Question
				for group, mask := range masks {
					for level, difficulty := range model.Difficulties {
						if mask&(1<<level) != 0 {
							qs = append(qs, model.Question{ID: int64(len(qs) + 1), Text: fmt.Sprint("text", group), Difficulty: difficulty})
						}
					}
				}
				var possible func(int, [3]int) bool
				possible = func(group int, remaining [3]int) bool {
					if group == len(masks) {
						return remaining == [3]int{}
					}
					if possible(group+1, remaining) {
						return true
					}
					for level := range 3 {
						if remaining[level] > 0 && masks[group]&(1<<level) != 0 {
							next := remaining
							next[level]--
							if possible(group+1, next) {
								return true
							}
						}
					}
					return false
				}
				pools := Pools(qs)
				for easy := range 3 {
					for medium := range 3 {
						for hard := range 3 {
							d := model.Distribution{"easy": easy, "medium": medium, "hard": hard}
							if got, want := len(Shortages(pools, d)) == 0, possible(0, [3]int{easy, medium, hard}); got != want {
								t.Fatalf("masks=%v, distribution=%v: available=%v, want %v", masks, d, got, want)
							}
						}
					}
				}
			}
		}
	}
}
