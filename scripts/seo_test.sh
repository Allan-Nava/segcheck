#!/bin/sh
# seo_test.sh — the generated crawler files have to agree with the page.
#
# Every failure here is silent by nature: a sitemap naming an old URL, a
# canonical that drifted, a JSON-LD block truncated by an edit. Nothing renders
# differently, and the only symptom is a page that quietly stops being found.
#
# There is also a mechanical reason a sitemap matters here rather than being
# hygiene: allan-nava.github.io's robots.txt generates one `Sitemap:` line per
# project site whose /<repo>/sitemap.xml answers **200**, from a daily sync. A
# site that ships none is simply absent — 28 of them were, segcheck included.
#
#   sh scripts/seo_test.sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
gate="$root/scripts/seo.sh"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/segcheck-seo-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT HUP TERM

pass=0; fail=0
ok()    { pass=$((pass + 1)); printf '  ok   %s\n' "$1"; }
notok() { fail=$((fail + 1)); printf '  FAIL %s\n' "$1"; }

fixture() {
	d=$1 url=$2
	mkdir -p "$d"
	cat > "$d/index.html" <<HTML
<!doctype html>
<html lang="en">
<head>
<title>segcheck — check what your HLS/DASH segments actually contain</title>
<meta name="description" content="A description of the right sort of length for a search result, saying what the tool does and for whom.">
<meta name="robots" content="index, follow">
<link rel="canonical" href="$url">
<meta property="og:title" content="segcheck">
<meta property="og:image" content="${url}assets/og-card.png">
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"SoftwareApplication","name":"segcheck","url":"$url"}
</script>
</head>
<body><h1>segcheck</h1></body>
</html>
HTML
	printf '# Changelog\n\n## [0.9.9] - 2026-09-03\n\n- a thing.\n' > "$d/CHANGELOG.md"
}

run() { SEO_DIR="$1" CHANGELOG_FILE="$1/CHANGELOG.md" sh "$gate" "$2" >/dev/null 2>&1; }

printf 'seo.sh render:\n'

fixture "$tmp/a" "https://example.test/segcheck/"
run "$tmp/a" render || notok "render failed"

[ -f "$tmp/a/sitemap.xml" ] && ok "a sitemap is written" || notok "no sitemap.xml"
grep -q "https://example.test/segcheck/" "$tmp/a/sitemap.xml" 2>/dev/null &&
	ok "the sitemap carries the canonical from the page, not a hardcoded URL" ||
	notok "the sitemap does not name the page canonical"
grep -qE '<lastmod>[0-9]{4}-[0-9]{2}-[0-9]{2}</lastmod>' "$tmp/a/sitemap.xml" 2>/dev/null &&
	ok "the sitemap has a dated lastmod" || notok "no lastmod"
grep -q "Sitemap: https://example.test/segcheck/sitemap.xml" "$tmp/a/robots.txt" 2>/dev/null &&
	ok "robots.txt points at the sitemap" || notok "robots.txt does not point at the sitemap"
grep -q "Disallow:$" "$tmp/a/robots.txt" 2>/dev/null &&
	ok "robots.txt lets crawlers in" || notok "robots.txt does not allow crawling"

# The version comes from the CHANGELOG, so llms.txt cannot describe a release
# that never happened.
grep -q "segcheck 0.9.9" "$tmp/a/llms.txt" 2>/dev/null &&
	ok "llms.txt names the tool and the released version" ||
	notok "llms.txt does not name segcheck 0.9.9"
grep -qi "hls" "$tmp/a/llms.txt" 2>/dev/null &&
	ok "llms.txt says what the tool is about" || notok "llms.txt does not mention HLS"

printf '\nseo.sh check:\n'

run "$tmp/a" check && ok "a rendered tree passes" || notok "check failed on a fresh render"

fixture "$tmp/b" "https://example.test/segcheck/"
run "$tmp/b" render
sed -i.bak 's|https://example.test/segcheck/|https://example.test/moved/|' "$tmp/b/index.html"
run "$tmp/b" check && notok "a canonical the sitemap does not name passed" ||
	ok "a canonical the sitemap does not name fails"

fixture "$tmp/c" "https://example.test/segcheck/"
run "$tmp/c" render
rm "$tmp/c/sitemap.xml"
run "$tmp/c" check && notok "a missing sitemap passed" || ok "a missing sitemap fails"

for tag in description robots canonical og:image; do
	fixture "$tmp/t-$tag" "https://example.test/segcheck/"
	run "$tmp/t-$tag" render
	grep -v "$tag" "$tmp/t-$tag/index.html" > "$tmp/t-$tag/new"
	mv "$tmp/t-$tag/new" "$tmp/t-$tag/index.html"
	run "$tmp/t-$tag" check && notok "a page with no $tag passed" || ok "a page with no $tag fails"
done

fixture "$tmp/ld" "https://example.test/segcheck/"
run "$tmp/ld" render
sed -i.bak 's|,"url":"https://example.test/segcheck/"}|,"url":"https://example.test/segcheck/"|' "$tmp/ld/index.html"
run "$tmp/ld" check && notok "unbalanced JSON-LD passed" ||
	ok "JSON-LD whose braces do not balance fails"

# The failure this gate was written after: the first version of the sitemap
# listed running-in-containers.html, and Pages serves docs/ as it is committed —
# the .md is 200 and the .html is 404. A declared URL that does not exist wastes
# crawl budget, which is the same reason the host root only lists sitemaps that
# answer 200.
printf '\nseo.sh check — every URL in the sitemap has to exist:\n'

fixture "$tmp/ghost" "https://example.test/segcheck/"
run "$tmp/ghost" render
sed -i.bak 's|<loc>https://example.test/segcheck/</loc>|<loc>https://example.test/segcheck/</loc>\n  </url>\n  <url>\n    <loc>https://example.test/segcheck/nowhere.html</loc>|' "$tmp/ghost/sitemap.xml"
run "$tmp/ghost" check && notok "a sitemap URL with no file behind it passed" ||
	ok "a sitemap URL with no file behind it fails"

# lastmod is a claim about the page, and `date +%F` is a claim about the clock.
# Google discounts a lastmod it finds unreliable, and rendering on a release that
# touched only the CHANGELOG would move the date on a page that did not change —
# which is exactly how a crawler learns to ignore it. The date has to come from
# the page: its last commit inside a repository, its mtime outside one.
printf '\nseo.sh — lastmod is dated from the page, not the clock:\n'

fixture "$tmp/lastmod" "https://example.test/segcheck/"
touch -t 202601150000 "$tmp/lastmod/index.html"
run "$tmp/lastmod" render
grep -q '<lastmod>2026-01-15</lastmod>' "$tmp/lastmod/sitemap.xml" 2>/dev/null &&
	ok "lastmod is the date the page last changed" ||
	notok "lastmod is $(awk -F'[<>]' '/lastmod/ { print $3 }' "$tmp/lastmod/sitemap.xml" 2>/dev/null), want 2026-01-15"

# And the gate has to hold it there. A page edited without re-rendering leaves a
# sitemap telling crawlers nothing changed, which is the silent half of this
# whole script.
fixture "$tmp/stale" "https://example.test/segcheck/"
run "$tmp/stale" render
touch -t 202612250000 "$tmp/stale/index.html"
run "$tmp/stale" check && notok "a sitemap whose lastmod predates the page passed" ||
	ok "a lastmod that no longer matches the page fails"

# Inside a work tree with no commit for the page — a shallow clone whose single
# commit did not touch it, which is what CI gets by default — the date cannot be
# established. That has to skip rather than fail: gating the build on how it was
# cloned would fail correct trees, and mtime is not a substitute because a
# checkout stamps every file with the moment it ran.
fixture "$tmp/nohist" "https://example.test/segcheck/"
(cd "$tmp/nohist" && git init -q . && git -c user.email=t@t -c user.name=t commit -q --allow-empty -m empty) 2>/dev/null
run "$tmp/nohist" render
run "$tmp/nohist" check && ok "a page with no commit history skips the lastmod check" ||
	notok "a page with no commit history failed the lastmod check instead of skipping"

fixture "$tmp/real" "https://example.test/segcheck/"
printf 'x' > "$tmp/real/extra.html"
run "$tmp/real" render
sed -i.bak 's|<loc>https://example.test/segcheck/</loc>|<loc>https://example.test/segcheck/</loc>\n  </url>\n  <url>\n    <loc>https://example.test/segcheck/extra.html</loc>|' "$tmp/real/sitemap.xml"
run "$tmp/real" check && ok "a sitemap URL whose file exists passes" ||
	notok "a real file was rejected"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
