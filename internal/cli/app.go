// Package cli is the enrichednow command-line tool.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Environment variables the tool reads.
const (
	envAPIKey    = enrichednow.EnvAPIKey
	envAPIURL    = "ENRICHEDNOW_API_URL"
	envConfigDir = "ENRICHEDNOW_CONFIG_DIR"
)

const docsURL = "https://docs.enrichednow.com"

// BuildInfo is what the binary knows about itself.
type BuildInfo struct {
	Version, Commit, Date string
}

func (b BuildInfo) String() string {
	out := b.Version
	var extra []string
	if b.Commit != "" {
		extra = append(extra, b.Commit)
	}
	if b.Date != "" {
		extra = append(extra, b.Date)
	}
	if len(extra) > 0 {
		out += " (" + strings.Join(extra, ", ") + ")"
	}
	return out
}

// App is one invocation of the tool: where it reads and writes, and what its global flags said.
type App struct {
	In       io.Reader
	Out, Err io.Writer
	// InTTY, OutTTY and ErrTTY say whether the three are a terminal.
	InTTY, OutTTY, ErrTTY bool
	// Width is the width of the terminal behind Out; 0 when it is not one.
	Width int
	// ReadSecret reads a line from the terminal without showing it.
	ReadSecret func() (string, error)
	Build      BuildInfo
	// PollInterval is the pause between two looks at a run; zero means the SDK's default.
	PollInterval time.Duration

	apiKey, apiURL          string
	jsonOut, quiet, noColor bool
	format, outFile         string

	// started is set once a command's own code runs: an error before that is a mistake in the command line.
	started bool
	stdout  *style
	stderr  *style
}

// Main runs the tool with the process's arguments and streams, and returns its exit code.
func Main(build BuildInfo) int {
	app := &App{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Build: build,
		InTTY: term.IsTerminal(int(os.Stdin.Fd())), OutTTY: term.IsTerminal(int(os.Stdout.Fd())), ErrTTY: term.IsTerminal(int(os.Stderr.Fd()))}
	if app.OutTTY {
		if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			app.Width = width
		}
	}
	app.ReadSecret = func() (string, error) {
		secret, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(secret), err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, os.Args[1:])
}

// Run runs the tool with these arguments and returns its exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	root := a.rootCommand()
	root.SetArgs(args)
	a.styles()
	if err := root.ExecuteContext(ctx); err != nil {
		return a.report(err)
	}
	return exitOK
}

func (a *App) styles() {
	plain := a.noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
	a.stdout = &style{on: a.OutTTY && !plain}
	a.stderr = &style{on: a.ErrTTY && !plain}
}

func (a *App) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "enrichednow",
		Short: "Run scrapers and read datasets from the command line",
		Long: `enrichednow is the command-line tool for the EnrichedNow API.

Start with a key from the console (API keys):

  enrichednow login
  enrichednow scrapers list
  enrichednow run ats.board --input board_url=https://boards.greenhouse.io/acme

The key is read from --api-key, then ` + envAPIKey + `, then what "enrichednow login" saved.

Exit codes: 0 success · 1 error · 2 a mistake in the command line · 3 a run that did not end ok or partial ·
4 no API key, or one that was not accepted · 130 interrupted.

Documentation: ` + docsURL,
		Example: `  # See what a scraper takes and returns
  enrichednow scrapers show careers.company

  # Run it, wait, and save the records
  enrichednow run careers.company --input domain=acme.com --out jobs.csv

  # The same, for a script
  enrichednow run careers.company --input domain=acme.com --json --quiet | jq '.[].title'`,
		Version:       a.Build.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		PersistentPreRun: func(*cobra.Command, []string) {
			a.started = true
			a.styles()
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.SetVersionTemplate("enrichednow {{.Version}}\n")
	root.CompletionOptions.HiddenDefaultCmd = false
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &cliError{code: "usage", msg: err.Error(), hint: fmt.Sprintf("See `%s --help`.", cmd.CommandPath()), exit: exitUsage}
	})

	flags := root.PersistentFlags()
	flags.StringVar(&a.apiKey, "api-key", "", "API `key` (en_live_…); prefer \"enrichednow login\" or "+envAPIKey+", which keep it out of your shell history")
	flags.StringVar(&a.apiURL, "api-url", "", "`address` of the API (default "+enrichednow.DefaultBaseURL+", or "+envAPIURL+")")
	flags.BoolVar(&a.jsonOut, "json", false, "machine-readable JSON on standard output; the same as --output json")
	flags.BoolVarP(&a.quiet, "quiet", "q", false, "no progress and no hints on standard error; errors are still printed")
	flags.BoolVar(&a.noColor, "no-color", false, "no colours (also off when NO_COLOR is set or the output is not a terminal)")

	root.AddGroup(&cobra.Group{ID: "core", Title: "Scrapers and runs:"}, &cobra.Group{ID: "data", Title: "Datasets:"},
		&cobra.Group{ID: "account", Title: "Account:"})
	root.AddCommand(a.cmdRun(), a.cmdScrapers(), a.cmdRuns(), a.cmdData(), a.cmdUsage(), a.cmdLogin(), a.cmdLogout(), a.cmdHealth())
	return root
}

// addOutputFlags gives a command that prints data the flags that choose its format and its destination.
func (a *App) addOutputFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&a.format, "output", "o", "", "output `format`: table, json, jsonl or csv (default table; with --out, by the file's extension)")
	cmd.Flags().StringVar(&a.outFile, "out", "", "write the output to this `file` instead of standard output")
	_ = cmd.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return formats, cobra.ShellCompDirectiveNoFileComp
	})
}

// resolveKey returns the key to use and where it came from: the flag, the environment, or the saved one.
func (a *App) resolveKey() (key, source string, err error) {
	if a.apiKey != "" {
		return strings.TrimSpace(a.apiKey), "--api-key", nil
	}
	if key := strings.TrimSpace(os.Getenv(envAPIKey)); key != "" {
		return key, envAPIKey, nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return "", "", err
	}
	return cfg.APIKey, "login", nil
}

func (a *App) baseURL() string {
	if a.apiURL != "" {
		return a.apiURL
	}
	if fromEnv := strings.TrimSpace(os.Getenv(envAPIURL)); fromEnv != "" {
		return fromEnv
	}
	return enrichednow.DefaultBaseURL
}

// client builds the SDK client. With needKey false, a client without a key is fine: the catalogue is public.
func (a *App) client(needKey bool) (*enrichednow.Client, error) {
	key, _, err := a.resolveKey()
	if err != nil {
		return nil, err
	}
	if key == "" && needKey {
		return nil, errNoKey()
	}
	return a.clientWith(key)
}

func (a *App) clientWith(key string) (*enrichednow.Client, error) {
	opts := []enrichednow.Option{enrichednow.WithBaseURL(a.baseURL()), enrichednow.WithUserAgent("enrichednow-cli/" + a.Build.Version)}
	if key == "" {
		opts = append(opts, enrichednow.WithoutAPIKey())
	} else {
		opts = append(opts, enrichednow.WithAPIKey(key))
	}
	client, err := enrichednow.NewClient(opts...)
	if err != nil {
		return nil, &cliError{code: "usage", msg: strings.TrimPrefix(err.Error(), "enrichednow: "), hint: "Check --api-url and " + envAPIURL + ".", exit: exitUsage}
	}
	return client, nil
}

// note prints a line for the person at the terminal on standard error, unless --quiet.
func (a *App) note(format string, args ...any) {
	if !a.quiet {
		fmt.Fprintf(a.Err, format+"\n", args...)
	}
}

// readLine reads one line from standard input.
func (a *App) readLine() (string, error) {
	line, err := bufio.NewReader(a.In).ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// exactArgs is cobra.ExactArgs with a message that says what is missing.
func exactArgs(names ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == len(names) {
			return nil
		}
		what := "<" + strings.Join(names, "> <") + ">"
		msg := fmt.Sprintf("`%s` takes %s", cmd.CommandPath(), what)
		if len(names) == 0 {
			msg = fmt.Sprintf("`%s` takes no arguments", cmd.CommandPath())
		}
		return &cliError{code: "usage", msg: msg, hint: fmt.Sprintf("See `%s --help`.", cmd.CommandPath()), exit: exitUsage}
	}
}

// completeScrapers completes a scraper id from the catalogue.
func (a *App) completeScrapers(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client, err := a.client(false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	scrapers, err := client.Scrapers.List(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, s := range scrapers {
		out = append(out, s.ID+"\t"+s.Title)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
