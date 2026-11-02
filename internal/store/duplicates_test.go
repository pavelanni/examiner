package store

import "testing"

func TestDuplicateQuestionGroups(t *testing.T) {
	s := newTestStore(t)
	if groups, err := s.DuplicateQuestionGroups(); err != nil || len(groups) != 0 {
		t.Fatalf("empty store: %v, %v", groups, err)
	}
	// Legacy rows: the duplicate checks did not exist when they were stored.
	a1 := insertTestQuestion(t, s, "What is X?", "easy", "x")
	insertTestQuestion(t, s, "Unique", "easy", "x")
	a2 := insertTestQuestion(t, s, "  what is x ", "easy", "x")
	insertTestQuestion(t, s, "What is X", "easy", "y") // same text, other topic
	b1 := insertTestQuestion(t, s, "Ёлка", "easy", "a")
	b2 := insertTestQuestion(t, s, "елка.", "easy", "a")
	a3 := insertTestQuestion(t, s, "WHAT IS X!", "easy", "x")

	groups, err := s.DuplicateQuestionGroups()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		topic string
		ids   []int64
	}{{"a", []int64{b1, b2}}, {"x", []int64{a1, a2, a3}}}
	if len(groups) != len(want) {
		t.Fatalf("groups = %+v; want %d", groups, len(want))
	}
	for i, w := range want {
		g := groups[i]
		if g.Topic != w.topic || len(g.Questions) != len(w.ids) {
			t.Errorf("group %d = %+v; want topic %s, ids %v", i, g, w.topic, w.ids)
			continue
		}
		for j, q := range g.Questions {
			if q.ID != w.ids[j] {
				t.Errorf("group %d question %d = #%d; want #%d", i, j, q.ID, w.ids[j])
			}
		}
	}
}

func TestFindQuestionByCourseAndText(t *testing.T) {
	s := newTestStore(t)
	id := insertTestQuestion(t, s, "A", "easy", "x")
	if q, err := s.FindQuestionByCourseAndText(1, "A"); err != nil || q == nil || q.ID != id {
		t.Errorf("hit = %+v, %v; want id %d", q, err, id)
	}
	for _, tc := range []struct {
		course int64
		text   string
	}{{1, "a"}, {2, "A"}} {
		if q, err := s.FindQuestionByCourseAndText(tc.course, tc.text); q != nil || err != nil {
			t.Errorf("FindQuestionByCourseAndText(%d, %q) = %+v, %v; want nil, nil", tc.course, tc.text, q, err)
		}
	}
}
