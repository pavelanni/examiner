// Package selection builds the question set of an exam session: the exact
// number of questions per difficulty (FR3-01, FR3-02), drawn at random and
// without repeats (FR3-03). Everything here is a pure function of its
// arguments; the random source is passed in.
package selection

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/pavelanni/examiner/internal/model"
)

// Parse reads the question_selection config value, as viper returns it for
// the key: a map from difficulty to count. nil means the key is not set and
// returns a nil Distribution. Counts must be whole numbers of at least 0,
// difficulties must be known, and the total must be above 0. Parse reports
// every problem, not only the first.
func Parse(raw any) (model.Distribution, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := toMap(raw)
	if !ok {
		return nil, fmt.Errorf("must map difficulties (%s) to question counts", levelList())
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	d := model.Distribution{}
	var errs []error
	for _, k := range keys {
		level := model.Difficulty(strings.ToLower(strings.TrimSpace(k)))
		if !slices.Contains(model.Difficulties, level) {
			errs = append(errs, fmt.Errorf("%s: unknown difficulty; use %s", k, levelList()))
			continue
		}
		n, err := count(m[k])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
			continue
		}
		d[level] = n
	}
	if len(errs) == 0 && d.Total() == 0 {
		errs = append(errs, errors.New("the total number of questions must be greater than 0"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return d, nil
}

func toMap(raw any) (map[string]any, bool) {
	switch m := raw.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[fmt.Sprint(k)] = v
		}
		return out, true
	}
	return nil, false
}

// count accepts a whole number of at least 0. Strings are rejected even
// when they hold digits: "2" in YAML is a quoting mistake, not a count.
func count(v any) (int, error) {
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int8:
		n = int64(x)
	case int16:
		n = int64(x)
	case int32:
		n = int64(x)
	case int64:
		n = x
	case uint:
		n = int64(x)
	case uint8:
		n = int64(x)
	case uint16:
		n = int64(x)
	case uint32:
		n = int64(x)
	case uint64:
		if x > math.MaxInt32 {
			return 0, fmt.Errorf("%d is too large", x)
		}
		n = int64(x)
	case float32:
		return count(float64(x))
	case float64:
		// JSON configs decode every number as a float; accept whole ones.
		if x != math.Trunc(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("must be a whole number, got %v", x)
		}
		if math.Abs(x) > math.MaxInt32 {
			return 0, fmt.Errorf("%v is too large", x)
		}
		n = int64(x)
	default:
		return 0, fmt.Errorf("must be a whole number, got %q", fmt.Sprint(v))
	}
	if n < 0 {
		return 0, fmt.Errorf("must not be negative, got %d", n)
	}
	if n > math.MaxInt32 {
		return 0, fmt.Errorf("%d is too large", n)
	}
	return int(n), nil
}

func levelList() string {
	names := make([]string, 0, len(model.Difficulties))
	for _, l := range model.Difficulties {
		names = append(names, string(l))
	}
	return strings.Join(names, ", ")
}

// Unique returns the questions in ID order without repeats: a question
// with the same ID or the same normalized text as an earlier one is dropped
// (legacy rows may duplicate each other, #43).
func Unique(questions []model.Question) []model.Question {
	sorted := slices.Clone(questions)
	slices.SortFunc(sorted, func(a, b model.Question) int { return cmp.Compare(a.ID, b.ID) })
	seenID := make(map[int64]bool, len(sorted))
	seenText := make(map[string]bool, len(sorted))
	out := sorted[:0]
	for _, q := range sorted {
		norm := q.TextNorm
		if norm == "" {
			norm = model.NormalizeQuestionText(q.Text)
		}
		if seenID[q.ID] || seenText[norm] {
			continue
		}
		seenID[q.ID], seenText[norm] = true, true
		out = append(out, q)
	}
	return out
}

// Pools groups the questions by difficulty after Unique.
func Pools(questions []model.Question) map[model.Difficulty][]model.Question {
	pools := make(map[model.Difficulty][]model.Question)
	for _, q := range Unique(questions) {
		pools[q.Difficulty] = append(pools[q.Difficulty], q)
	}
	return pools
}

// Shortage is a difficulty with fewer questions than an exam needs.
type Shortage struct {
	Difficulty model.Difficulty
	Need, Have int
}

// Shortages lists, easiest first, the difficulties whose pool is too small
// for d.
func Shortages(pools map[model.Difficulty][]model.Question, d model.Distribution) []Shortage {
	var out []Shortage
	for _, level := range model.Difficulties {
		if need, have := d[level], len(pools[level]); need > have {
			out = append(out, Shortage{Difficulty: level, Need: need, Have: have})
		}
	}
	return out
}

// ShortageError reports that a question set cannot be drawn.
type ShortageError struct {
	Shortages []Shortage
}

func (e *ShortageError) Error() string {
	parts := make([]string, 0, len(e.Shortages))
	for _, s := range e.Shortages {
		parts = append(parts, fmt.Sprintf("%s: need %d, have %d", s.Difficulty, s.Need, s.Have))
	}
	return "not enough questions: " + strings.Join(parts, "; ")
}

// Draw picks exactly d[level] questions at random from each pool, without
// repeats, and returns them easiest first and by ID within a difficulty.
// It returns *ShortageError, and no questions, if a pool is too small.
func Draw(pools map[model.Difficulty][]model.Question, d model.Distribution, rng *rand.Rand) ([]model.Question, error) {
	if s := Shortages(pools, d); len(s) > 0 {
		return nil, &ShortageError{Shortages: s}
	}
	out := make([]model.Question, 0, d.Total())
	for _, level := range model.Difficulties {
		out = append(out, sample(pools[level], d[level], rng)...)
	}
	sortByLevel(out)
	return out, nil
}

// DrawN picks n questions at random from questions after Unique (all of
// them when n <= 0 or n is larger), easiest first and by ID within a
// difficulty. It serves the num-questions setting.
func DrawN(questions []model.Question, n int, rng *rand.Rand) []model.Question {
	unique := Unique(questions)
	if n <= 0 || n > len(unique) {
		n = len(unique)
	}
	out := sample(unique, n, rng)
	sortByLevel(out)
	return out
}

// sample returns n distinct elements of qs chosen uniformly at random.
func sample(qs []model.Question, n int, rng *rand.Rand) []model.Question {
	c := slices.Clone(qs)
	// Partial Fisher-Yates: the first n slots end up a uniform sample.
	for i := 0; i < n; i++ {
		j := i + rng.IntN(len(c)-i)
		c[i], c[j] = c[j], c[i]
	}
	return c[:n]
}

func sortByLevel(qs []model.Question) {
	slices.SortFunc(qs, func(a, b model.Question) int {
		if ra, rb := levelRank(a.Difficulty), levelRank(b.Difficulty); ra != rb {
			return ra - rb
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// levelRank orders known difficulties easiest first and unknown ones last.
func levelRank(d model.Difficulty) int {
	if i := slices.Index(model.Difficulties, d); i >= 0 {
		return i
	}
	return len(model.Difficulties)
}
