package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/pavelanni/examiner/internal/backup"
)

func backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the database and configuration files, keeping the newest N",
		Long: `Takes a consistent snapshot of the database (safe while the server is
running), checks it with PRAGMA integrity_check and writes it compressed as
<name>-YYYYmmdd-HHMMSS.db.gz (UTC) to --out-dir. Files given with --config
are archived next to it as <name>-YYYYmmdd-HHMMSS.config.tar.gz; *.env
files are never archived. Exits non-zero if any step fails.`,
		RunE:         runBackup,
		SilenceUsage: true,
	}
	f := cmd.Flags()
	f.String("db", "examiner.db", "SQLite database path")
	f.String("out-dir", "backups", "Directory for backups")
	f.String("name", "examiner", "Backup file name prefix, e.g. examiner-ru")
	f.Int("keep", 14, "Number of newest backups to keep (0 = keep all)")
	f.StringSlice("config", nil, "Configuration file to archive with the backup (repeatable)")
	f.String("log-level", "info", "Log level (debug, info, warn, error)")
	f.String("log-format", "text", "Log format (text, json)")
	return cmd
}

func runBackup(cmd *cobra.Command, _ []string) error {
	setupLogging(cmd)
	v := viperForCmd(cmd)

	db, err := backup.OpenReadOnly(v.GetString("db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	info, err := backup.Create(cmd.Context(), backup.DB{DB: db}, backup.Options{
		Dir:     v.GetString("out-dir"),
		Name:    v.GetString("name"),
		Keep:    v.GetInt("keep"),
		Configs: v.GetStringSlice("config"),
	})
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	fmt.Printf("Backup: %s (%d bytes, integrity ok)\n", info.Path, info.Size)
	if info.ConfigPath != "" {
		fmt.Printf("Config: %s\n", info.ConfigPath)
	}
	return nil
}

func restoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Replace the database with a backup (stop the server first)",
		Long: `Unpacks the backup next to --db, checks it with PRAGMA integrity_check,
moves the current database and its -wal and -shm files aside as
<db>.pre-restore-YYYYmmdd-HHMMSS and puts the backup in place. Nothing is
changed if the backup is damaged. Run it with the server stopped, as the
user the server runs as.`,
		RunE:         runRestore,
		SilenceUsage: true,
	}
	f := cmd.Flags()
	f.String("db", "examiner.db", "SQLite database path")
	f.String("backup", "", "Backup file (.db.gz or .db)")
	f.String("log-level", "info", "Log level (debug, info, warn, error)")
	f.String("log-format", "text", "Log format (text, json)")
	_ = cmd.MarkFlagRequired("backup")
	return cmd
}

func runRestore(cmd *cobra.Command, _ []string) error {
	setupLogging(cmd)
	v := viperForCmd(cmd)

	dbPath := v.GetString("db")
	res, err := backup.Restore(cmd.Context(), v.GetString("backup"), dbPath, time.Now())
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	fmt.Printf("Restored %s from %s (integrity ok)\n", dbPath, v.GetString("backup"))
	if res.Previous != "" {
		fmt.Printf("Previous database kept as %s\n", res.Previous)
	}
	return nil
}

func statsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "stats",
		Short:        "Count users, questions, sessions, messages and grades (to compare before and after a restore)",
		RunE:         runStats,
		SilenceUsage: true,
	}
	f := cmd.Flags()
	f.String("db", "examiner.db", "SQLite database path")
	f.Bool("json", false, "Print JSON instead of text")
	f.String("log-level", "info", "Log level (debug, info, warn, error)")
	f.String("log-format", "text", "Log format (text, json)")
	return cmd
}

func runStats(cmd *cobra.Command, _ []string) error {
	setupLogging(cmd)
	v := viperForCmd(cmd)

	// Read-only: counting must not change the database being compared.
	db, err := backup.OpenReadOnly(v.GetString("db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	st, err := backup.Count(cmd.Context(), db)
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}
	if v.GetBool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	fmt.Print(st)
	return nil
}
