// Command segcheck downloads media segments from an HLS or DASH stream and
// checks what they actually contain against what the manifest claims.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Allan-Nava/segcheck/internal/analyze"
	"github.com/Allan-Nava/segcheck/internal/baseline"
	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/output"
)

// version is overwritten at build time via -ldflags.
var version = "dev"

const usage = `segcheck — check what HLS/DASH segments really contain, not just what the manifest says.

Usage:
  segcheck check <manifest-url> [flags]
  segcheck version

Sampling:
  --segments N        segments to sample per rendition (default 6)
  --renditions N      video renditions to inspect, 0 = all (default 0)
  --audio N           audio renditions to inspect (default 1)
  --subtitles N       subtitle renditions to inspect (default 1)
  --iframes N         EXT-X-I-FRAME-STREAM-INF trick-play rungs to inspect (default 1)
  --from MODE         where to sample: auto|edge|start (default auto: edge for live, start for VOD)
  --parts N           sampled segments whose EXT-X-PART parts are also fetched
                      and compared with the segment they make up (default 1,
                      0 = off; ignored by a stream that publishes no parts)

Conformance:
  --profile NAME      run a conformance rule set: none|apple|dash-if (default none)

Profiles are opt-in, because a rule with no way to turn it off turns a run that
was clean yesterday into a wall of findings today on a stream nobody changed.
Only rules the media can arbitrate are here; a manifest-only assertion belongs
in a manifest linter. Each finding names the rule it comes from and reports the
measured value beside the limit, so it can be argued with.

Live edge:
  --watch DUR         after checking the segments, keep re-reading the manifest
                      for DUR and report what the live edge did (default: off)
  --stall-tolerance N how many re-read intervals the edge may go without a new
                      segment before that is a stall (default 3)

A frozen packager serves a flawless playlist: every segment downloads, parses
and lines up, and only a second look a TARGETDURATION later tells it from a
healthy stream. --watch re-reads at the interval the manifest itself implies —
TARGETDURATION in HLS, minimumUpdatePeriod in DASH — and costs one request per
selected rendition per poll, so --renditions bounds what it costs. It reports
three shapes of a packager losing its clock: a stall, an edge that advances at
every poll and still loses ground against real time, and an edge that moves
backwards onto media a viewer has already played.

Low-latency HLS describes the same media twice — as segments and, more finely,
as the parts published before each segment exists — and a packager muxes the two
separately, so they can disagree. --parts fetches a segment's parts and checks
that they reconstruct it, that a part declared INDEPENDENT really opens on a
keyframe, and that no part outruns PART-TARGET.

Thresholds:
  --duration-tolerance PCT   allowed declared-vs-real duration drift (default 5)
  --gap-tolerance MS         allowed timeline gap or overlap between segments (default 100)
  --bitrate-tolerance PCT    allowed excess over the declared BANDWIDTH (default 10)

HTTP:
  --timeout DUR       per-request timeout (default 15s)
  --concurrency N     simultaneous segment downloads (default 6)
  --header 'K: V'     extra request header, repeatable
  --max-bytes N       cap on a single response body (default 67108864)
  --insecure          skip TLS verification
  --pop ADDR          also fetch every sampled segment through this edge address
                      and compare the bytes, repeatable (default: none)

--pop connects to an address the URL does not resolve to while still sending the
original Host and TLS server name, which is how one CDN edge is asked for the
same URL as another. A stale edge plays perfectly and plays the wrong content,
so only the viewers routed there ever see it. Each edge costs a second copy of
the sample.

Encrypted streams (AES-128):
  --key-file PATH     read the 16-byte content key from a file (raw or hex)
  --key-env NAME      read it from an environment variable, as hex
  --fetch-keys        fetch the key from the URI EXT-X-KEY states
  --clear-lead DUR    the unencrypted lead-in you asked your packager for, so the
                      measured one can be checked against it (default: report only)

The key is never a flag value: one in argv lands in shell history, in the
process list and in every CI log that echoes its own invocation, and unlike a
password it cannot be rotated without re-encrypting the content. --fetch-keys
is off by default because pointing a checker at a key server is a request to a
system that logs, rate-limits and sometimes bills.

Output:
  --output FORMAT     text|json|markdown|prometheus|otlp|slack (default text)
  --no-color          plain text even on a TTY
  --exit-on STATUS    exit 1 when a finding reaches warn|bad|error (default: never)
  --baseline FILE     compare against a saved --output json run and report what
                      changed: a rung that lost bitrate, a rendition that went
                      away, a check that was clean before

Exit status is 0 whenever the check ran, findings or not — a check that ran is a
success. Use --exit-on to gate CI on the result.

prometheus and otlp are for the run that happens on a timer rather than in
front of a person — a cron job feeding a textfile collector, or an OTLP/HTTP
ingest. They expose the aggregate and deliberately carry no per-segment label:
a live stream has different segments every run, so a target label would mint a
new series every tick and never retire one. Counts per check per status, the
worst severity per check and the facts of the run are what a dashboard needs;
the detail behind an alert is in --output json.

slack renders a Block Kit message, worst finding first, for the run that has to
be read rather than queried. Set SEGCHECK_SLACK_WEBHOOK and segcheck posts it;
leave it unset and the payload goes to stdout, so it can be inspected or piped
somewhere else. The webhook is never a flag: it is a credential, and a flag lands
in shell history and in the CI log of every run.

A single run says whether a stream is bad; it cannot say whether it got worse.
--baseline turns segcheck into a regression gate: the diff arrives as ordinary
findings on a "baseline" check, so it renders in every format and --exit-on gates
on it. Only what is stable between two runs is compared — a rendition is, one of
its segments is not, since a live stream has different segments every run — and a
measurement has to move more than 10% to count, because a measured number wobbles.

Examples:
  segcheck check https://cdn.example/master.m3u8
  segcheck check https://cdn.example/manifest.mpd --segments 12 --from edge
  segcheck check https://cdn.example/master.m3u8 --output markdown > report.md
  segcheck check https://cdn.example/master.m3u8 --output prometheus > /var/lib/node_exporter/textfile_collector/segcheck.prom
  SEGCHECK_SLACK_WEBHOOK=$HOOK segcheck check https://cdn.example/live.m3u8 --output slack
  segcheck check https://cdn.example/master.m3u8 --output json > baseline.json
  segcheck check https://cdn.example/master.m3u8 --baseline baseline.json --exit-on bad
  segcheck check https://cdn.example/master.m3u8 --exit-on bad
  segcheck check https://cdn.example/live.m3u8 --watch 2m --exit-on bad
  segcheck check https://cdn.example/ll.m3u8 --parts 2
  segcheck check https://cdn.example/master.m3u8 --profile apple
  segcheck check https://cdn.example/live.m3u8 --pop 203.0.113.7 --pop 198.51.100.4
`

// popFlag collects the repeatable --pop addresses in the order they were given,
// so a finding names the edge the operator named.
type popFlag []string

func (p *popFlag) String() string { return "" }

func (p *popFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("--pop needs an address, such as 203.0.113.7 or edge.example:8443")
	}
	*p = append(*p, v)
	return nil
}

type headerFlag map[string]string

func (h headerFlag) String() string { return "" }

func (h headerFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, ":")
	if !ok {
		return fmt.Errorf("header must be 'Name: value', got %q", v)
	}
	h[strings.TrimSpace(k)] = strings.TrimSpace(val)
	return nil
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is main without the process boundary: arguments in, writers in, exit code
// out. Everything the CLI promises — above all "exit 0 whenever the check ran"
// — is asserted against this rather than against a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "segcheck %s\n", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "check":
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}

	opts := analyze.Defaults()
	headers := headerFlag{}

	// ContinueOnError, not ExitOnError: a flag package that calls os.Exit
	// itself takes the exit code out of run's hands and kills the test process
	// with it.
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	fs.IntVar(&opts.Segments, "segments", opts.Segments, "")
	fs.IntVar(&opts.MaxRenditions, "renditions", opts.MaxRenditions, "")
	fs.IntVar(&opts.MaxAudio, "audio", opts.MaxAudio, "")
	fs.IntVar(&opts.MaxText, "subtitles", opts.MaxText, "")
	fs.IntVar(&opts.MaxIFrame, "iframes", opts.MaxIFrame, "")
	fs.StringVar(&opts.From, "from", opts.From, "")
	fs.IntVar(&opts.PartSegments, "parts", opts.PartSegments, "")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "")
	fs.Float64Var(&opts.DurationTolerancePct, "duration-tolerance", opts.DurationTolerancePct, "")
	fs.Float64Var(&opts.GapToleranceMS, "gap-tolerance", opts.GapToleranceMS, "")
	fs.Float64Var(&opts.BitrateTolerancePct, "bitrate-tolerance", opts.BitrateTolerancePct, "")
	fs.DurationVar(&opts.Watch, "watch", opts.Watch, "")
	fs.Float64Var(&opts.StallTolerance, "stall-tolerance", opts.StallTolerance, "")
	fs.IntVar(&opts.Concurrency, "concurrency", opts.Concurrency, "")
	timeout := fs.Duration("timeout", 15*time.Second, "")
	maxBytes := fs.Int64("max-bytes", fetch.DefaultMaxBytes, "")
	insecure := fs.Bool("insecure", false, "")
	keyFile := fs.String("key-file", "", "")
	keyEnv := fs.String("key-env", "", "")
	fs.BoolVar(&opts.FetchKeys, "fetch-keys", false, "")
	fs.DurationVar(&opts.ClearLead, "clear-lead", opts.ClearLead, "")
	format := fs.String("output", "text", "")
	noColor := fs.Bool("no-color", false, "")
	exitOn := fs.String("exit-on", "", "")
	baselineFile := fs.String("baseline", "", "")
	fs.Var(headers, "header", "")
	fs.Var((*popFlag)(&opts.POPs), "pop", "")

	// flag stops parsing at the first non-flag argument, so `check <url> --flags`
	// — the order everyone actually types — would silently ignore everything
	// after the URL. Parse in rounds instead, collecting positionals as they
	// interrupt the flags.
	positional, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		if len(positional) == 0 {
			fmt.Fprintf(stderr, "check needs a manifest URL\n\n%s", usage)
		} else {
			fmt.Fprintf(stderr, "check takes one manifest URL, got %d: %s\n\n%s", len(positional), strings.Join(positional, " "), usage)
		}
		return 2
	}
	target := positional[0]

	if err := validate(opts, *format, *exitOn); err != nil {
		fmt.Fprintf(stderr, "segcheck: %v\n", err)
		return 2
	}

	// Ctrl-C cancels in-flight downloads instead of leaving them to the timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The content key never arrives as a flag value. A key in argv lands in shell
	// history, in `ps` output, and in every CI log that echoes its own invocation —
	// and unlike a password it cannot be rotated without re-encrypting the content.
	key, err := resolveKeyMaterial(*keyFile, *keyEnv)
	if err != nil {
		fmt.Fprintf(stderr, "segcheck: %v\n", err)
		return 2
	}
	opts.Key = key

	fetch.Version = version
	client := fetch.New(fetch.Options{
		Timeout:  *timeout,
		Headers:  headers,
		Insecure: *insecure,
		MaxBytes: *maxBytes,
	})

	res := analyze.Run(ctx, client, target, opts)

	// The diff is merged into the findings rather than reported beside them, so
	// it sorts worst-first with everything else, renders in every format, and
	// --exit-on gates on a regression without knowing it came from a comparison.
	if *baselineFile != "" {
		diff, err := compareToBaseline(*baselineFile, res)
		if err != nil {
			// A usage error, not a finding. Comparing against a baseline that could
			// not be read would compare against an empty run and report every check
			// as newly appeared — a wall of noise that reads as the stream having
			// changed completely.
			fmt.Fprintf(stderr, "segcheck: %v\n", err)
			return 1
		}
		res.Findings = append(res.Findings, diff...)
		finding.SortWorstFirst(res.Findings)
	}

	switch *format {
	case "json":
		s, err := output.JSON(res)
		if err != nil {
			fmt.Fprintf(stderr, "segcheck: rendering JSON: %v\n", err)
			return 1
		}
		fmt.Fprint(stdout, s)
	case "markdown", "md":
		fmt.Fprint(stdout, output.Markdown(res))
	case "prometheus", "prom":
		fmt.Fprint(stdout, output.Prometheus(res))
	case "otlp":
		fmt.Fprint(stdout, output.OTLP(res))
	case "slack":
		payload := output.Slack(res)
		// The webhook is a credential, so it is only ever read from the
		// environment — a flag lands in shell history and in the CI log of every
		// run. Its presence is also what says "deliver this": without one there
		// is nothing to deliver to, and the payload goes to stdout like every
		// other format so it can be inspected or piped.
		hook := os.Getenv(slackWebhookEnv)
		if hook == "" {
			fmt.Fprint(stdout, payload)
			break
		}
		if err := postSlack(ctx, hook, payload); err != nil {
			// Not a finding: the check ran. This is segcheck failing to do what it
			// was told, which is the one other thing that earns a non-zero exit.
			fmt.Fprintf(stderr, "segcheck: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "segcheck: posted the %s report to Slack (%d findings)\n",
			finding.Worst(res.Findings), len(res.Findings))
	default:
		fmt.Fprint(stdout, output.Text(res, useColor(*noColor)))
	}

	if *exitOn != "" {
		threshold := finding.Status(strings.ToUpper(*exitOn))
		if finding.AtLeast(finding.Worst(res.Findings), threshold) {
			return 1
		}
	}
	return 0
}

// compareToBaseline reads a saved run and reports what changed since it.
//
// The file is whatever `--output json` wrote, read back through the same shape
// that wrote it, so the two cannot drift.
func compareToBaseline(path string, cur finding.Result) ([]finding.Finding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--baseline %s: %w", path, err)
	}
	base, err := output.ParseJSON(b)
	if err != nil {
		return nil, fmt.Errorf("--baseline %s is not a segcheck --output json report: %w", path, err)
	}
	return baseline.Compare(base, cur), nil
}

// slackWebhookEnv is the only way a webhook URL reaches segcheck. It is a
// credential: a flag would put it in shell history and in the log of every CI
// run that used it, which is the same rule --key-env exists for.
const slackWebhookEnv = "SEGCHECK_SLACK_WEBHOOK"

// postSlack delivers a Block Kit payload to an incoming webhook.
//
// It uses its own client rather than the media one on purpose. That client
// carries --insecure, the byte cap, and the --pop address override that pins a
// host to a chosen edge — all correct for fetching a segment from a CDN and all
// wrong for talking to Slack, and the last would silently send the report to
// whatever address was being probed.
//
// A webhook answers 200 with "ok"; anything else carries the reason in the body
// and nowhere else, so the body is what the error quotes. The URL never appears
// in an error: it is the secret, and an error goes to a log.
func postSlack(ctx context.Context, hook, payload string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("posting to Slack: the webhook in %s is not a usable URL", slackWebhookEnv)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "segcheck/"+version)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		// A transport error names the host, which for a webhook URL is
		// hooks.slack.com and not the secret path. url.Error stringifies the whole
		// URL though, so only the message is kept.
		return fmt.Errorf("posting to Slack: the request did not complete (%s)", transportReason(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("posting to Slack: HTTP %d, %q — the report was not delivered",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// transportReason is the tail of a transport error, without the URL a *url.Error
// prints. "dial tcp 1.2.3.4:443: connect: connection refused" is the diagnostic;
// the webhook path in front of it is the credential.
func transportReason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// parseInterspersed parses flags that may appear before and after the
// positional arguments, and returns the positionals in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func validate(opts analyze.Options, format, exitOn string) error {
	switch opts.From {
	case analyze.FromAuto, analyze.FromEdge, analyze.FromStart:
	default:
		return fmt.Errorf("--from must be auto, edge or start, got %q", opts.From)
	}
	switch format {
	case "text", "json", "markdown", "md", "prometheus", "prom", "otlp", "slack":
	default:
		return fmt.Errorf("--output must be text, json, markdown, prometheus, otlp or slack, got %q", format)
	}
	switch strings.ToUpper(exitOn) {
	case "", "WARN", "BAD", "ERROR":
	default:
		return fmt.Errorf("--exit-on must be warn, bad or error, got %q", exitOn)
	}
	if opts.Segments < 0 {
		return fmt.Errorf("--segments cannot be negative")
	}
	if opts.Watch < 0 {
		return fmt.Errorf("--watch cannot be negative")
	}
	if opts.PartSegments < 0 {
		return fmt.Errorf("--parts cannot be negative")
	}
	if opts.ClearLead < 0 {
		return fmt.Errorf("--clear-lead cannot be negative")
	}
	if !analyze.ValidProfile(opts.Profile) {
		return fmt.Errorf("--profile must be none, apple or dash-if, got %q", opts.Profile)
	}
	// A tolerance of zero would make every advance a stall, including the
	// healthy one, which is a checker that cries wolf on every live stream.
	if opts.Watch > 0 && opts.StallTolerance <= 0 {
		return fmt.Errorf("--stall-tolerance must be greater than zero")
	}
	return nil
}

// useColor reports whether to emit ANSI colour: only to a terminal, never when
// NO_COLOR is set, never when redirected to a file or a pipe.
func useColor(noColor bool) bool {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// resolveKeyMaterial reads the AES-128 content key from a file or from the
// environment. Both are given by name rather than by value, which is the whole
// point: a key on the command line is recorded everywhere the command is.
//
// A file may hold the sixteen raw bytes or the same thing as hex, because both are
// what key servers hand out and asking a caller to convert is asking them to leave
// the key in a shell pipeline.
func resolveKeyMaterial(file, env string) ([]byte, error) {
	switch {
	case file != "" && env != "":
		return nil, fmt.Errorf("--key-file and --key-env are alternatives; give one")
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading --key-file: %w", err)
		}
		return parseKeyMaterial(b, "--key-file "+file)
	case env != "":
		v := os.Getenv(env)
		if v == "" {
			return nil, fmt.Errorf("--key-env %s is empty or unset", env)
		}
		return parseKeyMaterial([]byte(v), "--key-env "+env)
	}
	return nil, nil
}

// parseKeyMaterial accepts sixteen raw bytes or their hexadecimal spelling, with or
// without an 0x prefix and with trailing whitespace a file is likely to carry.
//
// The error names the flag, never the value: a message that echoed the key would put
// it in the very logs the flags exist to keep it out of.
func parseKeyMaterial(b []byte, source string) ([]byte, error) {
	if len(b) == 16 {
		return b, nil
	}
	t := strings.TrimSpace(string(b))
	t = strings.TrimPrefix(strings.TrimPrefix(t, "0x"), "0X")
	if len(t) == 32 {
		out, err := hex.DecodeString(t)
		if err == nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("%s is not a 16-byte AES-128 key: expected 16 raw bytes or 32 hex digits", source)
}
