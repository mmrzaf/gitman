package ci

import (
	"context"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"testing"
	"time"
)

func TestRetentionSeparatesLogsActiveHeadAndLatestDeployment(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	for _, row := range []struct {
		id, ref string
		number  int64
	}{{"head", "main", 1}, {"deleted", "gone", 2}} {
		insertTestRun(t, db, row.id, "r1", row.number, "branch", row.ref, "passed", true)
	}
	for _, q := range []string{
		`UPDATE runs SET finished_at=now()-interval '200 days'`,
		`INSERT INTO refs(repo_id,kind,name,commit_hash) SELECT repo_id,ref_kind,ref_name,commit_hash FROM runs WHERE id='head'`,
		`INSERT INTO steps(id,run_id,index,name,status,finished_at,log_bytes,log_lines) VALUES('step','head',0,'test','passed',now()-interval '200 days',6,1)`,
		`INSERT INTO step_logs(step_id,sequence,content,byte_len,line_count) VALUES('step',0,E'hello\n',6,1)`,
		`INSERT INTO deployments(id,repo_id,target,version,commit_hash,created_at) VALUES('latest','r1','production','v2','abc',now()-interval '400 days'),('old','r1','production','v1','abc',now()-interval '500 days')`,
		`INSERT INTO events(id,repo_id,action,created_at) VALUES('old-audit','r1','rule.saved',now()-interval '400 days')`,
	} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewService(db).PruneRetention(ctx, time.Now(), config.DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	var heads, deleted, logs, expired, deploys, audits, bytes int
	if err := db.Q.QueryRow(ctx, `SELECT (SELECT count(*) FROM runs WHERE id='head'),(SELECT count(*) FROM runs WHERE id='deleted'),(SELECT count(*) FROM step_logs),(SELECT count(*) FROM steps WHERE logs_expired_at IS NOT NULL),(SELECT count(*) FROM deployments),(SELECT count(*) FROM events),COALESCE((SELECT log_bytes FROM repository_storage WHERE repo_id='r1'),0)`).Scan(&heads, &deleted, &logs, &expired, &deploys, &audits, &bytes); err != nil {
		t.Fatal(err)
	}
	if heads != 1 || deleted != 0 || logs != 0 || expired != 1 || deploys != 1 || audits != 0 || bytes != 0 {
		t.Fatalf("heads=%d deleted=%d logs=%d expired=%d deployments=%d audit=%d usage=%d", heads, deleted, logs, expired, deploys, audits, bytes)
	}
}

func TestRepositoryLogBudgetAndDuplicateChunks(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	insertTestRun(t, db, "run", "r1", 1, "branch", "main", "running", false)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO steps(id,run_id,index,name,status) VALUES('step','run',0,'test','running')`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db)
	for i := 0; i < 2; i++ {
		if err := svc.AppendLog(ctx, "run", "step", 0, "hello\n"); err != nil {
			t.Fatal(err)
		}
	}
	var used int64
	if err := db.Q.QueryRow(ctx, `SELECT log_bytes FROM repository_storage WHERE repo_id='r1'`).Scan(&used); err != nil || used != 6 {
		t.Fatalf("duplicate usage=%d error=%v", used, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE repository_storage SET log_bytes=1073741820 WHERE repo_id='r1'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.AppendLog(ctx, "run", "step", 1, "hello\n"); err == nil {
		t.Fatal("repository log quota allowed overflow")
	}
	var warning string
	if err := db.Q.QueryRow(ctx, `SELECT log_recording_error FROM steps WHERE id='step'`).Scan(&warning); err != nil || warning == "" {
		t.Fatalf("quota warning=%q error=%v", warning, err)
	}
}
