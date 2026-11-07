package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/pavelanni/examiner/internal/store"
)

// reactivateAdminCmd восстанавливает активность администратора локальной базы.
// Пароль сохраняется; запуск HTTP-сервера и подключение к LLM не требуются.
func reactivateAdminCmd() *cobra.Command {
	var dbPath, username string
	cmd := &cobra.Command{
		Use:   "reactivate-admin",
		Short: "Reactivate an existing administrator without changing their password",
		Long: "Reactivate an existing administrator in a local database. Stop the server and back up the database first. " +
			"Uses --db directly; configuration files and environment variables do not select the database for this command.",
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			info, err := os.Stat(dbPath)
			if err != nil {
				return fmt.Errorf("open existing database: %w", err)
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return fmt.Errorf("database must be an existing non-empty regular file: %s", dbPath)
			}
			if username == "" {
				return fmt.Errorf("administrator username is required")
			}
			s, err := store.New(dbPath)
			if err != nil {
				return err
			}
			defer func() { runErr = errors.Join(runErr, s.Close()) }()
			if err := s.ReactivateAdmin(username); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Administrator %q is active. Password unchanged.\n", username)
			return err
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "examiner.db", "Existing SQLite database path")
	cmd.Flags().StringVar(&username, "username", "admin", "Existing administrator username")
	return cmd
}
