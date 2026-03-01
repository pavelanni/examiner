package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
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
	root.AddCommand(serve, exportCmd(), prepCmd())

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
	f.String("exam-id", "", "Exam identifier (read from DB if omitted)")
	f.String("subject", "", "Subject name (read from DB if omitted)")
	f.String("date", "", "Exam date in YYYY-MM-DD format (read from DB if omitted)")
	f.String("prompt-variant", "", "Prompt variant (read from DB if omitted)")
	f.StringP("output", "o", "-", "Output file path (- for stdout)")
	f.String("log-level", "info", "Log level (debug, info, warn, error)")
	f.String("log-format", "text", "Log format (text, json)")

	return cmd
}

func prepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prep",
		Short: "Prepare exam database from manifest and roster",
		RunE:  runPrep,
	}
	f := cmd.Flags()
	f.StringP("manifest", "m", "", "Path to manifest YAML (required)")
	f.StringP("output-dir", "o", ".", "Directory for output files")
	f.String("log-level", "info", "Log level (debug, info, warn, error)")
	f.String("log-format", "text", "Log format (text, json)")

	_ = cmd.MarkFlagRequired("manifest")

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

	// Read metadata from DB as defaults; CLI flags override.
	info, err := db.GetExamInfo()
	if err != nil {
		return fmt.Errorf("read exam metadata: %w", err)
	}

	examID := v.GetString("exam-id")
	if examID == "" {
		examID = info.ExamID
	}
	subject := v.GetString("subject")
	if subject == "" {
		subject = info.Subject
	}
	date := v.GetString("date")
	if date == "" {
		date = info.Date
	}
	promptVariant := v.GetString("prompt-variant")
	if promptVariant == "" {
		promptVariant = info.PromptVariant
	}
	if promptVariant == "" {
		promptVariant = "standard"
	}

	// Validate required fields after merging DB + flags.
	if examID == "" {
		return fmt.Errorf("exam-id is required (set via --exam-id flag or store metadata)")
	}
	if subject == "" {
		return fmt.Errorf("subject is required (set via --subject flag or store metadata)")
	}
	if date == "" {
		return fmt.Errorf("date is required (set via --date flag or store metadata)")
	}

	results, err := db.ExportAllSessions()
	if err != nil {
		return fmt.Errorf("export sessions: %w", err)
	}

	// Use DB metadata for num_questions; fall back to first result.
	numQuestions := info.NumQuestions
	if numQuestions == 0 && len(results) > 0 {
		numQuestions = len(results[0].Questions)
	}

	export := model.ExamExport{
		ExamID:        examID,
		Subject:       subject,
		Date:          date,
		PromptVariant: promptVariant,
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

func runPrep(cmd *cobra.Command, _ []string) error {
	setupLogging(cmd)
	v := viperForCmd(cmd)

	manifestPath := v.GetString("manifest")
	outputDir := v.GetString("output-dir")

	// Parse manifest YAML.
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest model.ExamManifest
	if err := yaml.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	// Validate manifest fields.
	if manifest.ExamID == "" {
		return fmt.Errorf("manifest: exam_id is required")
	}
	if manifest.Subject == "" {
		return fmt.Errorf("manifest: subject is required")
	}
	if manifest.Date == "" {
		return fmt.Errorf("manifest: date is required")
	}
	if _, err := time.Parse("2006-01-02", manifest.Date); err != nil {
		return fmt.Errorf("manifest: date must be YYYY-MM-DD: %w", err)
	}
	if manifest.PromptVariant == "" {
		manifest.PromptVariant = string(prompts.PromptStandard)
	}
	if !prompts.IsValidVariant(manifest.PromptVariant) {
		return fmt.Errorf("manifest: invalid prompt_variant %q", manifest.PromptVariant)
	}
	if manifest.Questions == "" {
		return fmt.Errorf("manifest: questions file path is required")
	}
	if manifest.Roster == "" {
		return fmt.Errorf("manifest: roster file path is required")
	}

	// Resolve questions path relative to manifest directory.
	manifestDir := filepath.Dir(manifestPath)
	questionsPath := manifest.Questions
	if !filepath.IsAbs(questionsPath) {
		questionsPath = filepath.Join(manifestDir, questionsPath)
	}
	if _, err := os.Stat(questionsPath); err != nil {
		return fmt.Errorf("questions file: %w", err)
	}

	// Resolve roster path relative to manifest directory.
	rosterPath := manifest.Roster
	if !filepath.IsAbs(rosterPath) {
		rosterPath = filepath.Join(manifestDir, rosterPath)
	}

	// Parse roster CSV.
	rosterFile, err := os.Open(rosterPath)
	if err != nil {
		return fmt.Errorf("open roster: %w", err)
	}
	defer rosterFile.Close()

	reader := csv.NewReader(rosterFile)
	rosterRecords, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("parse roster CSV: %w", err)
	}
	if len(rosterRecords) < 2 {
		return fmt.Errorf("roster CSV must have a header row and at least one student")
	}

	// Find column indices.
	header := rosterRecords[0]
	idCol, nameCol := -1, -1
	for i, h := range header {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "student_id":
			idCol = i
		case "display_name":
			nameCol = i
		}
	}
	if idCol < 0 {
		return fmt.Errorf("roster CSV: missing student_id column")
	}
	if nameCol < 0 {
		return fmt.Errorf("roster CSV: missing display_name column")
	}

	// Create database.
	dbPath := filepath.Join(outputDir, manifest.ExamID+".db")
	db, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	defer db.Close()

	// Store exam metadata.
	if err := db.SetExamInfo(model.ExamInfo{
		ExamID:        manifest.ExamID,
		Subject:       manifest.Subject,
		Date:          manifest.Date,
		PromptVariant: manifest.PromptVariant,
		NumQuestions:  manifest.NumQuestions,
	}); err != nil {
		return fmt.Errorf("store exam metadata: %w", err)
	}

	// Create admin user with random password.
	adminPassword, err := randomPassword("admin", 8)
	if err != nil {
		return fmt.Errorf("generate admin password: %w", err)
	}
	adminHash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	_, err = db.CreateUser(model.User{
		Username:     "admin",
		DisplayName:  "Administrator",
		PasswordHash: string(adminHash),
		Role:         model.UserRoleAdmin,
		Active:       true,
	})
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	// Load questions.
	maxFollowups := manifest.MaxFollowups
	if maxFollowups == 0 {
		maxFollowups = 3
	}
	if err := loadQuestions(db, []string{questionsPath}, maxFollowups); err != nil {
		return fmt.Errorf("load questions: %w", err)
	}

	// Build credentials list (admin first).
	type credential struct {
		studentID   string
		displayName string
		username    string
		password    string
	}
	creds := []credential{
		{studentID: "", displayName: "Administrator", username: "admin", password: adminPassword},
	}

	// Subject prefix for passwords (first 4 lowercase chars).
	prefix := strings.ToLower(manifest.Subject)
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}

	// Create student users.
	usedUsernames := map[string]bool{"admin": true}
	for _, row := range rosterRecords[1:] {
		studentID := strings.TrimSpace(row[idCol])
		displayName := strings.TrimSpace(row[nameCol])
		if studentID == "" {
			continue
		}

		// Username: first letter of first name + last name, truncated to 8 chars.
		// Duplicates get last char replaced with 2, 3, etc.
		username := deduplicateUsername(usernameFromDisplayName(displayName), usedUsernames)
		usedUsernames[username] = true

		password, err := randomPassword(prefix, 5)
		if err != nil {
			return fmt.Errorf("generate password for %s: %w", studentID, err)
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password for %s: %w", studentID, err)
		}

		_, err = db.CreateUser(model.User{
			Username:     username,
			ExternalID:   studentID,
			DisplayName:  displayName,
			PasswordHash: string(hash),
			Role:         model.UserRoleStudent,
			Active:       true,
		})
		if err != nil {
			return fmt.Errorf("create user %s: %w", studentID, err)
		}

		creds = append(creds, credential{
			studentID:   studentID,
			displayName: displayName,
			username:    username,
			password:    password,
		})
	}

	// Write credentials CSV.
	credsPath := filepath.Join(outputDir, manifest.ExamID+"-creds.csv")
	credsFile, err := os.Create(credsPath)
	if err != nil {
		return fmt.Errorf("create credentials file: %w", err)
	}
	defer credsFile.Close()

	csvWriter := csv.NewWriter(credsFile)
	_ = csvWriter.Write([]string{"student_id", "display_name", "username", "password"})
	for _, c := range creds {
		_ = csvWriter.Write([]string{c.studentID, c.displayName, c.username, c.password})
	}
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		return fmt.Errorf("write credentials CSV: %w", err)
	}

	slog.Info("exam prepared",
		"db", dbPath,
		"credentials", credsPath,
		"students", len(creds)-1,
	)
	fmt.Printf("Database:    %s\n", dbPath)
	fmt.Printf("Credentials: %s\n", credsPath)

	return nil
}

// randomPassword generates a password like "prefix-XXXXX" with random alphanumeric chars.
func randomPassword(prefix string, length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return prefix + "-" + string(b), nil
}

// usernameFromDisplayName builds a username from "First Last" as first letter
// of the first name + last name, lowercased and truncated to 8 characters.
// For example, "Alice Johnson" becomes "ajohnson", "Bob Smith" becomes "bsmith".
func usernameFromDisplayName(displayName string) string {
	parts := strings.Fields(displayName)
	if len(parts) == 0 {
		return "user"
	}
	first := strings.ToLower(parts[0])
	if len(parts) == 1 {
		if len(first) > 8 {
			return first[:8]
		}
		return first
	}
	last := strings.ToLower(parts[len(parts)-1])
	username := string(first[0]) + last
	if len(username) > 8 {
		username = username[:8]
	}
	return username
}

// deduplicateUsername ensures uniqueness by replacing the last character with
// an incrementing digit (2, 3, ...) when a collision is found.
func deduplicateUsername(base string, used map[string]bool) string {
	if !used[base] {
		return base
	}
	for n := 2; n <= 99; n++ {
		suffix := fmt.Sprintf("%d", n)
		candidate := base[:len(base)-len(suffix)] + suffix
		if !used[candidate] {
			return candidate
		}
	}
	return base
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
