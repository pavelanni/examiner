package prompts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"unicode/utf8"

	"github.com/pavelanni/examiner/internal/model"
)

var (
	studentAnswerRegex      = regexp.MustCompile(`(?i)</?\s*student-answer\b[^>]*>`)
	systemInstructionsRegex = regexp.MustCompile(`(?i)</?\s*system-instructions\b[^>]*>`)
)

type PromptVariant string

const (
	PromptStrict   PromptVariant = "strict"
	PromptStandard PromptVariant = "standard"
	PromptLenient  PromptVariant = "lenient"
)

var validVariants = map[PromptVariant]bool{
	PromptStrict:   true,
	PromptStandard: true,
	PromptLenient:  true,
}

var (
	loadOnce       sync.Once
	loadErr        error
	evalTemplates  map[PromptVariant]*template.Template
	gradeTemplates map[PromptVariant]*template.Template
)

func IsValidVariant(v string) bool {
	return validVariants[PromptVariant(v)]
}

type EvalData struct {
	QuestionText string
	MaxPoints    int
	Rubric       string
	ModelAnswer  string
	Answer       string
	CanFollowup  bool
}

type GradeData struct {
	QuestionText string
	MaxPoints    int
	Rubric       string
	ModelAnswer  string
	Answer       string
}

func Load(fsys fs.FS) error {
	loadOnce.Do(func() {
		evalTemplates = make(map[PromptVariant]*template.Template)
		gradeTemplates = make(map[PromptVariant]*template.Template)

		variants := []PromptVariant{PromptStrict, PromptStandard, PromptLenient}

		for _, v := range variants {
			evalFile := "prompts/eval_" + string(v) + ".txt"
			gradeFile := "prompts/grade_" + string(v) + ".txt"

			evalContent, err := fs.ReadFile(fsys, evalFile)
			if err != nil {
				loadErr = errors.New("failed to read prompt file " + evalFile + ": " + err.Error())
				return
			}

			evalTmpl, err := template.New("eval").Parse(string(evalContent))
			if err != nil {
				loadErr = errors.New("failed to parse prompt template " + evalFile + ": " + err.Error())
				return
			}
			evalTemplates[v] = evalTmpl

			gradeContent, err := fs.ReadFile(fsys, gradeFile)
			if err != nil {
				loadErr = errors.New("failed to read prompt file " + gradeFile + ": " + err.Error())
				return
			}

			gradeTmpl, err := template.New("grade").Parse(string(gradeContent))
			if err != nil {
				loadErr = errors.New("failed to parse prompt template " + gradeFile + ": " + err.Error())
				return
			}
			gradeTemplates[v] = gradeTmpl
		}
	})
	return loadErr
}

func BuildEvalPrompt(variant PromptVariant, question model.Question, messages []model.Message, maxFollowups int) (string, error) {
	if evalTemplates == nil {
		return "", errors.New("templates not initialized: call Load first")
	}
	tmpl, ok := evalTemplates[variant]
	if !ok {
		if loadErr != nil {
			return "", fmt.Errorf("templates load failed: %w", loadErr)
		}
		return "", errors.New("invalid prompt variant: " + string(variant))
	}

	answer := extractStudentAnswer(messages)
	canFollowup := CountFollowups(messages) < maxFollowups

	data := EvalData{
		QuestionText: question.Text,
		MaxPoints:    question.MaxPoints,
		Rubric:       question.Rubric,
		ModelAnswer:  question.ModelAnswer,
		Answer:       sanitizeAnswer(answer),
		CanFollowup:  canFollowup,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func BuildGradePrompt(variant PromptVariant, question model.Question, messages []model.Message) (string, error) {
	if gradeTemplates == nil {
		return "", errors.New("templates not initialized: call Load first")
	}
	tmpl, ok := gradeTemplates[variant]
	if !ok {
		if loadErr != nil {
			return "", fmt.Errorf("templates load failed: %w", loadErr)
		}
		return "", errors.New("invalid prompt variant: " + string(variant))
	}

	answer := extractConversation(messages)
	answer = sanitizeAnswer(answer)

	data := GradeData{
		QuestionText: question.Text,
		MaxPoints:    question.MaxPoints,
		Rubric:       question.Rubric,
		ModelAnswer:  question.ModelAnswer,
		Answer:       answer,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func extractStudentAnswer(messages []model.Message) string {
	var lastStudent string
	for _, m := range messages {
		if m.Role == model.RoleStudent {
			lastStudent = m.Content
		}
	}
	return lastStudent
}

func extractConversation(messages []model.Message) string {
	var sb strings.Builder
	for _, m := range messages {
		role := "Student"
		if m.Role == model.RoleLLM {
			role = "Assistant"
		}
		sb.WriteString(role + ": " + m.Content + "\n\n")
	}
	return sb.String()
}

// CountFollowups returns the number of LLM messages (follow-up questions) in the conversation.
func CountFollowups(messages []model.Message) int {
	count := 0
	for _, m := range messages {
		if m.Role == model.RoleLLM {
			count++
		}
	}
	return count
}

func sanitizeAnswer(answer string) string {
	answer = studentAnswerRegex.ReplaceAllString(answer, "")
	answer = systemInstructionsRegex.ReplaceAllString(answer, "")
	answer = strings.TrimSpace(answer)

	if answer == "" {
		return "[No answer provided]"
	}

	if utf8.RuneCountInString(answer) > 10000 {
		runes := []rune(answer)
		runes = runes[:10000]
		answer = string(runes) + "\n\n[Answer truncated due to length]"
	}

	return answer
}
