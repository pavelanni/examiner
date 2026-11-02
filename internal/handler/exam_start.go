package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/selection"
)

// blueprintID is the only exam blueprint until blueprints can be managed.
const blueprintID = 1

// errNoQuestions means no question in the topic matches the exam settings.
var errNoQuestions = errors.New("no questions match the exam settings")

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	h.renderIndex(w, r, http.StatusOK, "")
}

// renderIndex renders the home page; errMsg explains a failed exam start.
func (h *Handler) renderIndex(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	ctx := r.Context()
	user := model.UserFromContext(ctx)

	var sessions []model.ExamSession
	var err error
	if user.Role == model.UserRoleStudent {
		sessions, err = h.store.ListSessionsByUser(user.ID)
	} else {
		sessions, err = h.store.ListSessions()
	}
	if err != nil {
		slog.Error("failed to list sessions", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The user's own sessions decide which topics they can continue or start.
	mine := sessions
	if user.Role != model.UserRoleStudent {
		if mine, err = h.store.ListSessionsByUser(user.ID); err != nil {
			slog.Error("failed to list own sessions", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	allTopics, err := h.store.ListPublishedTopics()
	if err != nil {
		slog.Error("failed to list topics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// If --topic is set, restrict topics to only that value.
	var topics []string
	for _, t := range allTopics {
		if h.config.Topic == "" || t == h.config.Topic {
			topics = append(topics, t)
		}
	}

	// The questions exams draw from, by topic.
	pool, err := h.store.ListQuestionsFiltered(h.difficultyFilter(), h.config.Topic)
	if err != nil {
		slog.Error("failed to list filtered questions", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pool = selection.Unique(pool)
	byTopic := make(map[string][]model.Question)
	for _, q := range pool {
		byTopic[q.Topic] = append(byTopic[q.Topic], q)
	}

	examCount := len(pool)
	if d := h.config.Selection; d != nil {
		examCount = d.Total()
	} else if h.config.NumQuestions > 0 && h.config.NumQuestions < examCount {
		examCount = h.config.NumQuestions
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	err = views.IndexPage(views.IndexData{
		Sessions:       sessions,
		AvailableCount: len(pool),
		ExamCount:      examCount,
		Config:         h.config,
		Topics:         h.topicChoices(ctx, user, topics, byTopic, mine),
		Error:          errMsg,
	}).Render(ctx, w)
	if err != nil {
		slog.Error("render error", "error", err)
	}
}

// difficultyFilter returns the difficulties exams may use, as
// ListQuestionsFiltered takes them; a distribution names its own.
func (h *Handler) difficultyFilter() string {
	if h.config.Selection != nil {
		return ""
	}
	return h.config.Difficulty
}

// topicChoices says, per topic, whether the user can continue an exam,
// start one, or neither and why. mine are the user's sessions, newest first.
func (h *Handler) topicChoices(ctx context.Context, user *model.User, topics []string, byTopic map[string][]model.Question, mine []model.ExamSession) []views.TopicChoice {
	// Per topic, the session a start would return: in progress, else newest.
	latest := make(map[string]model.ExamSession)
	for _, s := range mine {
		if s.Topic == "" || s.BlueprintID != blueprintID {
			continue
		}
		if cur, ok := latest[s.Topic]; !ok || (s.Status == model.StatusInProgress && cur.Status != model.StatusInProgress) {
			latest[s.Topic] = s
		}
	}
	retake := canRetake(user)

	out := make([]views.TopicChoice, 0, len(topics))
	for _, topic := range topics {
		c := views.TopicChoice{Topic: topic, Available: true}
		if s, ok := latest[topic]; ok {
			switch {
			case s.Status == model.StatusInProgress:
				c.Available, c.Active = false, &s
			case !retake:
				c.Available, c.Note = false, appI18n.T(ctx, "ExamTaken")
			}
		}
		if c.Available {
			if note := h.unavailableNote(ctx, byTopic[topic]); note != "" {
				c.Available, c.Note = false, note
			}
		}
		out = append(out, c)
	}
	return out
}

// unavailableNote explains why no exam can be drawn from a topic's
// questions, or returns "" if one can.
func (h *Handler) unavailableNote(ctx context.Context, questions []model.Question) string {
	if d := h.config.Selection; d != nil {
		return shortageText(ctx, "ExamShortageShort", "", selection.Shortages(selection.Pools(questions), d))
	}
	if len(questions) == 0 {
		return appI18n.T(ctx, "ExamNoQuestions")
	}
	return ""
}

// shortageText renders shortages with message msgID, one per difficulty.
func shortageText(ctx context.Context, msgID, topic string, shortages []selection.Shortage) string {
	parts := make([]string, 0, len(shortages))
	for _, s := range shortages {
		parts = append(parts, appI18n.Td(ctx, msgID, map[string]any{
			"Topic": topic, "Difficulty": string(s.Difficulty), "Need": s.Need, "Have": s.Have,
		}))
	}
	return strings.Join(parts, " ")
}

// canRetake reports whether user may start a new exam in a topic after
// finishing one. Students get one attempt per topic (FR3-05); teachers and
// admins try exams out.
func canRetake(user *model.User) bool {
	return user.Role != model.UserRoleStudent
}

func (h *Handler) handleStartExam(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := model.UserFromContext(ctx)

	// Use topic from form (dropdown) if provided, otherwise fall back to CLI flag.
	topic := strings.TrimSpace(r.FormValue("topic"))
	if topic == "" {
		topic = h.config.Topic
	}
	if topic == "" || (h.config.Topic != "" && topic != h.config.Topic) {
		h.renderIndex(w, r, http.StatusBadRequest, appI18n.T(ctx, "ExamChooseTopic"))
		return
	}
	retake := canRetake(user)

	// The variant is fixed (FR3-05): a student who already has a session
	// for this exam goes back to it instead of drawing new questions.
	existing, err := h.store.FindExamSession(blueprintID, user.ID, topic, retake)
	if err != nil {
		slog.Error("failed to find exam session", "user", user.ID, "topic", topic, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		h.redirectToSession(w, r, *existing)
		return
	}

	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	questions, err := h.drawQuestions(topic, rng)
	var short *selection.ShortageError
	switch {
	case errors.As(err, &short):
		h.renderIndex(w, r, http.StatusConflict, shortageText(ctx, "ExamShortage", topic, short.Shortages))
		return
	case errors.Is(err, errNoQuestions):
		h.renderIndex(w, r, http.StatusBadRequest, appI18n.Td(ctx, "ExamNoQuestionsInTopic", map[string]any{"Topic": topic}))
		return
	case err != nil:
		slog.Error("failed to draw questions", "topic", topic, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	questionIDs := make([]int64, 0, len(questions))
	for _, q := range questions {
		questionIDs = append(questionIDs, q.ID)
	}
	// StartSession checks again inside its transaction: of two concurrent
	// starts, the second gets the first one's session.
	sess, created, err := h.store.StartSession(blueprintID, user.ID, topic, questionIDs, retake)
	if err != nil {
		slog.Error("failed to create session", "user", user.ID, "topic", topic, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if created {
		slog.Info("exam started", "session_id", sess.ID, "user", user.ID, "topic", topic, "questions", len(questionIDs))
	}
	h.redirectToSession(w, r, sess)
}

// drawQuestions draws the question set of a new session in topic: exactly
// the configured number per difficulty or, without a distribution,
// num-questions from the allowed difficulties. The set is always random;
// the shuffle setting only randomizes the order, which is otherwise
// easiest first.
func (h *Handler) drawQuestions(topic string, rng *rand.Rand) ([]model.Question, error) {
	questions, err := h.store.ListQuestionsFiltered(h.difficultyFilter(), topic)
	if err != nil {
		return nil, err
	}
	var picked []model.Question
	if d := h.config.Selection; d != nil {
		if picked, err = selection.Draw(selection.Pools(questions), d, rng); err != nil {
			return nil, err
		}
	} else {
		picked = selection.DrawN(questions, h.config.NumQuestions, rng)
	}
	if len(picked) == 0 {
		return nil, errNoQuestions
	}
	if h.config.Shuffle {
		rng.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
	}
	return picked, nil
}

// redirectToSession sends the user to a session: the exam while it is in
// progress, otherwise the results.
func (h *Handler) redirectToSession(w http.ResponseWriter, r *http.Request, sess model.ExamSession) {
	target := fmt.Sprintf("/exam/%d", sess.ID)
	if sess.Status != model.StatusInProgress {
		target = fmt.Sprintf("/results/%d", sess.ID)
	}
	http.Redirect(w, r, h.path(target), http.StatusSeeOther)
}
