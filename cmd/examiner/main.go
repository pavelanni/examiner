package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/handler"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/llm/prompts"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

//go:generate templ generate

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "examiner",
		Short: "Oral exam simulator powered by LLMs",
	}

	serve := serveCmd()
	root.AddCommand(serve, exportCmd())

	// Make "serve" the default when no subcommand is given.
	root.RunE = serve.RunE

	// Register serve flags on root so bare `examiner --addr ...` still works.
	root.Flags().AddFlagSet(serve.Flags())

	return root
}

func serveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP exam server",
		RunE:  runServe,
	}
	f := cmd.Flags()
	f.StringP("addr", "a", ":8080", "HTTP listen address")
	f.String("db", "examiner.db", "SQLite database path")
	f.StringSliceP("questions", "q", []string{"questions/physics_en.json"}, "Paths to questions JSON files (repeatable)")
	f.String("llm-url", "http://localhost:11434/v1", "OpenAI-compatible API base URL")
	f.String("llm-key", "ollama", "API key for LLM")
	f.String("llm-model", "llama3.2", "LLM model name")
	f.StringP("lang", "l", "en", "UI language (en, ru)")
	f.IntP("num-questions", "n", 0, "Number of questions per exam (0 = all available)")
	f.StringP("difficulty", "d", "", "Filter questions by difficulty (easy, medium, hard)")
	f.StringP("topic", "t", "", "Filter questions by topic")
	f.Int("max-followups", 3, "Maximum follow-up questions per answer")
	f.Bool("shuffle", true, "Randomize question order")
	f.String("base-path", "", "URL prefix for sub-path deployments (e.g. /ru)")
	f.Bool("secure-cookies", true, "Set Secure flag on session cookies")
	f.String("prompt-variant", string(prompts.PromptStandard), "Grading prompt variant (strict, standard, lenient)")
	f.String("admin-password", "", "Initial admin password (or set EXAMINER_ADMIN_PASSWORD)")
	f.String("log-level", "info", "Log level (debug, info, warn, error)")
	f.String("log-format", "text", "Log format (text, json)")
	return cmd
}

func exportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export exam results as JSON",
		RunE:  runExport,
	}
	f := cmd.Flags()
	f.String("db", "examiner.db", "SQLite database path")
	f.String("exam-id", "", "Exam identifier for output (required)")
	f.String("subject", "", "Subject name for output (required)")
	f.String("date", "", "Exam date in YYYY-MM-DD format (required)")
	f.String("prompt-variant", "standard", "Prompt variant included in export metadata")
	f.StringP("output", "o", "-", "Output file path (- for stdout)")
	f.String("log-level", "info", "Log level (debug, info, warn, error)")
	f.String("log-format", "text", "Log format (text, json)")

	_ = cmd.MarkFlagRequired("exam-id")
	_ = cmd.MarkFlagRequired("subject")
	_ = cmd.MarkFlagRequired("date")

	return cmd
}

func setupLogging(cmd *cobra.Command) {
	v := viperForCmd(cmd)

	var logLevel slog.Level
	switch strings.ToLower(v.GetString("log-level")) {
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
	switch strings.ToLower(v.GetString("log-format")) {
	case "json":
		logHandler = slog.NewJSONHandler(os.Stderr, handlerOpts)
	default:
		logHandler = slog.NewTextHandler(os.Stderr, handlerOpts)
	}
	slog.SetDefault(slog.New(logHandler))
}

// viperForCmd binds a command's flags and environment to a fresh viper instance.
func viperForCmd(cmd *cobra.Command) *viper.Viper {
	v := viper.New()
	_ = v.BindPFlags(cmd.Flags())

	v.SetEnvPrefix("EXAMINER")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	v.SetConfigName("examiner")
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.config/examiner")
	v.AddConfigPath("/etc/examiner")
	v.AddConfigPath("/data")
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			slog.Warn("error reading config file", "error", err)
		}
	} else {
		slog.Info("loaded config file", "path", v.ConfigFileUsed())
	}

	return v
}

func runServe(cmd *cobra.Command, _ []string) error {
	setupLogging(cmd)
	v := viperForCmd(cmd)

	// Open database.
	db, err := store.New(v.GetString("db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Seed default admin user if no users exist.
	if err := seedAdmin(db, v.GetString("admin-password")); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}

	// Load questions from all specified files.
	if err := loadQuestions(db, v.GetStringSlice("questions"), v.GetInt("max-followups")); err != nil {
		return fmt.Errorf("load questions: %w", err)
	}

	// Initialize i18n.
	lang := v.GetString("lang")
	if err := appI18n.Init(lang); err != nil {
		return fmt.Errorf("init i18n: %w", err)
	}

	// Create LLM client.
	promptVariant := strings.ToLower(strings.TrimSpace(v.GetString("prompt-variant")))
	if !prompts.IsValidVariant(promptVariant) {
		slog.Warn("invalid prompt-variant, using standard", "variant", promptVariant)
		promptVariant = string(prompts.PromptStandard)
	}
	llmClient, err := llm.New(
		v.GetString("llm-url"),
		v.GetString("llm-key"),
		v.GetString("llm-model"),
		promptVariant,
	)
	if err != nil {
		return fmt.Errorf("create LLM client: %w", err)
	}
	if err := llmClient.Ping(context.Background()); err != nil {
		return fmt.Errorf("LLM health check: %w", err)
	}
	slog.Info("LLM endpoint OK", "url", v.GetString("llm-url"), "model", v.GetString("llm-model"))

	// Normalize base path.
	basePath := strings.TrimRight(v.GetString("base-path"), "/")
	if basePath != "" && !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}

	examCfg := model.ExamConfig{
		NumQuestions:  v.GetInt("num-questions"),
		Difficulty:    v.GetString("difficulty"),
		Topic:         v.GetString("topic"),
		MaxFollowups:  v.GetInt("max-followups"),
		Shuffle:       v.GetBool("shuffle"),
		BasePath:      v.GetString("base-path"),
		SecureCookies: v.GetBool("secure-cookies"),
		PromptVariant: promptVariant,
	}

	h, err := handler.New(db, llmClient, examCfg)
	if err != nil {
		return fmt.Errorf("create handler: %w", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(appI18n.Middleware(lang))

	if basePath != "" {
		r.Route(basePath, func(sub chi.Router) {
			sub.Use(h.BasePathMiddleware)
			h.Routes(sub)
		})
		r.Get(basePath, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, basePath+"/", http.StatusMovedPermanently)
		})
	} else {
		r.Use(h.BasePathMiddleware)
		h.Routes(r)
	}

	addr := v.GetString("addr")
	slog.Info("starting server",
		"addr", addr,
		"model", v.GetString("llm-model"),
		"llm_url", v.GetString("llm-url"),
		"lang", lang,
		"num_questions", examCfg.NumQuestions,
		"difficulty", examCfg.Difficulty,
		"topic", examCfg.Topic,
		"max_followups", examCfg.MaxFollowups,
		"shuffle", examCfg.Shuffle,
		"base_path", basePath,
	)
	return http.ListenAndServe(addr, r)
}

func runExport(cmd *cobra.Command, _ []string) error {
	setupLogging(cmd)
	v := viperForCmd(cmd)

	db, err := store.New(v.GetString("db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	results, err := db.ExportAllSessions()
	if err != nil {
		return fmt.Errorf("export sessions: %w", err)
	}

	// Determine num_questions from the first result (all sessions share the same blueprint).
	numQuestions := 0
	if len(results) > 0 {
		numQuestions = len(results[0].Questions)
	}

	export := model.ExamExport{
		ExamID:        v.GetString("exam-id"),
		Subject:       v.GetString("subject"),
		Date:          v.GetString("date"),
		PromptVariant: v.GetString("prompt-variant"),
		NumQuestions:  numQuestions,
		Results:       results,
	}

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	outPath := v.GetString("output")
	var w io.Writer
	if outPath == "" || outPath == "-" {
		w = os.Stdout
	} else {
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	_, err = w.Write(data)
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	// Ensure trailing newline.
	_, _ = fmt.Fprintln(w)

	return nil
}

func loadQuestions(db *store.Store, paths []string, maxFollowups int) error {
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
