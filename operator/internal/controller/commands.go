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

// Job logs are the only evidence of a failed operation. Every external command runs
// through `run`, which captures its stderr to a file and replays it with shell builtins,
// so a tool whose own output never reached the log still leaves its error behind.
const scriptPrologue = `set -u
run() {
  step="$1"; shift
  if "$@" 2>/tmp/kiln-step.err; then return 0; fi
  rc=$?
  echo "kiln: step \"$step\" ($*) failed with exit $rc" >&2
  while IFS= read -r line; do echo "kiln: $line" >&2; done </tmp/kiln-step.err
  exit $rc
}
until pg_isready -q; do sleep 2; done
`

func (PostgresCommands) BackupScript(backupID string) string {
	return fmt.Sprintf(scriptPrologue+`echo "kiln: dumping to %[1]s/%[2]s.dump"
run "inspect volume" ls -ld %[1]s
run "pg_dump" pg_dump -Fc -f %[1]s/%[2]s.dump.tmp
run "rename dump" mv %[1]s/%[2]s.dump.tmp %[1]s/%[2]s.dump
echo "kiln: backup %[2]s complete"`, backupsMountPath, backupID)
}

func (PostgresCommands) RestoreScript(backupID string) string {
	var pick string
	if backupID == platformv1.RestoreFromLatest {
		pick = fmt.Sprintf(`f=$(ls -1 %s/*.dump | sort | tail -n 1)`, backupsMountPath)
	} else {
		pick = fmt.Sprintf(`f=%s/%s.dump`, backupsMountPath, backupID)
	}
	return fmt.Sprintf(scriptPrologue+`%s
run "locate dump" test -f "$f"
echo "kiln: restoring from $f"
run "pg_restore" pg_restore --clean --if-exists --no-owner -d "$PGDATABASE" "$f"
echo "kiln: restore complete"`, pick)
}
