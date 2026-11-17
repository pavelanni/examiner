// Package statement renders the exam statement using the workbook attached to issue #53.
package statement

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/xuri/excelize/v2"
)

//go:embed template.xlsx
var workbookTemplate []byte

var columns = []string{"№ п/п", "ФИО", "Группа", "Название экзамена/теста",
	"Дата прохождения", "Утверждённая оценка", "Преподаватель", "Дата утверждения"}

// safeCSVText stops spreadsheet formula execution, including after leading whitespace.
func safeCSVText(value string) string {
	trimmed := strings.TrimLeftFunc(value, unicode.IsSpace)
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

// CSV renders UTF-8 with BOM, semicolon separators and eight fields on every row.
func CSV(report model.Statement, generated time.Time, location *time.Location) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	w.UseCRLF = true
	subject := make([]string, len(columns))
	subject[0], subject[1] = "Название предмета", safeCSVText(report.Subject)
	date := make([]string, len(columns))
	date[0], date[1] = "Дата формирования", generated.In(location).Format("02.01.2006")
	if err := w.WriteAll([][]string{subject, date, columns}); err != nil {
		return nil, err
	}
	for i, row := range report.Rows {
		grade, reviewer, reviewed := "Не утверждена", "", ""
		if row.FinalGrade != nil {
			grade = strconv.FormatFloat(*row.FinalGrade, 'f', -1, 64)
			reviewer = safeCSVText(row.Reviewer)
			if row.ReviewedAt != nil {
				reviewed = row.ReviewedAt.In(location).Format("02.01.2006")
			}
		}
		if err := w.Write([]string{strconv.Itoa(i + 1), safeCSVText(row.StudentName),
			safeCSVText(row.GroupName), safeCSVText(row.ExamName),
			row.SubmittedAt.In(location).Format("02.01.2006"), grade, reviewer, reviewed}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// XLSX fills the supplied template. SetCellValue stores strings as text and
// confirmed grades as numbers, so user data never becomes an Excel formula.
func XLSX(report model.Statement, generated time.Time, location *time.Location) (data []byte, err error) {
	f, err := excelize.OpenReader(bytes.NewReader(workbookTemplate))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	sheet := f.GetSheetName(0)
	if err := f.SetCellStr(sheet, "A1", "Название предмета — "+report.Subject); err != nil {
		return nil, err
	}
	if err := f.SetCellStr(sheet, "A2", "Дата формирования — "+generated.In(location).Format("02.01.2006")); err != nil {
		return nil, err
	}
	head := make([]any, len(columns))
	for i, value := range columns {
		head[i] = value
	}
	if err := f.SetSheetRow(sheet, "A3", &head); err != nil {
		return nil, err
	}
	for _, width := range []struct {
		col string
		n   float64
	}{{"A", 8}, {"B", 30}, {"C", 18}, {"D", 35}, {"E", 20}, {"F", 25}, {"G", 30}, {"H", 20}} {
		if err := f.SetColWidth(sheet, width.col, width.col, width.n); err != nil {
			return nil, err
		}
	}
	format := "dd.mm.yyyy"
	dateStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: &format})
	if err != nil {
		return nil, err
	}
	gradeStyle, err := f.NewStyle(&excelize.Style{NumFmt: 0})
	if err != nil {
		return nil, err
	}
	for i, row := range report.Rows {
		n := i + 4
		values := []any{i + 1, row.StudentName, row.GroupName, row.ExamName,
			row.SubmittedAt.In(location), "Не утверждена", "", ""}
		if row.FinalGrade != nil {
			values[5], values[6] = *row.FinalGrade, row.Reviewer
			if row.ReviewedAt != nil {
				values[7] = row.ReviewedAt.In(location)
			}
		}
		if err := f.SetSheetRow(sheet, fmt.Sprintf("A%d", n), &values); err != nil {
			return nil, err
		}
		for _, col := range []string{"E", "H"} {
			cell := fmt.Sprintf("%s%d", col, n)
			if err := f.SetCellStyle(sheet, cell, cell, dateStyle); err != nil {
				return nil, err
			}
		}
		cell := fmt.Sprintf("F%d", n)
		if err := f.SetCellStyle(sheet, cell, cell, gradeStyle); err != nil {
			return nil, err
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
