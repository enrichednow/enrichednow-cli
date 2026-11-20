# enrichednow — the EnrichedNow command-line tool

[![CI](https://github.com/enrichednow/enrichednow-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/enrichednow/enrichednow-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/enrichednow/enrichednow-cli)](https://github.com/enrichednow/enrichednow-cli/releases/latest)

Run scrapers, read their records and query the datasets of the [EnrichedNow API](https://docs.enrichednow.com)
from a terminal or a script.

```console
$ enrichednow login
API key (it is not shown):
Logged in. Key en_live_Zk3uTq8v… saved to /home/you/.config/enrichednow/config.json

$ enrichednow run ats.board --input board_url=https://boards.greenhouse.io/acme
Run run_841742f37553189ff9c27ba1 ended ok: 3 records, 1 unit, 885ms.
8 more fields are not shown in the table; use --output json, jsonl or csv for all of them.
TITLE                          COMPANY_NAME  LOCATION         EMPLOYMENT_TYPE  POSTED_AT   SALARY
Senior Software Engineer       Acme          Austin, TX       Full-time        2026-11-03
Product Designer               Acme          Remote, US       Full-time        2026-11-05  $120,000 - $150,000
Account Executive, Mid-Market  Acme          London, England
```

## Install

On Linux and macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/enrichednow/enrichednow-cli/main/install.sh | sh
```

The script downloads the latest release for your machine, checks it against the release's `checksums.txt`
(SHA-256), and puts `enrichednow` in `/usr/local/bin` if you can write there, otherwise in `~/.local/bin`. Nothing
runs as root. To choose the version or the directory:

```bash
curl -fsSL https://raw.githubusercontent.com/enrichednow/enrichednow-cli/main/install.sh |
  ENRICHEDNOW_VERSION=0.3.1 ENRICHEDNOW_INSTALL_DIR="$HOME/bin" sh
```

On Windows, download `enrichednow_<version>_windows_amd64.zip` (or `arm64`) from the
[latest release](https://github.com/enrichednow/enrichednow-cli/releases/latest) and put `enrichednow.exe` on
your `PATH`.

Every release has binaries for Linux, macOS and Windows on amd64 and arm64, and a `checksums.txt`.

With Go 1.26 or later you can also build it yourself:

```bash
go install github.com/enrichednow/enrichednow-cli/cmd/enrichednow@latest
```

## Commands

| | |
| --- | --- |
| `enrichednow login` / `logout` | Save an API key on this computer, or forget it. |
| `enrichednow scrapers list` | The catalogue, with what a run of each costs. |
| `enrichednow scrapers show <id>` | One scraper: its input fields (required or optional, defaults, limits), the fields of its records, how it is charged. |
| `enrichednow run <id>` | Start a run, show its progress, wait, print the records. |
| `enrichednow runs list` | Your runs, newest first. `--scraper`, `--status`, `--limit`, `--cursor`, `--all`. |
| `enrichednow runs get <run-id>` | One run: status, reason, what it did. |
| `enrichednow runs wait <run-id>` | Wait until a run has ended. |
| `enrichednow runs cancel <run-id>` | Ask a run to stop. |
| `enrichednow runs records <run-id>` | The records a run found. `--format csv\|ndjson` has the API write them. |
| `enrichednow webhooks list\|add\|show\|update\|remove\|rotate-secret` | Endpoints told when a run ends, and their signing secrets. |
| `enrichednow webhooks run-secret\|deliveries\|resend\|verify` | The secret for one run's `--webhook-url`, what was sent, sending again, checking a signature. |
| `enrichednow usage` | Units used over a period. `--from`, `--to`, `--group-by`, `--product`. |
| `enrichednow data companies list\|get <domain>\|postings <domain>` | Datasets: companies. |
| `enrichednow data postings list\|get <posting-id>` | Datasets: job postings, with filters. |
| `enrichednow health` | Is the API up. |
| `enrichednow completion bash\|zsh\|fish\|powershell` | Shell completion. |

Every command has `--help`, with examples.

## The key

The key is looked for in this order:

1. `--api-key`
2. the environment variable `ENRICHEDNOW_API_KEY`
3. what `enrichednow login` saved

`login` asks for the key without showing it (or reads it from a pipe: `printf '%s' "$KEY" | enrichednow login`),
checks that the API accepts it, and saves it in `enrichednow/config.json` in your configuration directory —
`~/.config` on Linux, `~/Library/Application Support` on macOS, `%AppData%` on Windows — readable by you alone
(`0600`). `ENRICHEDNOW_CONFIG_DIR` chooses another directory. The key is never printed; only its first characters
are, the same ones the console shows.

`scrapers list`, `scrapers show` and `health` need no key.

## Running a scraper

```bash
# one field
enrichednow run ats.board --input board_url=https://boards.greenhouse.io/acme

# several
enrichednow run careers.company --input domain=acme.com --input max_detail_pages=10

# the input as JSON: on the command line, from a file, from a pipe
enrichednow run careers.company --input-json '{"domain": "acme.com"}'
enrichednow run careers.company --file input.json
echo '{"domain": "acme.com"}' | enrichednow run careers.company --file -
```

The three can be combined; `--file` is read first, then `--input-json`, then each `--input`, and a later one
wins field by field.

A value given as `--input key=value` is turned into what the scraper's input schema asks for: a number for a
number field, `true`/`false` for a boolean, `a,b,c` for a list — and a string field stays a string, so
`--input zip=02139` is not turned into a number. `enrichednow scrapers show <id>` lists the fields.

Progress and the summary go to standard error; the records go to standard output. So this works:

```bash
enrichednow run ats.board --input board_url=https://boards.greenhouse.io/acme -o jsonl | jq -r .title
```

| Flag | |
| --- | --- |
| `--no-wait` | Start the run, print its id and return. Follow it with `enrichednow runs wait <id>`. |
| `--timeout 10m` | How long to wait. When the time is up the run goes on; the command says how to follow it. |
| `--limit N` | Print at most N records. |
| `--format csv\|ndjson` | The records as the API writes them, instead of `--output`: a CSV has a column for each field of the scraper's record schema, in its order, so every run of a scraper has the same columns. |
| `--idempotency-key KEY` | Name this start. Run the same command with the same key and input within 24 hours and you get the same run back, never a second one. |
| `--webhook-url URL` | An `https` URL that is sent a signed POST when the run ends (`enrichednow webhooks run-secret` once first). |

The request that starts a run carries an idempotency key (yours, or one the tool makes), so after a connection
error or a server error it is sent again without ever starting a second run. If the start still fails, the error
gives the key: run the same command with `--idempotency-key` and that key to get the run if it was started, or to
start it if it was not. Interrupting the wait with Ctrl-C does not stop the run; `enrichednow runs cancel <id>`
does.

## Output

| | |
| --- | --- |
| `-o, --output table` | The default: aligned columns for a person. For records, as many fields as fit the terminal. |
| `-o json` (or `--json`) | Indented JSON. For a list command, the API's own answer (`{"runs": [...], "next_cursor": ...}`); for records, a list of them. |
| `-o jsonl` | One JSON object a line. |
| `-o csv` | One column a field; a nested field becomes `parent.child` columns. A text cell that a spreadsheet would run as a formula (`=`, `+`, `-`, `@`) starts with an apostrophe, as in the API's and the console's CSV. |
| `--out FILE` | Write to a file instead. Without `--output` the format follows the extension: `.json`, `.csv`, `.txt` (table), anything else JSON lines. |

JSON is the API's JSON, field for field and in the API's order — nothing is renamed or left out — so it is safe
to script against, and a field the API adds later shows up without a new version of the tool.

With `--json`, an error is JSON too, on standard error: `{"error": {"code", "message", "details"}}`, with the API's
own code when the API answered it.

Global flags: `--json`, `--quiet` (no progress, no hints), `--no-color`, `--api-url`, `--api-key`. Colours are used
only on a terminal, and never when `NO_COLOR` is set.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | It worked. For `run` and `runs wait`: the run ended `ok` or `partial`. |
| `1` | An error: the API refused the request, could not be reached, or the wait timed out. |
| `2` | A mistake in the command line. |
| `3` | The run ended `failed`, `unsupported` or `cancelled`. The status and the reason are printed. |
| `4` | No API key, or one the API did not accept. |
| `130` | Interrupted. |

```bash
if enrichednow run careers.company --input domain=acme.com --out jobs.jsonl --quiet; then
  wc -l jobs.jsonl
elif [ $? -eq 3 ]; then
  echo "the scrape did not work; see the reason above"
fi
```

## Shell completion

```bash
# bash
source <(enrichednow completion bash)
# zsh
enrichednow completion zsh > "${fpath[1]}/_enrichednow"
# fish
enrichednow completion fish > ~/.config/fish/completions/enrichednow.fish
# PowerShell
enrichednow completion powershell | Out-String | Invoke-Expression
```

Scraper ids are completed from the catalogue; statuses, formats and the datasets' filters from their lists.

## Environment

| Variable | |
| --- | --- |
| `ENRICHEDNOW_API_KEY` | The API key. |
| `ENRICHEDNOW_API_URL` | The address of the API, instead of `https://api.enrichednow.com`. |
| `ENRICHEDNOW_CONFIG_DIR` | Where `login` keeps the key. |
| `NO_COLOR` | Set to anything: no colours. |

## Development

```bash
go test ./...            # includes a test that builds the binary and drives it end to end
go test -short ./...     # without that one
go vet ./... && gofmt -l .
```

The tests run against a fake API (`internal/fakeapi`) that answers the documented endpoints with the documented
shapes. Nothing calls the real API.

The tool is built on the [Go SDK](https://github.com/enrichednow/enrichednow-go), and requires a released
version of it in `go.mod`.

## Releasing

A release is cut by pushing a tag:

```bash
git tag v0.3.1 && git push origin v0.3.1
```

`.github/workflows/release.yml` then runs [GoReleaser](https://goreleaser.com) with `.goreleaser.yaml`: it runs
the tests, builds the Linux, macOS and Windows binaries for amd64 and arm64, archives them, writes
`checksums.txt`, and creates a **draft** release. Look the files over and publish it:

```bash
gh release view v0.3.1
gh release edit v0.3.1 --draft=false
```

`install.sh` installs a release once it is published. To try the build without releasing:

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=publish   # everything but the release, into dist/
```

## Licence

MIT. See [LICENSE](LICENSE). To report a security problem, see [SECURITY.md](SECURITY.md).
