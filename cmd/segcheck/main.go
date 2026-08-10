// Command segcheck downloads media segments from an HLS or DASH stream and
// checks what they actually contain against what the manifest claims.
package main

import (
	"context"
	"flag"
	"fmt"
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
  --from MODE         where to sample: auto|edge|start (default auto: edge for live, start for VOD)

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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("segcheck %s\n", version)
		return
	case "help", "--help", "-h":
		fmt.Print(usage)
		return
	case "check":
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	opts := analyze.Defaults()
	headers := headerFlag{}

	fs := flag.NewFlagSet("check", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.IntVar(&opts.Segments, "segments", opts.Segments, "")
	fs.IntVar(&opts.MaxRenditions, "renditions", opts.MaxRenditions, "")
	fs.IntVar(&opts.MaxAudio, "audio", opts.MaxAudio, "")
	fs.StringVar(&opts.From, "from", opts.From, "")
	fs.Float64Var(&opts.DurationTolerancePct, "duration-tolerance", opts.DurationTolerancePct, "")
	fs.Float64Var(&opts.GapToleranceMS, "gap-tolerance", opts.GapToleranceMS, "")
	fs.Float64Var(&opts.BitrateTolerancePct, "bitrate-tolerance", opts.BitrateTolerancePct, "")
	fs.IntVar(&opts.Concurrency, "concurrency", opts.Concurrency, "")
	timeout := fs.Duration("timeout", 15*time.Second, "")
	maxBytes := fs.Int64("max-bytes", fetch.DefaultMaxBytes, "")
	insecure := fs.Bool("insecure", false, "")
	format := fs.String("output", "text", "")
	noColor := fs.Bool("no-color", false, "")
	exitOn := fs.String("exit-on", "", "")
	fs.Var(headers, "header", "")

	// flag stops parsing at the first non-flag argument, so `check <url> --flags`
	// — the order everyone actually types — would silently ignore everything
	// after the URL. Parse in rounds instead, collecting positionals as they
	// interrupt the flags.
	positional, err := parseInterspersed(fs, os.Args[2:])
	if err != nil {
		os.Exit(2)
	}
	if len(positional) != 1 {
		if len(positional) == 0 {
			fmt.Fprintf(os.Stderr, "check needs a manifest URL\n\n%s", usage)
		} else {
			fmt.Fprintf(os.Stderr, "check takes one manifest URL, got %d: %s\n\n%s", len(positional), strings.Join(positional, " "), usage)
		}
		os.Exit(2)
	}
	target := positional[0]

	if err := validate(opts, *format, *exitOn); err != nil {
		fmt.Fprintf(os.Stderr, "segcheck: %v\n", err)
		os.Exit(2)
	}

	// Ctrl-C cancels in-flight downloads instead of leaving them to the timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
			fmt.Fprintf(os.Stderr, "segcheck: rendering JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(s)
	case "markdown", "md":
		fmt.Print(output.Markdown(res))
	default:
		fmt.Print(output.Text(res, useColor(*noColor)))
	}

	if *exitOn != "" {
		threshold := finding.Status(strings.ToUpper(*exitOn))
		if finding.AtLeast(finding.Worst(res.Findings), threshold) {
			os.Exit(1)
		}
	}
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
