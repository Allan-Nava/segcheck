//go:build docker

// The container smoke test. It is not part of `go test ./...` — it needs a
// built image and a working Docker daemon — so it lives behind the `docker`
// build tag and CI runs it as its own job:
//
//	docker build -t segcheck:test .
//	SEGCHECK_IMAGE=segcheck:test go test -tags docker ./internal/analyze/
//
// What it defends is the packaging, not the checks: an image that cannot reach
// an origin over TLS, or that ships a shell, or that runs as root, is a defect
// in how segcheck is delivered even when every unit test is green. The CA-bundle
// case is the one that bites — a `FROM scratch` image with no trust store fails
// every https:// manifest, and the failure reads like an origin problem.
package analyze

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// imageUnderTest is the tag CI builds before running this file.
func imageUnderTest(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed: this is an environment gap, not a failure")
	}
	img := os.Getenv("SEGCHECK_IMAGE")
	if img == "" {
		img = "segcheck:test"
	}
	// A missing image is a real failure, not a skip: the whole point of this
	// file is that the image exists and behaves.
	if out, err := exec.Command("docker", "image", "inspect", img).CombinedOutput(); err != nil {
		t.Fatalf("image %q is not built (docker image inspect: %v)\n%s\nbuild it with: docker build -t %s .", img, err, out, img)
	}
	return img
}

// dockerRun runs the image with the given arguments and returns stdout+stderr
// and the exit code.
func dockerRun(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("docker run failed to start: %v\n%s", err, out)
	}
	return string(out), code
}

func TestDockerImage_ReportsItsVersion(t *testing.T) {
	img := imageUnderTest(t)
	out, code := dockerRun(t, "run", "--rm", img, "version")
	if code != 0 {
		t.Fatalf("`segcheck version` exited %d in the container: %s", code, out)
	}
	if !strings.Contains(out, "segcheck") {
		t.Errorf("version output does not name the tool: %q", out)
	}
	if strings.Contains(out, "dev") {
		t.Errorf("image reports the placeholder version %q — the build must pass -X main.version", strings.TrimSpace(out))
	}
}

// The image must not carry a shell. A static binary needs none, and a container
// with one is a container an attacker can pivot inside.
func TestDockerImage_HasNoShell(t *testing.T) {
	img := imageUnderTest(t)
	for _, sh := range []string{"/bin/sh", "/bin/bash", "/busybox"} {
		if _, code := dockerRun(t, "run", "--rm", "--entrypoint", sh, img, "-c", "echo reachable"); code == 0 {
			t.Errorf("%s runs inside the image: the final stage must contain the binary and the CA bundle, nothing else", sh)
		}
	}
}

// The CA bundle cannot be checked by running the binary — a healthy image and
// one with no trust store both fail an https:// check for reasons that look
// alike from outside — so it is checked by extracting the file. A scratch image
// that lost this line reports every https:// manifest as unreachable, and the
// operator goes looking at their origin.
func TestDockerImage_ShipsATrustStore(t *testing.T) {
	img := imageUnderTest(t)
	name := "segcheck-truststore-probe"
	_, _ = exec.Command("docker", "rm", "-f", name).CombinedOutput()
	if out, err := exec.Command("docker", "create", "--name", name, img).CombinedOutput(); err != nil {
		t.Fatalf("docker create: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = exec.Command("docker", "rm", "-f", name).CombinedOutput() })

	out, err := exec.Command("docker", "cp", name+":/etc/ssl/certs/ca-certificates.crt", "-").Output()
	if err != nil {
		t.Fatalf("the image has no /etc/ssl/certs/ca-certificates.crt: every https:// manifest will fail TLS and look like a broken origin (%v)", err)
	}
	// The tar stream carries the file; a stub would not be this big.
	if len(out) < 10_000 {
		t.Errorf("trust store is %d bytes — that is not a CA bundle", len(out))
	}
}

func TestDockerImage_DoesNotRunAsRoot(t *testing.T) {
	img := imageUnderTest(t)
	out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Config.User}}", img).CombinedOutput()
	if err != nil {
		t.Fatalf("docker image inspect: %v\n%s", err, out)
	}
	user := strings.TrimSpace(string(out))
	if user == "" || user == "root" || strings.HasPrefix(user, "0:") || user == "0" {
		t.Errorf("image runs as %q — set a numeric non-root USER", user)
	}
}

// The real thing: the containerised binary checks a live origin and reports the
// defect that was planted in it, and still exits 0 while doing so.
func TestDockerImage_ChecksAnOriginAndKeepsTheExitZeroRule(t *testing.T) {
	img := imageUnderTest(t)

	segs := cleanSegments(4, 1280, 720)
	// Half a second of missing media before the third segment, undeclared —
	// the same defect TestRun_FindsUndeclaredGap plants, so a green unit suite
	// and a red container here can only mean the packaging.
	for i := 2; i < len(segs); i++ {
		segs[i].startPTS += segTicks / 4
	}
	origin := newReachableOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs},
	})

	out, code := dockerRun(t, "run", "--rm",
		"--add-host=host.docker.internal:host-gateway",
		img, "check", origin+"/master.m3u8", "--output", "json", "--segments", "4")
	if code != 0 {
		t.Fatalf("the check ran but the container exited %d — exit 0 whenever the check ran: %s", code, out)
	}

	var res finding.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--output json did not produce a parseable result: %v\n%s", err, out)
	}
	if res.Segments == 0 {
		t.Fatalf("the container downloaded no segments — it reached the manifest but not the media:\n%s", out)
	}
	if !hasCheck(res, "continuity") {
		t.Fatalf("no continuity finding: the check did not run inside the container:\n%s", out)
	}
	var gap *finding.Finding
	for i, f := range res.Findings {
		if f.Check == "continuity" && f.Status == finding.BAD {
			gap = &res.Findings[i]
			break
		}
	}
	if gap == nil {
		t.Fatalf("the planted gap was not reported from inside the container:\n%s", out)
	}
	if !strings.Contains(gap.Message, "gap") {
		t.Errorf("continuity finding does not name the defect: %q", gap.Message)
	}
}

// newReachableOrigin serves the synthetic stream on an address a container can
// reach. httptest binds to 127.0.0.1, which inside a container is the container
// itself; the origin has to listen on every interface and be addressed through
// the host gateway.
func newReachableOrigin(t *testing.T, variants []variantSpec) string {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: hlsOriginHandler(variants), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	port := ln.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://host.docker.internal:%d", port)
}
