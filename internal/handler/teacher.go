package handler

import (
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/pavelanni/examiner/internal/model"
    "github.com/pavelanni/examiner/internal/handler/views"
    jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
    "github.com/go-chi/chi/v5"
)

func (h *Handler) handleTeacherProfile(w http.ResponseWriter, r *http.Request) {
    user := model.UserFromContext(r.Context())

    files := []string{}
    entries, err := os.ReadDir("questions")
    if err == nil {
        for _, e := range entries {
            if e.IsDir() {
                continue
            }
            name := e.Name()
            if filepath.Ext(name) == ".json" {
                files = append(files, name)
            }
        }
    }

    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    if err := views.TeacherProfilePage(user.DisplayName, files).Render(r.Context(), w); err != nil {
        slog.Error("render error", "error", err)
    }
}

func (h *Handler) handleTeacherCreateTest(w http.ResponseWriter, r *http.Request) {
    user := model.UserFromContext(r.Context())

    filename := r.URL.Query().Get("file")
    var existingQuestions []model.QuestionImport

    if filename != "" {
        baseName := filepath.Base(filename)
        filePath := filepath.Join("questions", baseName)

        fileBytes, err := os.ReadFile(filePath)
        if err == nil {
            if errUnmarshal := json.Unmarshal(fileBytes, &existingQuestions); errUnmarshal != nil {
                var wrapper struct {
                    Questions []model.QuestionImport `json:"questions"`
                }
                if errWrap := json.Unmarshal(fileBytes, &wrapper); errWrap == nil {
                    existingQuestions = wrapper.Questions
                }
            }
        } else {
            slog.Warn("failed to read file for editing", "file", filePath, "error", err)
        }
    }

    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    
    component := views.TeacherCreateTestPage(user.DisplayName, model.CSRFTokenFromContext(r.Context()), existingQuestions, filename)
    if err := component.Render(r.Context(), w); err != nil {
        slog.Error("render error", "error", err)
    }
}

func (h *Handler) handleTeacherMe(w http.ResponseWriter, r *http.Request) {
    user := model.UserFromContext(r.Context())
    if user == nil {
        http.Error(w, "unauthorized", http.StatusUnauthorized)
        return
    }
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "id":          user.ID,
        "username":    user.Username,
        "display_name": user.DisplayName,
        "role":        user.Role,
        "active":      user.Active,
    })
}

func (h *Handler) handleTeacherUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "file too large", http.StatusBadRequest)
		return
	}

	var data []byte
	var filename string

	file, header, err := r.FormFile("questions_file")
	if err == nil {
		defer file.Close()
		b, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "failed to read file", http.StatusInternalServerError)
			return
		}
		data = b
		filename = header.Filename
	} else {
		txt := r.FormValue("questions_json")
		if txt == "" {
			http.Error(w, "no input provided", http.StatusBadRequest)
			return
		}
		data = []byte(txt)
		filename = fmt.Sprintf("teacher_%d.json", time.Now().Unix())
	}

	// Fail closed: Schema validation is mandatory
	absSchema, err := filepath.Abs("schema/question_schema.json")
	if err != nil {
		slog.Error("failed to get absolute path for schema", "error", err)
		http.Error(w, "internal server configuration error", http.StatusInternalServerError)
		return
	}

	compiler := jsonschema.NewCompiler()
	f, err := os.Open(absSchema)
	if err != nil {
		slog.Error("failed to open schema file", "error", err)
		http.Error(w, "internal server configuration error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	schemaURL := "file://" + filepath.ToSlash(absSchema)
	if err := compiler.AddResource(schemaURL, f); err != nil {
		slog.Error("failed to add schema resource", "error", err)
		http.Error(w, "internal server configuration error", http.StatusInternalServerError)
		return
	}

	sch, err := compiler.Compile(schemaURL)
	if err != nil {
		slog.Error("failed to compile schema", "error", err)
		http.Error(w, "internal server configuration error", http.StatusInternalServerError)
		return
	}

	// Validate the JSON structure against the compiled schema
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := sch.Validate(v); err != nil {
		http.Error(w, "schema validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	var questions []model.QuestionImport
	if err := json.Unmarshal(data, &questions); err != nil {
		var wrapper struct {
			TestName        string                 `json:"test_name"`
			TestDescription string                 `json:"test_description"`
			DefaultTopic    string                 `json:"default_topic"`
			Questions       []model.QuestionImport `json:"questions"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		questions = wrapper.Questions
	}
	if len(questions) == 0 {
		http.Error(w, "no questions found in JSON", http.StatusBadRequest)
		return
	}

	user := model.UserFromContext(r.Context())
	editingFile := r.FormValue("editing_file")
	customFilename := strings.TrimSpace(r.FormValue("custom_filename"))

	var safeName string
	var outPath string

	if editingFile != "" {
		safeName = filepath.Base(editingFile)
		if customFilename != "" {
			base := filepath.Base(customFilename)
			base = strings.TrimSuffix(base, ".json")
			safeName = base + ".json"
		}
		outPath = filepath.Join("questions", safeName)
		filename = safeName
	} else {
		if customFilename != "" {
			base := filepath.Base(customFilename)
			base = strings.TrimSuffix(base, ".json")
			safeName = base + ".json"
		} else {
			safeName = fmt.Sprintf("teacher_%s_%d.json", user.Username, time.Now().Unix())
		}
		outPath = filepath.Join("questions", safeName)
	}

	if err := os.WriteFile(outPath, data, 0644); err != nil {
		slog.Error("failed to save questions file", "error", err)
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}

	for _, qi := range questions {
		_, err := h.store.InsertQuestion(model.Question{
			CourseID:    1,
			Text:        qi.Text,
			Difficulty:  qi.Difficulty,
			Topic:       qi.Topic,
			Rubric:      qi.Rubric,
			ModelAnswer: qi.ModelAnswer,
			MaxPoints:   qi.MaxPoints,
		})
		if err != nil {
			slog.Error("failed to insert question", "error", err)
			http.Error(w, "failed to insert question: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if err := h.store.SetImportedFileHash(filename, hash); err != nil {
		slog.Warn("failed to set import hash", "error", err)
	}

	slog.Info("teacher uploaded questions", "user", user.Username, "file", outPath, "count", len(questions))

	// Automatically redirect to teacher's profile
	http.Redirect(w, r, "/teacher/profile", http.StatusSeeOther)
}

func (h *Handler) handleTeacherDownload(w http.ResponseWriter, r *http.Request) {
    name := chi.URLParam(r, "name")
    if name == "" {
        http.Error(w, "missing name", http.StatusBadRequest)
        return
    }
    if filepath.Base(name) != name {
        http.Error(w, "invalid filename", http.StatusBadRequest)
        return
    }
    path := filepath.Join("questions", name)
    http.ServeFile(w, r, path)
}