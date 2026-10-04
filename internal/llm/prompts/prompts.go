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

	"github.com/pavelanni/examiner/internal/model"
)

// reservedTagRegex matches the tags the prompts use for structure. They are
// stripped from student text so it cannot close its own block or forge
// a follow-up or a second answer.
var reservedTagRegex = regexp.MustCompile(`(?i)</?\s*(student-answer|system-instructions|dialogue|answer|followup)\b[^>]*>`)

const (
	// MaxAnswerRunes caps each student answer and each follow-up question.
	MaxAnswerRunes = 4000
	// MaxDialogueRunes caps the whole dialogue; earlier parts shrink first.
	MaxDialogueRunes = 10000
)

// PromptVariant represents a grading prompt variant.
type PromptVariant string

const (
	// PromptStrict is a strict grading variant for majors.
	PromptStrict PromptVariant = "strict"
	// PromptStandard is the default grading variant.
	PromptStandard PromptVariant = "standard"
	// PromptLenient is a lenient grading variant for electives.
	PromptLenient PromptVariant = "lenient"
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

// IsValidVariant checks if a prompt variant name is valid.
func IsValidVariant(v string) bool {
	return validVariants[PromptVariant(v)]
}

// EvalData holds template data for evaluation prompts.
type EvalData struct {
	QuestionText string
	MaxPoints    int
	Rubric       string
	ModelAnswer  string
	Dialogue     string // student answers and examiner follow-ups, see buildDialogue
	CanFollowup  bool
}

// GradeData holds template data for grading prompts.
type GradeData struct {
	QuestionText string
	MaxPoints    int
	Rubric       string
	ModelAnswer  string
	Dialogue     string // student answers and examiner follow-ups, see buildDialogue
}

// Load loads prompt templates from the embedded filesystem.
// It uses sync.Once to ensure templates are loaded only once.
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

// BuildEvalPrompt builds an evaluation prompt using the specified variant.
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

	canFollowup := CountFollowups(messages) < maxFollowups

	data := EvalData{
		QuestionText: question.Text,
		MaxPoints:    question.MaxPoints,
		Rubric:       question.Rubric,
		ModelAnswer:  question.ModelAnswer,
		Dialogue:     buildDialogue(messages),
		CanFollowup:  canFollowup,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// BuildGradePrompt builds a final grading prompt using the specified variant.
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

	data := GradeData{
		QuestionText: question.Text,
		MaxPoints:    question.MaxPoints,
		Rubric:       question.Rubric,
		ModelAnswer:  question.ModelAnswer,
		Dialogue:     buildDialogue(messages),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// dialoguePart is one <answer> or <followup> element of the dialogue.
type dialoguePart struct {
	tag       string // "answer" or "followup"
	n         int    // number of the student answer it belongs to
	text      []rune
	truncated bool
}

// minPartRunes is how much of a part survives when the dialogue as a whole
// is over MaxDialogueRunes.
const minPartRunes = 200

const truncatedMarker = "\n[truncated]"

// buildDialogue renders the conversation for the model: every student answer
// in order as <answer n="i">, with the examiner's follow-up question that
// followed it as <followup n="i">. The examiner's feedback is left out on
// purpose: the model must grade the student's words, not its own earlier
// remarks. Each part is capped at MaxAnswerRunes; if the whole is still over
// MaxDialogueRunes, the earliest parts are shortened first, so the latest
// answers are the ones that survive.
func buildDialogue(messages []model.Message) string {
	var parts []dialoguePart
	answers := 0
	for _, m := range messages {
		switch {
		case m.Role == model.RoleStudent:
			answers++
			parts = append(parts, newPart("answer", answers, m.Content, true))
		case m.Role == model.RoleLLM && m.Followup != "":
			parts = append(parts, newPart("followup", answers, m.Followup, false))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, newPart("answer", 1, "", true))
	}

	total := 0
	for _, p := range parts {
		total += len(p.text)
	}
	for i := 0; i < len(parts)-1 && total > MaxDialogueRunes; i++ {
		keep := max(minPartRunes, len(parts[i].text)-(total-MaxDialogueRunes))
		if keep < len(parts[i].text) {
			total -= len(parts[i].text) - keep
			parts[i].text = parts[i].text[:keep]
			parts[i].truncated = true
		}
	}

	var sb strings.Builder
	sb.WriteString("<dialogue>\n")
	for _, p := range parts {
		fmt.Fprintf(&sb, "<%s n=\"%d\">\n%s", p.tag, p.n, string(p.text))
		if p.truncated {
			sb.WriteString(truncatedMarker)
		}
		fmt.Fprintf(&sb, "\n</%s>\n", p.tag)
	}
	sb.WriteString("</dialogue>")
	return sb.String()
}

func newPart(tag string, n int, text string, placeholder bool) dialoguePart {
	text = strings.TrimSpace(reservedTagRegex.ReplaceAllString(text, ""))
	if text == "" && placeholder {
		text = "[No answer provided]"
	}
	runes := []rune(text)
	p := dialoguePart{tag: tag, n: n, text: runes}
	if len(runes) > MaxAnswerRunes {
		p.text = runes[:MaxAnswerRunes]
		p.truncated = true
	}
	return p
}

// CountFollowups returns the number of follow-up questions the examiner has
// asked in the conversation. Feedback messages without a question don't count.
func CountFollowups(messages []model.Message) int {
	count := 0
	for _, m := range messages {
		if m.Role == model.RoleLLM && m.Followup != "" {
			count++
		}
	}
	return count
}
