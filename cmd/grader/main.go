package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/grader/handler"
	"github.com/pavelanni/examiner/internal/grader/store"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/userutil"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "grader",
		Short: "Teacher grading app for reviewing exam results",
	}

	v := viper.New()
	v.SetEnvPrefix("GRADER")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	pf := root.PersistentFlags()
	pf.String("db", "grader.db", "SQLite database path")
	pf.String("log-level", "info", "Log level (debug, info, warn, error)")
	pf.String("log-format", "text", "Log format (text, json)")

	_ = v.BindPFlag("db", pf.Lookup("db"))
	_ = v.BindPFlag("log-level", pf.Lookup("log-level"))
	_ = v.BindPFlag("log-format", pf.Lookup("log-format"))

	serve := serveCmd(v)
	imp := importCmd(v)
	impUsers := importUsersCmd(v)
	root.AddCommand(serve, imp, impUsers)

	// Make "serve" the default when no subcommand is given.
	root.RunE = serve.RunE
	root.Flags().AddFlagSet(serve.Flags())

	return root
}

func setupLogging(v *viper.Viper) {
	var level slog.Level
	switch strings.ToLower(v.GetString("log-level")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	switch strings.ToLower(v.GetString("log-format")) {
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	default:
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

func serveCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the grader HTTP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Bind serve-specific flags.
			_ = v.BindPFlag("port", cmd.Flags().Lookup("port"))
			_ = v.BindPFlag("admin-password", cmd.Flags().Lookup("admin-password"))
			_ = v.BindPFlag("secure-cookies", cmd.Flags().Lookup("secure-cookies"))

			setupLogging(v)

			dbPath := v.GetString("db")
			s, err := store.New(dbPath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer s.Close()

			// Seed admin user if no users exist.
			count, err := s.UserCount()
			if err != nil {
				return fmt.Errorf("check user count: %w", err)
			}
			if count == 0 {
				password := v.GetString("admin-password")
				if password == "" {
					slog.Error("admin password is required when no users exist: set --admin-password or GRADER_ADMIN_PASSWORD")
					os.Exit(1)
				}
				hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
				if err != nil {
					return fmt.Errorf("hash admin password: %w", err)
				}
				if _, err := s.CreateUser(model.User{
					Username:     "admin",
					DisplayName:  "Administrator",
					PasswordHash: string(hash),
					Role:         model.UserRoleAdmin,
					Active:       true,
				}); err != nil {
					return fmt.Errorf("create admin user: %w", err)
				}
				slog.Info("seeded default admin user", "username", "admin")
			}

			port := v.GetInt("port")
			secureCookies := v.GetBool("secure-cookies")

			h := handler.New(s, secureCookies)
			addr := fmt.Sprintf(":%d", port)

			slog.Info("starting grader server", "addr", addr, "db", dbPath, "secure_cookies", secureCookies)
			return http.ListenAndServe(addr, h.Routes())
		},
	}

	f := cmd.Flags()
	f.Int("port", 8082, "HTTP listen port")
	f.String("admin-password", "", "Initial admin password (or set GRADER_ADMIN_PASSWORD)")
	f.Bool("secure-cookies", true, "Set Secure flag on session cookies")

	return cmd
}

func importCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import [files...]",
		Short: "Import exam result JSON files into the grader database",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			setupLogging(v)

			dbPath := v.GetString("db")
			s, err := store.New(dbPath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer s.Close()

			for _, path := range args {
				data, err := os.ReadFile(path)
				if err != nil {
					slog.Error("failed to read file", "path", path, "error", err)
					continue
				}

				var export model.ExamExport
				if err := json.Unmarshal(data, &export); err != nil {
					slog.Error("failed to parse file", "path", path, "error", err)
					continue
				}

				if err := s.ImportExam(export); err != nil {
					slog.Error("failed to import exam", "path", path, "error", err)
					continue
				}

				slog.Info("imported exam", "path", path, "exam_id", export.ExamID, "students", len(export.Results))
			}

			return nil
		},
	}

	return cmd
}

func importUsersCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import-users [file.csv]",
		Short: "Import teacher accounts from a CSV file (columns: user_id, display_name)",
		Long: `Import teacher accounts from a CSV file. Usernames and passwords
are generated automatically. A credentials CSV is written to
<input>-creds.csv with the generated login details.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			setupLogging(v)

			dbPath := v.GetString("db")
			s, err := store.New(dbPath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer s.Close()

			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open CSV: %w", err)
			}
			defer f.Close()

			creds, err := userutil.ImportCSV(f, s, userutil.ImportConfig{
				Role:           model.UserRoleTeacher,
				PasswordPrefix: "teach",
			})
			if err != nil {
				return fmt.Errorf("import users: %w", err)
			}

			slog.Info("imported users", "path", args[0], "count", len(creds))

			// Write credentials CSV.
			credsPath := strings.TrimSuffix(args[0], ".csv") + "-creds.csv"
			cf, err := os.Create(credsPath)
			if err != nil {
				return fmt.Errorf("create credentials file: %w", err)
			}
			defer cf.Close()

			if err := userutil.WriteCredentialsCSV(cf, creds); err != nil {
				return fmt.Errorf("write credentials: %w", err)
			}

			slog.Info("credentials written", "path", credsPath)
			return nil
		},
	}

	return cmd
}
