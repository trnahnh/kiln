package controller

import (
	"os/exec"
	"strings"
	"testing"
)

// The scripts run under the image's /bin/sh; this checks the wrapper's exit-status
// handling with a real shell wherever one is available.
func TestRunWrapperPropagatesFailure(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	script := strings.Replace(scriptPrologue, "until pg_isready -q; do sleep 2; done\n", "", 1) +
		`run "ok step" true
run "bad step" sh -c 'echo boom >&2; exit 3'
echo "unreachable"`
	out, err := exec.Command(sh, "-c", script).CombinedOutput()
	if err == nil {
		t.Fatalf("a failing step must fail the script; output:\n%s", out)
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
		t.Fatalf("expected exit 3, got %v; output:\n%s", err, out)
	}
	for _, want := range []string{`step "bad step"`, "failed with exit 3", "kiln: boom"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "unreachable") {
		t.Error("script continued past a failed step")
	}
}
