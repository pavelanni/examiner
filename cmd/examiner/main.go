package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/handler"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

//go:generate templ generate

func init() {
	// Define flags with shorthands where useful.
	flag.StringP("addr", "a", ":8080", "HTTP listen address")
	flag.String("db", "examiner.db", "SQLite database path")
	flag.StringSliceP("questions", "q", []string{"questions/physics_en.json"}, "Paths to questions JSON files (repeatable)")
	flag.String("llm-url", "http://localhost:11434/v1", "OpenAI-compatible API base URL")
	flag.String("llm-key", "ollama", "API key for LLM")
	flag.String("llm-model", "llama3.2", "LLM model name")
	flag.StringP("lang", "l", "en", "UI language (en, ru)")
	flag.IntP("num-questions", "n", 0, "Number of questions per exam (0 = all available)")
	flag.StringP("difficulty", "d", "", "Filter questions by difficulty (easy, medium, hard)")
	flag.StringP("topic", "t", "", "Filter questions by topic")
	flag.Int("max-followups", 3, "Maximum follow-up questions per answer")
	flag.Bool("shuffle", true, "Randomize question order")
	flag.String("base-path", "", "URL prefix for sub-path deployments (e.g. /ru)")
	flag.String("admin-password", "", "Initial admin password (or set EXAMINER_ADMIN_PASSWORD)")
	flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.String("log-format", "text", "Log format (text, json)")
}

func main() {
	flag.Parse()

	// Bind pflags to Viper.
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		fmt.Fprintf(os.Stderr, "error binding flags: %v\n", err)
		os.Exit(1)
	}

	// Environment variables: EXAMINER_ADDR, EXAMINER_LLM_URL, etc.
	viper.SetEnvPrefix("EXAMINER")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	// Set up structured logging.
	var logLevel slog.Level
	switch strings.ToLower(viper.GetString("log-level")) {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	handlerOpts := &slog.HandlerOptions{Level: logLevel}
	var logHandler slog.Handler
	switch strings.ToLower(viper.GetString("log-format")) {
	case "text":
		logHandler = slog.NewTextHandler(os.Stderr, handlerOpts)
	case "json":
		logHandler = slog.NewJSONHandler(os.Stderr, handlerOpts)
	default:
		fmt.Fprintf(os.Stderr, "invalid --log-format %q: must be \"text\" or \"json\"\n", viper.GetString("log-format"))
		os.Exit(1)
	}
	slog.SetDefault(slog.New(logHandler))

	// Config file support: examiner.yaml, examiner.toml, etc.
	// Note: do NOT call SetConfigType here — it would cause viper to
	// try parsing any examiner.* file (e.g. examiner.db) as YAML.
	viper.SetConfigName("examiner")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.config/examiner")
	viper.AddConfigPath("/etc/examiner")
	viper.AddConfigPath("/data")
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			fmt.Fprintf(os.Stderr, "error reading config file: %v\n", err)
			os.Exit(1)
		}
	} else {
		slog.Info("loaded config file", "path", viper.ConfigFileUsed())
	}

	// Open database.
	db, err := store.New(viper.GetString("db"))
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Seed default admin user if no users exist.
	if err := seedAdmin(db, viper.GetString("admin-password")); err != nil {
		slog.Error("failed to seed admin user", "error", err)
		os.Exit(1)
	}

	// Load questions from all specified files (skips already-imported files).
	if err := loadQuestions(db, viper.GetStringSlice("questions"), viper.GetInt("max-followups")); err != nil {
		slog.Error("failed to load questions", "error", err)
		os.Exit(1)
	}

	// Initialize i18n.
	lang := viper.GetString("lang")
	if err := appI18n.Init(lang); err != nil {
		slog.Error("failed to initialize i18n", "error", err)
		os.Exit(1)
	}

	// Create LLM client and verify connectivity.
	llmClient := llm.New(
		viper.GetString("llm-url"),
		viper.GetString("llm-key"),
		viper.GetString("llm-model"),
	)
	if err := llmClient.Ping(context.Background()); err != nil {
		slog.Error("LLM health check failed", "url", viper.GetString("llm-url"), "error", err)
		os.Exit(1)
	}
	slog.Info("LLM endpoint OK", "url", viper.GetString("llm-url"), "model", viper.GetString("llm-model"))

	// Normalize base path: must start with / and not end with /, or be empty.
	basePath := strings.TrimRight(viper.GetString("base-path"), "/")
	if basePath != "" && !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}

	// Build exam config.
	examCfg := model.ExamConfig{
		NumQuestions: viper.GetInt("num-questions"),
		Difficulty:   viper.GetString("difficulty"),
		Topic:        viper.GetString("topic"),
		MaxFollowups: viper.GetInt("max-followups"),
		Shuffle:      viper.GetBool("shuffle"),
		BasePath:     basePath,
	}

	// Create handler.
	h, err := handler.New(db, llmClient, examCfg)
	if err != nil {
		slog.Error("failed to create handler", "error", err)
		os.Exit(1)
	}

	// Set up router.
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(appI18n.Middleware(lang))

	if basePath != "" {
		r.Route(basePath, func(sub chi.Router) {
			sub.Use(h.BasePathMiddleware)
			h.Routes(sub)
		})
		// Redirect bare base path without trailing slash.
		r.Get(basePath, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, basePath+"/", http.StatusMovedPermanently)
		})
	} else {
		r.Use(h.BasePathMiddleware)
		h.Routes(r)
	}

	addr := viper.GetString("addr")
	slog.Info("starting server",
		"addr", addr,
		"model", viper.GetString("llm-model"),
		"llm_url", viper.GetString("llm-url"),
		"lang", lang,
		"num_questions", examCfg.NumQuestions,
		"difficulty", examCfg.Difficulty,
		"topic", examCfg.Topic,
		"max_followups", examCfg.MaxFollowups,
		"shuffle", examCfg.Shuffle,
		"base_path", basePath,
	)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func loadQuestions(db *store.Store, paths []string, maxFollowups int) error {
	// Ensure a default blueprint exists.
	count, err := db.QuestionCount()
	if err != nil {
		return err
	}
	if count == 0 {
		_, err = db.CreateBlueprint(model.ExamBlueprint{
			CourseID:     1,
			Name:         "Exam",
			TimeLimit:    0,
			MaxFollowups: maxFollowups,
		})
		if err != nil {
			return err
		}
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		hash := sha256sum(data)
		storedHash, err := db.GetImportedFileHash(path)
		if err != nil {
			return fmt.Errorf("check import status for %s: %w", path, err)
		}

		if storedHash == hash {
			slog.Info("questions file unchanged, skipping", "path", path)
			continue
		}
		if storedHash != "" {
			slog.Warn("questions file changed since last import, skipping to avoid breaking existing sessions",
				"path", path)
			continue
		}

		var questions []model.QuestionImport
		if err := json.Unmarshal(data, &questions); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}

		for _, qi := range questions {
			_, err := db.InsertQuestion(model.Question{
				CourseID:    1,
				Text:        qi.Text,
				Difficulty:  qi.Difficulty,
				Topic:       qi.Topic,
				Rubric:      qi.Rubric,
				ModelAnswer: qi.ModelAnswer,
				MaxPoints:   qi.MaxPoints,
			})
			if err != nil {
				return fmt.Errorf("insert question from %s: %w", path, err)
			}
		}

		if err := db.SetImportedFileHash(path, hash); err != nil {
			return fmt.Errorf("record import for %s: %w", path, err)
		}
		slog.Info("imported questions", "path", path, "count", len(questions))
	}

	return nil
}

func sha256sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func seedAdmin(db *store.Store, password string) error {
	count, err := db.UserCount()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	if password == "" {
		return fmt.Errorf("admin password is required: set --admin-password flag or EXAMINER_ADMIN_PASSWORD env var")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	_, err = db.CreateUser(model.User{
		Username:     "admin",
		DisplayName:  "Administrator",
		PasswordHash: string(hash),
		Role:         model.UserRoleAdmin,
		Active:       true,
	})
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	slog.Info("seeded default admin user", "username", "admin")
	return nil
}
