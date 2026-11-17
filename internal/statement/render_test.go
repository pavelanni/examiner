package statement

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/xuri/excelize/v2"
)

func statementFixture() model.Statement {
	zero := 0.0
	date := time.Date(2026, 11, 17, 0, 30, 0, 0, time.FixedZone("Moscow", 3*3600))
	return model.Statement{Subject: "=Физика", Rows: []model.StatementRow{
		{StudentName: "=Иванов; \"Иван\"\nИванович", GroupName: "+Г-1", ExamName: "@Тест", SubmittedAt: date},
		{StudentName: "Петров", GroupName: "-Г-2", ExamName: "Механика", SubmittedAt: date, FinalGrade: &zero, Reviewer: "Преподаватель", ReviewedAt: &date},
	}}
}

// TestCSVStatement checks BOM, RFC quoting, equal field counts and formula safety.
func TestCSVStatement(t *testing.T) {
	report := statementFixture()
	data, err := CSV(report, report.Rows[0].SubmittedAt, report.Rows[0].SubmittedAt.Location())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		t.Fatal("missing UTF-8 BOM")
	}
	reader := csv.NewReader(bytes.NewReader(data[3:]))
	reader.Comma = ';'
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, row := range rows {
		if len(row) != 8 {
			t.Fatalf("field count = %d", len(row))
		}
	}
	if rows[0][1] != "'=Физика" || !strings.Contains(rows[1][1], "17.11.2026") {
		t.Fatalf("metadata = %v", rows[:2])
	}
	if rows[3][1] != "'"+report.Rows[0].StudentName || rows[3][2] != "'+Г-1" || rows[3][3] != "'@Тест" {
		t.Fatalf("escaped text = %v", rows[3])
	}
	if rows[3][5] != "Не утверждена" || rows[3][6] != "" || rows[3][7] != "" || rows[4][5] != "0" {
		t.Fatalf("approval values = %v", rows[3:])
	}
}

// TestXLSXStatement checks the supplied template, literal text and numeric dates/grades.
func TestXLSXStatement(t *testing.T) {
	report := statementFixture()
	date := report.Rows[0].SubmittedAt
	data, err := XLSX(report, date, date.Location())
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	for cell, want := range map[string]string{
		"A1": "Название предмета — =Физика", "A2": "Дата формирования — 17.11.2026",
		"B3": "ФИО", "C3": "Группа", "B4": report.Rows[0].StudentName,
		"F4": "Не утверждена", "G4": "", "H4": "", "F5": "0",
		"E5": "17.11.2026", "H5": "17.11.2026",
	} {
		got, err := f.GetCellValue(sheet, cell)
		if err != nil || got != want {
			t.Errorf("%s = %q, %v, want %q", cell, got, err, want)
		}
	}
	for _, cell := range []string{"A1", "B4", "C4", "D4"} {
		formula, err := f.GetCellFormula(sheet, cell)
		if err != nil || formula != "" {
			t.Errorf("unexpected formula at %s: %q, %v", cell, formula, err)
		}
	}
	kind, err := f.GetCellType(sheet, "F5")
	if err != nil || (kind != excelize.CellTypeNumber && kind != excelize.CellTypeUnset) {
		t.Errorf("grade type = %v, %v", kind, err)
	}
}
