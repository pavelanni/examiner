package report

import (
	"fmt"
	"strings"

	"github.com/pavelanni/examiner/internal/grader/store"
)

// Generate produces a Markdown report for a student's graded session.
func Generate(data store.ReviewData) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Exam Report: %s\n\n", data.Subject)
	fmt.Fprintf(&b, "**Date:** %s\n", data.Date)
	fmt.Fprintf(&b, "**Exam ID:** %s\n\n", data.ExamID)
	fmt.Fprintf(&b, "**Student:** %s (%s)\n", data.StudentName, data.ExternalID)
	fmt.Fprintf(&b, "**LLM Grade:** %.1f%%\n", data.LLMGrade)

	if data.Grade != nil && data.Grade.FinalGrade != nil {
		fmt.Fprintf(&b, "**Final Grade:** %.1f%%\n", *data.Grade.FinalGrade)
	}

	b.WriteString("\n---\n\n")

	for i, q := range data.Questions {
		fmt.Fprintf(&b, "## Question %d: %s (%s, %d pts)\n\n",
			i+1, q.Topic, q.Difficulty, q.MaxPoints)
		fmt.Fprintf(&b, "> %s\n\n", q.Text)

		if len(q.Messages) > 0 {
			b.WriteString("### Conversation\n\n")
			for _, m := range q.Messages {
				role := "Student"
				if m.Role == "assistant" {
					role = "Examiner"
				}
				if !m.Timestamp.IsZero() {
					fmt.Fprintf(&b, "**%s** (%s):\n",
						role, m.Timestamp.Format("15:04"))
				} else {
					fmt.Fprintf(&b, "**%s**:\n", role)
				}
				fmt.Fprintf(&b, "%s\n\n", m.Content)
			}
		}

		b.WriteString("### Grading\n\n")
		b.WriteString("| | Score | Feedback |\n")
		b.WriteString("|---|---|---|\n")
		fmt.Fprintf(&b, "| LLM | %.1f/%d | %s |\n",
			q.LLMScore, q.MaxPoints, q.LLMFeedback)
		if q.TeacherScore != nil {
			fmt.Fprintf(&b, "| Teacher | %.1f/%d | %s |\n",
				*q.TeacherScore, q.MaxPoints, q.TeacherComment)
		}

		b.WriteString("\n---\n\n")
	}

	b.WriteString("## Summary\n\n")
	fmt.Fprintf(&b, "**LLM Grade:** %.1f%%\n", data.LLMGrade)
	if data.Grade != nil && data.Grade.FinalGrade != nil {
		fmt.Fprintf(&b, "**Final Grade:** %.1f%%\n", *data.Grade.FinalGrade)
	}
	if data.Grade != nil && data.Grade.TeacherComment != "" {
		fmt.Fprintf(&b, "\n**Teacher's Comment:**\n%s\n",
			data.Grade.TeacherComment)
	}

	return b.String()
}
