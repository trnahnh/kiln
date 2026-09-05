package controller

import (
	"fmt"

	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
)

// Commands builds the shell run by backup and restore Jobs. It is injected so tests can
// wrap the real commands (for example with a delay) without a test-only code path in
// the reconciler.
type Commands interface {
	BackupScript(backupID string) string
	RestoreScript(backupID string) string
}

const backupsMountPath = "/backups"

// PostgresCommands relies on PGHOST, PGUSER, PGPASSWORD and PGDATABASE being set on the Job.
type PostgresCommands struct{}

func (PostgresCommands) BackupScript(backupID string) string {
	return fmt.Sprintf(`set -e
until pg_isready -q; do sleep 2; done
pg_dump -Fc -f %[1]s/%[2]s.dump.tmp
mv %[1]s/%[2]s.dump.tmp %[1]s/%[2]s.dump`, backupsMountPath, backupID)
}

func (PostgresCommands) RestoreScript(backupID string) string {
	var pick string
	if backupID == platformv1.RestoreFromLatest {
		pick = fmt.Sprintf(`f=$(ls -1 %s/*.dump | sort | tail -n 1)`, backupsMountPath)
	} else {
		pick = fmt.Sprintf(`f=%s/%s.dump`, backupsMountPath, backupID)
	}
	return fmt.Sprintf(`set -e
%s
test -f "$f"
until pg_isready -q; do sleep 2; done
pg_restore --clean --if-exists --no-owner -d "$PGDATABASE" "$f"`, pick)
}
