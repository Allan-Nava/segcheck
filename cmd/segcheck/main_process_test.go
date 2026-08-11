package main

import (
	"os"
	osexec "os/exec"
	"strings"
	"testing"
)

// main itself is one line — os.Exit(run(...)) — but it is the line that wires the
// process boundary to run, and nothing else asserts that the exit code actually
// reaches the shell. `--exit-on` is only useful if it does.
//
// It cannot be called in-process because os.Exit would take the test binary down
// with it, so the test re-executes itself as a subprocess with an environment
// variable that makes it call main.

const reexecEnv = "SEGCHECK_TEST_REEXEC_MAIN"

// TestMain gives the subprocess a way in: with the variable set it becomes
// segcheck, with the arguments after the marker.
func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv(reexecEnv); ok {
		os.Args = append([]string{"segcheck"}, strings.Fields(args)...)
		main() // exits the process with run's code
		return
	}
	os.Exit(m.Run())
}

// runMain re-executes this test binary as segcheck and returns what the process
// actually did.
func runMain(t *testing.T, args string) (stdout, stderr string, code int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	cmd := osexec.Command(exe)
	cmd.Env = append(os.Environ(), reexecEnv+"="+args)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	code = 0
	if ee, ok := err.(*osexec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the subprocess: %v", err)
	}
	return out.String(), errb.String(), code
}

// No arguments is a usage error, and it must exit non-zero and say what is
// wrong — a CLI that prints usage and exits 0 makes a scripted mistake invisible.
func TestMainProcess_UsageErrorExitsTwo(t *testing.T) {
	_, stderr, code := runMain(t, "")
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for a usage error", code)
	}
	if stderr == "" {
		t.Error("a usage error said nothing on stderr")
	}
}

// The version flag is the simplest path all the way through the real process
// boundary: it prints and exits 0.
func TestMainProcess_VersionExitsZero(t *testing.T) {
	stdout, _, code := runMain(t, "--version")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "segcheck") {
		t.Errorf("stdout = %q, want it to name the tool", stdout)
	}
}

// An unreachable origin is a failure to fetch, not a defect in a stream — but the
// check did not run, so this is the one place the process still exits non-zero
// without --exit-on. What matters here is that a code chosen inside run is the
// code the shell sees.
func TestMainProcess_ExitCodeReachesTheShell(t *testing.T) {
	_, _, code := runMain(t, "check --bogus-flag")
	if code == 0 {
		t.Error("an unknown flag exited 0: run's exit code did not reach the process")
	}
}
