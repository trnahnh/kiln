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

// Job logs are the only evidence of a failed operation, so each step is narrated and the
// exit trap names the failing status even when the failing tool printed nothing.
const scriptPrologue = `set -e
trap 'rc=$?; if [ $rc -ne 0 ]; then echo "kiln: step \"$step\" failed with exit $rc" >&2; fi' EXIT
step="wait for postgres"
until pg_isready -q; do sleep 2; done
`

func (PostgresCommands) BackupScript(backupID string) string {
	return fmt.Sprintf(scriptPrologue+`step="pg_dump"
echo "kiln: dumping to %[1]s/%[2]s.dump"
df -h %[1]s
pg_dump -Fc -f %[1]s/%[2]s.dump.tmp
step="rename dump"
mv %[1]s/%[2]s.dump.tmp %[1]s/%[2]s.dump
echo "kiln: backup %[2]s complete"`, backupsMountPath, backupID)
}

func (PostgresCommands) RestoreScript(backupID string) string {
	var pick string
	if backupID == platformv1.RestoreFromLatest {
		pick = fmt.Sprintf(`f=$(ls -1 %s/*.dump | sort | tail -n 1)`, backupsMountPath)
	} else {
		pick = fmt.Sprintf(`f=%s/%s.dump`, backupsMountPath, backupID)
	}
	return fmt.Sprintf(scriptPrologue+`step="locate dump"
%s
test -f "$f"
echo "kiln: restoring from $f"
step="pg_restore"
pg_restore --clean --if-exists --no-owner -d "$PGDATABASE" "$f"
echo "kiln: restore complete"`, pick)
}
