// Command segcheck downloads media segments from an HLS or DASH stream and
// checks what they actually contain against what the manifest claims.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Allan-Nava/segcheck/internal/analyze"
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
  --from MODE         where to sample: auto|edge|start (default auto: edge for live, start for VOD)
  --parts N           sampled segments whose EXT-X-PART parts are also fetched
                      and compared with the segment they make up (default 1,
                      0 = off; ignored by a stream that publishes no parts)

Live edge:
  --watch DUR         after checking the segments, keep re-reading the manifest
                      for DUR and report what the live edge did (default: off)
  --stall-tolerance N how many re-read intervals the edge may go without a new
                      segment before that is a stall (default 3)

A frozen packager serves a flawless playlist: every segment downloads, parses
and lines up, and only a second look a TARGETDURATION later tells it from a
healthy stream. --watch re-reads at the interval the manifest itself implies —
TARGETDURATION in HLS, minimumUpdatePeriod in DASH — and costs one request per
selected rendition per poll, so --renditions bounds what it costs.

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

Encrypted streams (AES-128):
  --key-file PATH     read the 16-byte content key from a file (raw or hex)
  --key-env NAME      read it from an environment variable, as hex
  --fetch-keys        fetch the key from the URI EXT-X-KEY states

The key is never a flag value: one in argv lands in shell history, in the
process list and in every CI log that echoes its own invocation, and unlike a
password it cannot be rotated without re-encrypting the content. --fetch-keys
is off by default because pointing a checker at a key server is a request to a
system that logs, rate-limits and sometimes bills.

Output:
  --output FORMAT     text|json|markdown (default text)
  --no-color          plain text even on a TTY
  --exit-on STATUS    exit 1 when a finding reaches warn|bad|error (default: never)

Exit status is 0 whenever the check ran, findings or not — a check that ran is a
success. Use --exit-on to gate CI on the result.

Examples:
  segcheck check https://cdn.example/master.m3u8
  segcheck check https://cdn.example/manifest.mpd --segments 12 --from edge
  segcheck check https://cdn.example/master.m3u8 --output markdown > report.md
  segcheck check https://cdn.example/master.m3u8 --exit-on bad
  segcheck check https://cdn.example/live.m3u8 --watch 2m --exit-on bad
  segcheck check https://cdn.example/ll.m3u8 --parts 2
`

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
	fs.StringVar(&opts.From, "from", opts.From, "")
	fs.IntVar(&opts.PartSegments, "parts", opts.PartSegments, "")
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
	format := fs.String("output", "text", "")
	noColor := fs.Bool("no-color", false, "")
	exitOn := fs.String("exit-on", "", "")
	fs.Var(headers, "header", "")

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
	case "text", "json", "markdown", "md":
	default:
		return fmt.Errorf("--output must be text, json or markdown, got %q", format)
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
