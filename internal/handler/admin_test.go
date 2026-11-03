package handler

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func TestAdminUploadDuplicateProtection(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h := &Handler{store: s}

	// 1. Подготавливаем JSON с двумя вопросами в одной теме,
	// которые отличаются только регистром, пробелами и знаком вопроса.
	jsonContent := `[
		{"text": "Что такое ПИД-регулятор?", "difficulty": "easy", "topic": "control", "rubric": "r", "model_answer": "a", "max_points": 10},
		{"text": "  что  такое  пид-регулятор  ", "difficulty": "easy", "topic": "control", "rubric": "r", "model_answer": "a", "max_points": 10}
	]`

	uploadQuestionsWithLang := func(filename string, content string, lang string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("questions_file", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("part.Write: %v", err)
		}
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/admin/questions", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		adminUser := &model.User{ID: 1, Role: model.UserRoleAdmin}
		ctx := i18n.WithLocalizer(model.ContextWithUser(req.Context(), adminUser), i18n.NewLocalizer(lang))
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		h.handleUploadQuestions(rec, req)
		return rec
	}

	uploadQuestions := func(filename string, content string) *httptest.ResponseRecorder {
		return uploadQuestionsWithLang(filename, content, "en")
	}

	// Первый импорт: 2 вопроса в файле, но 1 дубликат (EN locale)
	rec1 := uploadQuestions("questions_v1.json", jsonContent)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first upload status = %d, body: %s", rec1.Code, rec1.Body.String())
	}

	// Критерий 1: в базе должна быть только 1 запись
	count, err := s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 question in DB, got %d", count)
	}

	// Критерий 1: в ответе должно быть локализованное предупреждение о дубликате на английском
	body1 := rec1.Body.String()
	if !strings.Contains(body1, "matches question #1 in the file under topic") || !strings.Contains(body1, "Warnings:") {
		t.Errorf("expected English warning in response, got: %s", body1)
	}

	// Критерий 2: повторный импорт того же файла не меняет количество вопросов и показывает локализованное сообщение
	rec2 := uploadQuestions("questions_v1.json", jsonContent)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second upload status = %d, body: %s", rec2.Code, rec2.Body.String())
	}

	body2 := rec2.Body.String()
	if strings.Contains(body2, "UploadDuplicate") {
		t.Errorf("expected localized message, but got raw key UploadDuplicate in body: %s", body2)
	}
	if !strings.Contains(body2, "This file has already been imported.") {
		t.Errorf("expected 'This file has already been imported.' in body, got: %s", body2)
	}

	countAfterReimport, err := s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount after re-import: %v", err)
	}
	if countAfterReimport != 1 {
		t.Errorf("expected question count to remain 1 after re-import, got %d", countAfterReimport)
	}

	// Повторный импорт под другим именем файла тоже не должен плодить дубликаты
	rec3 := uploadQuestions("questions_v2_new_name.json", `[{"text": "что такое пид-регулятор", "difficulty": "easy", "topic": "control", "rubric": "r", "model_answer": "a", "max_points": 10}]`)
	if rec3.Code != http.StatusOK {
		t.Fatalf("third upload status = %d, body: %s", rec3.Code, rec3.Body.String())
	}

	countAfterNewFile, err := s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount after third upload: %v", err)
	}
	if countAfterNewFile != 1 {
		t.Errorf("expected question count to still remain 1, got %d", countAfterNewFile)
	}

	body3 := rec3.Body.String()
	if !strings.Contains(body3, "matches existing question #") {
		t.Errorf("expected English warning about existing question in DB, got: %s", body3)
	}

	// Проверяем русскую локаль (RU locale)
	recRuDuplicate := uploadQuestionsWithLang("questions_v1.json", jsonContent, "ru")
	bodyRuDuplicate := recRuDuplicate.Body.String()
	if strings.Contains(bodyRuDuplicate, "UploadDuplicate") {
		t.Errorf("expected localized Russian message, got raw UploadDuplicate: %s", bodyRuDuplicate)
	}
	if !strings.Contains(bodyRuDuplicate, "Этот файл уже был импортирован.") {
		t.Errorf("expected Russian duplicate notice, got: %s", bodyRuDuplicate)
	}

	recRuNew := uploadQuestionsWithLang("questions_ru_test.json", `[{"text": "что такое пид-регулятор", "difficulty": "easy", "topic": "control", "rubric": "r", "model_answer": "a", "max_points": 10}]`, "ru")
	bodyRuNew := recRuNew.Body.String()
	if !strings.Contains(bodyRuNew, "совпадает с существующим вопросом #") || !strings.Contains(bodyRuNew, "Предупреждения:") {
		t.Errorf("expected Russian warning about existing question in DB, got: %s", bodyRuNew)
	}

	fmt.Println("All duplicate protection criteria passed!")
}
