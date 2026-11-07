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

// handleIndex renders the available exam topics and previous attempts.
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

	allTopics, err := h.store.ListDistinctTopics()
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
	if h.config.Topic != "" && len(topics) == 0 {
		topics = append(topics, h.config.Topic)
	}

	// The questions exams draw from, by topic.
	pool, err := h.store.ListQuestionsFiltered(h.difficultyFilter(), h.config.Topic)
	if err != nil {
		slog.Error("failed to list filtered questions", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byTopic := make(map[string][]model.Question)
	for _, q := range pool {
		byTopic[q.Topic] = append(byTopic[q.Topic], q)
	}
	// A variant draws from one topic. Deduplicate within that topic, just
	// as drawQuestions does; matching text in another topic is still usable.
	availableCount := 0
	for topic, questions := range byTopic {
		availableCount += len(selection.Unique(questions))
		// Distribution-aware allocation needs all difficulty alternatives.
		if h.config.Selection == nil {
			byTopic[topic] = selection.Unique(questions)
		}
	}

	examCount := availableCount
	if d := h.config.Selection; d != nil {
		examCount = d.Total()
	} else if h.config.NumQuestions > 0 && h.config.NumQuestions < examCount {
		examCount = h.config.NumQuestions
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	err = views.IndexPage(views.IndexData{
		Sessions:       sessions,
		AvailableCount: availableCount,
		ExamCount:      examCount,
		Config:         h.config,
		Topics:         h.topicChoices(ctx, topics, byTopic),
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

// topicChoices marks topics that can supply a complete exam.
func (h *Handler) topicChoices(ctx context.Context, topics []string, byTopic map[string][]model.Question) []views.TopicChoice {
	out := make([]views.TopicChoice, 0, len(topics))
	for _, topic := range topics {
		note := h.unavailableNote(ctx, byTopic[topic])
		out = append(out, views.TopicChoice{Topic: topic, Available: note == "", Note: note})
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

// handleStartExam validates the selected topic and creates a complete variant.
func (h *Handler) handleStartExam(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	selected := r.Form.Has("topic")
	topic := r.Form.Get("topic")
	if !selected {
		topic = h.config.Topic
	}
	if h.config.Selection != nil && ((!selected && topic == "") || (h.config.Topic != "" && topic != h.config.Topic)) {
		h.renderIndex(w, r, http.StatusBadRequest, appI18n.T(ctx, "ExamChooseTopic"))
		return
	}
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	var topicFilter *string
	if selected || h.config.Topic != "" {
		topicFilter = &topic
	}
	questions, err := h.drawQuestionsFiltered(topicFilter, rng)
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
	user := model.UserFromContext(ctx)
	sessionID, err := h.store.CreateSession(blueprintID, user.ID, questionIDs)
	if err != nil {
		slog.Error("failed to create session", "user", user.ID, "topic", topic, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, h.path(fmt.Sprintf("/exam/%d", sessionID)), http.StatusSeeOther)
}

// drawQuestions uses the exact distribution when configured. Without it,
// num-questions, difficulty and shuffle retain their previous behavior.
func (h *Handler) drawQuestions(topic string, rng *rand.Rand) ([]model.Question, error) {
	return h.drawQuestionsFiltered(&topic, rng)
}

// drawQuestionsFiltered keeps an omitted legacy topic distinct from an empty one.
func (h *Handler) drawQuestionsFiltered(topic *string, rng *rand.Rand) ([]model.Question, error) {
	var questions []model.Question
	var err error
	if topic == nil {
		questions, err = h.store.ListQuestionsFiltered(h.difficultyFilter(), "")
	} else {
		questions, err = h.store.ListQuestionsInTopic(h.difficultyFilter(), *topic)
	}
	if err != nil {
		return nil, err
	}
	var picked []model.Question
	if d := h.config.Selection; d != nil {
		picked, err = selection.Draw(selection.Pools(questions), d, rng)
		if err != nil {
			return nil, err
		}
	} else {
		picked = selection.Unique(questions)
	}
	if len(picked) == 0 {
		return nil, errNoQuestions
	}
	if h.config.Shuffle {
		rng.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
	}
	if h.config.Selection == nil && h.config.NumQuestions > 0 && h.config.NumQuestions < len(picked) {
		picked = picked[:h.config.NumQuestions]
	}
	return picked, nil
}
