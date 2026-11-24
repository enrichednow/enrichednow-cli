# Changelog

What changed in each version of the tool. Versions before 0.4.0 are in the API's changelog,
<https://docs.enrichednow.com/changelog/>.

## 0.4.0 (not released yet)

Formulas: ready-made workflows that turn a few parameters into a finished list of rows.

- `enrichednow formulas list | get | estimate | run | wait | rows | cancel`. `run` waits for the run to end and prints
  its rows as a table, or writes them to a file (`--out rows.csv`; `-o json`, `jsonl` or `csv`); `rows` reads a run's
  rows with `--fields`, `--min-score` and `--view company`, and downloads the whole run as the API's CSV. Parameters
  are given one a flag (`--param job_titles="Java Developer"`, converted by the formula's parameter schema) or whole
  (`--params-json`).
- `enrichednow saved list | create | run | pause | resume | duplicate | rows`: saved formulas, on a schedule
  (`--schedule weekdays@07:30 --timezone Europe/London`), and their new rows since a time (`--since 24h`).
- The formula errors say what to do: accept the terms in the console, what the plan allows, what is missing,
  duplicate an archived saved formula.
- Built on the Go module 0.4.0.
