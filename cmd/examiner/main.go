package main

import (
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
	flag.StringP("questions", "q", "questions.json", "Path to questions JSON file")
	flag.String("llm-url", "http://localhost:11434/v1", "OpenAI-compatible API base URL")
	flag.String("llm-key", "ollama", "API key for LLM")
	flag.String("llm-model", "llama3.2", "LLM model name")
	flag.StringP("lang", "l", "en", "UI language (en, ru)")
	flag.IntP("num-questions", "n", 0, "Number of questions per exam (0 = all available)")
	flag.StringP("difficulty", "d", "", "Filter questions by difficulty (easy, medium, hard)")
	flag.StringP("topic", "t", "", "Filter questions by topic")
	flag.Int("max-followups", 3, "Maximum follow-up questions per answer")
	flag.Bool("shuffle", false, "Randomize question order")
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

	// Config file support: examiner.yaml, examiner.toml, etc.
	viper.SetConfigName("examiner")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.config/examiner")
	viper.AddConfigPath("/etc/examiner")
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			fmt.Fprintf(os.Stderr, "error reading config file: %v\n", err)
			os.Exit(1)
		}
	} else {
		slog.Info("loaded config file", "path", viper.ConfigFileUsed())
	}

	// Set up structured logging.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	// Open database.
	db, err := store.New(viper.GetString("db"))
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Load questions if the database is empty.
	if err := loadQuestions(db, viper.GetString("questions"), viper.GetInt("max-followups")); err != nil {
		slog.Error("failed to load questions", "error", err)
		os.Exit(1)
	}

	// Initialize i18n.
	lang := viper.GetString("lang")
	if err := appI18n.Init(lang); err != nil {
		slog.Error("failed to initialize i18n", "error", err)
		os.Exit(1)
	}

	// Create LLM client.
	llmClient := llm.New(
		viper.GetString("llm-url"),
		viper.GetString("llm-key"),
		viper.GetString("llm-model"),
	)

	// Build exam config.
	examCfg := model.ExamConfig{
		NumQuestions: viper.GetInt("num-questions"),
		Difficulty:   viper.GetString("difficulty"),
		Topic:        viper.GetString("topic"),
		MaxFollowups: viper.GetInt("max-followups"),
		Shuffle:      viper.GetBool("shuffle"),
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
	h.Routes(r)

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
	)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func loadQuestions(db *store.Store, path string, maxFollowups int) error {
	count, err := db.QuestionCount()
	if err != nil {
		return err
	}
	if count > 0 {
		slog.Info("questions already loaded", "count", count)
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var questions []model.QuestionImport
	if err := json.Unmarshal(data, &questions); err != nil {
		return err
	}

	// Create a default blueprint.
	_, err = db.CreateBlueprint(model.ExamBlueprint{
		CourseID:     1,
		Name:         "Exam",
		TimeLimit:    0,
		MaxFollowups: maxFollowups,
	})
	if err != nil {
		return err
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
			return err
		}
	}

	slog.Info("loaded questions", "count", len(questions))
	return nil
}
