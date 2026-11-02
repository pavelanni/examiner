package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/backup"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// seeded returns a file store with users, questions, a graded and
// finalized session, messages and scores.
func seeded(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin, err := s.CreateUser(model.User{Username: "admin", PasswordHash: "x", Role: model.UserRoleAdmin, Active: true})
	must(err)
	student, err := s.CreateUser(model.User{Username: "st", PasswordHash: "x", Role: model.UserRoleStudent, Active: true})
	must(err)
	bp, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam", MaxFollowups: 3})
	must(err)
	var qs []int64
	for i := range 3 {
		id, err := s.InsertQuestion(model.Question{CourseID: 1, Text: fmt.Sprint("Q", i), Difficulty: "easy", Topic: "t", Rubric: "r", ModelAnswer: "a", MaxPoints: 5})
		must(err)
		qs = append(qs, id)
	}
	sess, err := s.CreateSession(bp, student, qs)
	must(err)
	threads, err := s.GetThreadsForSession(sess)
	must(err)
	for _, th := range threads {
		_, err := s.AddMessage(model.Message{ThreadID: th.ID, Role: model.RoleStudent, Content: "answer"})
		must(err)
		must(s.UpsertScore(model.QuestionScore{ThreadID: th.ID, LLMScore: 3, LLMFeedback: "ok"}))
	}
	must(s.UpsertGrade(model.Grade{SessionID: sess, LLMGrade: 60}))
	must(s.FinalizeGrade(sess, 70, admin))
	must(s.UpdateSessionStatus(sess, model.StatusReviewed))
	if _, err := s.CreateSession(bp, student, qs[:1]); err != nil { // one in progress
		t.Fatal(err)
	}
	return s
}

func count(t *testing.T, path string) backup.Stats {
	t.Helper()
	db, err := backup.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	st, err := backup.Count(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func fixedNow(s string) func() time.Time {
	ts, _ := time.Parse(time.RFC3339, s)
	return func() time.Time { return ts }
}

func TestCreateAndRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "examiner.db")
	s := seeded(t, dbPath)
	before := count(t, dbPath)
	if want := "users                    2\nquestions                3\nsessions                 2\nsessions.in_progress     1\nsessions.reviewed        1\nmessages                 3\nscores                   3\ngrades                   1\ngrades.final             1\n"; before.String() != want {
		t.Fatalf("stats =\n%s\nwant\n%s", before, want)
	}

	backups := filepath.Join(dir, "backups")
	info, err := backup.Create(context.Background(), s, backup.Options{Dir: backups, Name: "examiner-ru", Now: fixedNow("2026-11-03T10:20:30+03:00")})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name() != "examiner-ru-20261103-072030.db.gz" || info.Size == 0 {
		t.Errorf("info = %+v; want the UTC time in the name", info)
	}
	if fi, err := os.Stat(info.Path); err != nil || (fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows") {
		t.Errorf("backup file mode: %v, %v; want owner-only", fi.Mode(), err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(backups, ".*")); len(leftovers) != 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}

	// The server keeps writing after the backup; the restore brings back
	// the state at backup time.
	if _, err := s.CreateUser(model.User{Username: "late", PasswordHash: "x", Role: model.UserRoleStudent, Active: true}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	res, err := backup.Restore(context.Background(), info.Path, dbPath, time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if res.Previous != dbPath+".pre-restore-20261103-080000" {
		t.Errorf("previous = %q", res.Previous)
	}
	if after := count(t, dbPath); after.String() != before.String() {
		t.Errorf("restored stats =\n%s\nwant\n%s", after, before)
	}
	if prev := count(t, res.Previous); prev.Users != before.Users+1 {
		t.Errorf("the replaced database was not kept: %d users", prev.Users)
	}
	// The restored database opens normally (migrations run) for the server.
	restored, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("server cannot open the restored database: %v", err)
	}
	_ = restored.Close()
}

func TestCreateWhileWriting(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "examiner.db")
	s := seeded(t, dbPath)
	threads, err := s.GetThreadsForSession(1)
	if err != nil || len(threads) == 0 {
		t.Fatal(threads, err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writeErrs sync.Map
	for g := range 50 {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_, err := s.AddMessage(model.Message{ThreadID: threads[g%len(threads)].ID, Role: model.RoleStudent, Content: fmt.Sprint(g, "-", i)})
				if err != nil {
					writeErrs.Store(g, err)
					return
				}
			}
		})
	}
	var infos []backup.Info
	for i := range 3 {
		info, err := backup.Create(context.Background(), s, backup.Options{Dir: filepath.Join(dir, "b"), Name: "examiner-en",
			Now: fixedNow(fmt.Sprintf("2026-11-03T10:00:0%dZ", i))})
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("backup %d during writes: %v", i, err)
		}
		infos = append(infos, info)
	}
	close(stop)
	wg.Wait()
	writeErrs.Range(func(k, v any) bool {
		t.Errorf("writer %v: %v", k, v)
		return true
	})

	// Each backup unpacks to a database that passes integrity_check.
	for _, info := range infos {
		out := filepath.Join(t.TempDir(), "check.db")
		if _, err := backup.Restore(context.Background(), info.Path, out, time.Now()); err != nil {
			t.Errorf("%s: %v", info.Name(), err)
		}
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for d := 1; d <= 5; d++ {
		touch(fmt.Sprintf("examiner-ru-2026110%d-000000.db.gz", d))
		touch(fmt.Sprintf("examiner-ru-2026110%d-000000.config.tar.gz", d))
	}
	touch("examiner-en-20261101-000000.db.gz") // another instance
	touch("notes.txt")

	deleted, err := backup.Rotate(dir, "examiner-ru", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 6 {
		t.Errorf("deleted %v; want 3 backups with their config archives", deleted)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	want := []string{
		"examiner-en-20261101-000000.db.gz",
		"examiner-ru-20261104-000000.config.tar.gz", "examiner-ru-20261104-000000.db.gz",
		"examiner-ru-20261105-000000.config.tar.gz", "examiner-ru-20261105-000000.db.gz",
		"notes.txt",
	}
	if !slices.Equal(names, want) {
		t.Errorf("left %v\nwant %v", names, want)
	}

	list, err := backup.List(dir, "examiner-ru")
	if err != nil || len(list) != 2 || list[0].Name() != "examiner-ru-20261105-000000.db.gz" || list[0].ConfigPath == "" {
		t.Errorf("List = %+v, %v; want newest first with config", list, err)
	}
}

func TestCreateKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	s := seeded(t, filepath.Join(dir, "examiner.db"))
	for i := range 4 {
		_, err := backup.Create(context.Background(), s, backup.Options{Dir: filepath.Join(dir, "b"), Name: "x", Keep: 2,
			Now: fixedNow(fmt.Sprintf("2026-11-03T10:00:0%dZ", i))})
		if err != nil {
			t.Fatal(err)
		}
	}
	list, _ := backup.List(filepath.Join(dir, "b"), "x")
	if len(list) != 2 || list[0].Name() != "x-20261103-100003.db.gz" {
		t.Errorf("list = %v; want the 2 newest", list)
	}
}

func TestCreateArchivesConfigWithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	s := seeded(t, filepath.Join(dir, "examiner.db"))
	cfg := filepath.Join(dir, "cfg")
	if err := os.Mkdir(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for name, body := range map[string]string{"examiner-ru.yaml": "lang: ru\n", "examiner-ru.env": "SECRET=1\n", "Caddyfile": "x {}\n"} {
		p := filepath.Join(cfg, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	info, err := backup.Create(context.Background(), s, backup.Options{Dir: filepath.Join(dir, "b"), Name: "examiner-ru", Configs: paths})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(info.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Caddyfile", "examiner-ru.yaml"}) {
		t.Errorf("archive = %v; want the config files without the .env secrets", names)
	}

	// A missing config file fails the backup and leaves nothing behind.
	_, err = backup.Create(context.Background(), s, backup.Options{Dir: filepath.Join(dir, "b2"), Name: "examiner-ru",
		Configs: []string{filepath.Join(cfg, "missing.yaml")}})
	if err == nil {
		t.Error("missing config file: want an error")
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "b2")); len(left) != 0 {
		t.Errorf("files left after a failed backup: %v", left)
	}
}

func TestRestoreRejectsBadBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "examiner.db")
	s := seeded(t, dbPath)
	_ = s.Close()
	before := count(t, dbPath)

	bad := filepath.Join(dir, "bad.db.gz")
	f, _ := os.Create(bad)
	zw := gzip.NewWriter(f)
	_, _ = zw.Write([]byte(strings.Repeat("not a database ", 100)))
	_ = zw.Close()
	_ = f.Close()

	if _, err := backup.Restore(context.Background(), bad, dbPath, time.Now()); err == nil {
		t.Fatal("corrupt backup: want an error")
	}
	if after := count(t, dbPath); after.String() != before.String() {
		t.Errorf("database changed by a failed restore")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*pre-restore*")); len(left) != 0 {
		t.Errorf("current database moved aside although the restore failed: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".restore-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestCreateRejectsBadName(t *testing.T) {
	s := seeded(t, filepath.Join(t.TempDir(), "examiner.db"))
	for _, name := range []string{"", "../x", ".hidden", "a/b"} {
		if _, err := backup.Create(context.Background(), s, backup.Options{Dir: t.TempDir(), Name: name}); err == nil {
			t.Errorf("name %q: want an error", name)
		}
	}
}

func TestOpenReadOnlyOddPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my backups #1 50%")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "exam?.db")
	if runtime.GOOS == "windows" {
		dbPath = filepath.Join(dir, "exam.db") // ? is not allowed in Windows file names
	}
	s := seeded(t, dbPath)
	info, err := backup.Create(context.Background(), s, backup.Options{Dir: dir, Name: "examiner"})
	if err != nil {
		t.Fatal(err)
	}
	if st := count(t, dbPath); st.Users != 2 {
		t.Errorf("stats through an odd path = %+v", st)
	}
	if err := backup.Check(context.Background(), dbPath); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Error(err)
	}
}

func TestRestoreRollback(t *testing.T) {
	dir := t.TempDir()
	s := seeded(t, filepath.Join(dir, "live.db"))
	info, err := backup.Create(context.Background(), s, backup.Options{Dir: filepath.Join(dir, "b"), Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// A change after the backup that is only in the -wal file, as when the
	// server is killed: copy the database and its -wal while it is open.
	if _, err := s.CreateUser(model.User{Username: "late", PasswordHash: "x", Role: model.UserRoleStudent, Active: true}); err != nil {
		t.Fatal(err)
	}
	stand := filepath.Join(dir, "stand")
	if err := os.Mkdir(stand, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(stand, "examiner.db")
	for _, side := range []string{"", "-wal"} {
		data, err := os.ReadFile(filepath.Join(dir, "live.db"+side))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dbPath+side, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	want := count(t, dbPath)
	if want.Users != 3 {
		t.Fatalf("copied database has %d users, want 3 (the late one in the -wal)", want.Users)
	}

	res, err := backup.Restore(context.Background(), info.Path, dbPath, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, dbPath); got.Users != 2 {
		t.Fatalf("after restore: %d users, want 2", got.Users)
	}

	// Rolling back restores the moved-aside database, including the change
	// that was only in its -wal file.
	if _, err := backup.Restore(context.Background(), res.Previous, dbPath, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := count(t, dbPath); got.String() != want.String() {
		t.Errorf("after rollback =\n%s\nwant\n%s", got, want)
	}
}
