package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/polera/tokeneyes/pkg/tokeneyes"
	"github.com/spf13/pflag"
)

const (
	ExitOK           = 0
	ExitUsage        = 2
	ExitOverflow     = 3
	ExitTokenBudget  = 4
	ExitCostBudget   = 5
	ExitIncomplete   = 6
	ExitVerification = 7
)

type App struct {
	Stdout      io.Writer
	Stderr      io.Writer
	Stdin       io.Reader
	Getwd       func() (string, error)
	Version     string
	Updater     UpgradeManager
	Interactive func() bool
}

func New(version string) *App {
	if version == "" {
		version = "dev"
	}
	return &App{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		Getwd:       os.Getwd,
		Version:     version,
		Updater:     NewGitHubUpdater(),
		Interactive: func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stderr) },
	}
}

func (a *App) Execute(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage()
		return ExitUsage
	}
	switch args[0] {
	case "help", "--help", "-h":
		a.usage()
		return ExitOK
	case "version", "--version":
		fmt.Fprintf(a.Stdout, "tokeneyes %s catalog=%s\n", a.installedVersion(), tokeneyes.CatalogVersion)
		return ExitOK
	case "upgrade":
		return a.upgrade(ctx, args[1:])
	}
	configPath, explicit := findConfigArg(args)
	cfg, err := loadConfig(configPath, explicit)
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	var code int
	switch args[0] {
	case "estimate":
		code = a.runAnalysis(ctx, "estimate", args[1:], cfg)
	case "compare":
		code = a.runAnalysis(ctx, "compare", args[1:], cfg)
	case "history":
		code = a.history(ctx, args[1:], cfg)
	case "diff":
		code = a.diff(ctx, args[1:], cfg)
	case "models":
		code = a.models(args[1:], cfg)
	default:
		a.error(fmt.Errorf("unknown command %q", args[0]))
		a.usage()
		return ExitUsage
	}
	if code == ExitOK {
		a.offerUpgrade(ctx)
	}
	return code
}

type analysisOptions struct {
	model, models, prompt, promptFile, preset, system, systemFile, tools, toolsFile                                      string
	outputTokens, profile, catalogPath, dbPath, maxCost                                                                  string
	processing, imageDetail, documentDetail                                                                              string
	reasoning, cached, maxFile, maxTotal, maxMedia, maxInput                                                             int64
	maxDuration                                                                                                          time.Duration
	workers, maxMediaCount, maxPages                                                                                     int
	transcripts                                                                                                          []string
	readStdin, verify, requireVerify, allowFileUpload, jsonOut, tui, noSave, failIncomplete, failOverflow, includeHidden bool
}

func (a *App) runAnalysis(ctx context.Context, command string, args []string, cfg Config) int {
	root, err := a.Getwd()
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	dbDefault := cfg.Database
	if dbDefault == "" {
		dbDefault, _ = defaultDBPath()
	}
	modelsDefault := strings.Join(cfg.Models, ",")
	if modelsDefault == "" {
		modelsDefault = "gpt-5.5,claude-opus-4-7,gemini-3.1-pro-preview"
	}
	modelDefault := cfg.Model
	if modelDefault == "" {
		modelDefault = "gpt-5.5"
	}
	outputsDefault := joinInts(cfg.OutputTokens)
	if outputsDefault == "" {
		outputsDefault = "1000,4000,16000"
	}
	profileDefault := cfg.Profile
	if profileDefault == "" {
		profileDefault = "none"
	}

	maxDuration, _ := time.ParseDuration(cfg.MaxDuration)
	o := analysisOptions{model: modelDefault, models: modelsDefault, outputTokens: outputsDefault, reasoning: cfg.ReasoningTokens, cached: cfg.CachedTokens, maxFile: cfg.MaxFileBytes, maxTotal: cfg.MaxTotalBytes, maxMedia: cfg.MaxMediaBytes, maxMediaCount: cfg.MaxMediaCount, maxPages: cfg.MaxPages, maxDuration: maxDuration, processing: cfg.Processing, imageDetail: cfg.ImageDetail, documentDetail: cfg.DocumentDetail, transcripts: append([]string(nil), cfg.Transcripts...), workers: cfg.Workers, profile: profileDefault, catalogPath: cfg.Catalog, dbPath: dbDefault, noSave: cfg.NoSave, failIncomplete: cfg.FailIncomplete, failOverflow: cfg.FailOverflow, maxInput: cfg.MaxInputTokens, maxCost: cfg.MaxCostUSD, includeHidden: cfg.IncludeHidden}
	if o.processing == "" {
		o.processing = "native"
	}
	if o.imageDetail == "" {
		o.imageDetail = "auto"
	}
	if o.documentDetail == "" {
		o.documentDetail = "auto"
	}
	fs := pflag.NewFlagSet(command, pflag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.SetInterspersed(true)
	if command == "estimate" {
		fs.StringVarP(&o.model, "model", "m", o.model, "model or alias")
	} else {
		fs.StringVarP(&o.models, "models", "m", o.models, "comma-separated models or aliases")
	}
	fs.StringVar(&o.prompt, "prompt", "", "inline prompt")
	fs.StringVar(&o.promptFile, "prompt-file", "", "prompt file")
	fs.StringVar(&o.preset, "preset", "", "input preset: tracked, changed, or plan")
	fs.BoolVar(&o.readStdin, "stdin", false, "read input from stdin")
	fs.StringVar(&o.system, "system", "", "system prompt (counted as overhead)")
	fs.StringVar(&o.systemFile, "system-file", "", "system prompt file")
	fs.StringVar(&o.tools, "tools", "", "JSON tool declarations (counted as overhead)")
	fs.StringVar(&o.toolsFile, "tools-file", "", "JSON tool declarations file")
	fs.StringVarP(&o.outputTokens, "output-tokens", "o", o.outputTokens, "comma-separated output scenarios")
	fs.Int64Var(&o.reasoning, "reasoning-tokens", o.reasoning, "reasoning tokens per scenario")
	fs.Int64Var(&o.cached, "cached-tokens", o.cached, "input tokens charged at cached rate")
	fs.StringVar(&o.profile, "profile", o.profile, "overhead profile: none, chat, or codex")
	fs.IntVarP(&o.workers, "workers", "j", o.workers, "bounded collection and comparison workers")
	fs.Int64Var(&o.maxFile, "max-file-bytes", o.maxFile, "per-file byte limit")
	fs.Int64Var(&o.maxTotal, "max-total-bytes", o.maxTotal, "total byte limit")
	fs.Int64Var(&o.maxMedia, "max-media-size", o.maxMedia, "per-media byte limit")
	fs.IntVar(&o.maxMediaCount, "max-media-count", o.maxMediaCount, "media asset limit")
	fs.IntVar(&o.maxPages, "max-pages", o.maxPages, "document page limit")
	fs.DurationVar(&o.maxDuration, "max-duration", o.maxDuration, "audio duration limit")
	fs.StringVar(&o.processing, "processing", o.processing, "media processing: native or normalized-text")
	fs.StringVar(&o.imageDetail, "image-detail", o.imageDetail, "image detail: auto, low, high, or original")
	fs.StringVar(&o.documentDetail, "document-detail", o.documentDetail, "document detail: auto, text, low, or high")
	fs.StringSliceVar(&o.transcripts, "transcript", o.transcripts, "audio-path=text-path transcript sidecar (repeatable)")
	fs.BoolVar(&o.includeHidden, "include-hidden", o.includeHidden, "scan hidden directories")
	fs.BoolVar(&o.verify, "verify", false, "send input to official provider counting endpoints")
	fs.BoolVar(&o.requireVerify, "require-verification", false, "fail if exact verification is unavailable")
	fs.BoolVar(&o.allowFileUpload, "allow-file-upload", false, "authorize temporary provider file uploads during verification")
	fs.BoolVar(&o.jsonOut, "json", false, "emit stable versioned JSON")
	fs.BoolVar(&o.tui, "tui", false, "emit a compact terminal dashboard")
	fs.BoolVar(&o.noSave, "no-save", o.noSave, "do not save run history")
	fs.StringVar(&o.dbPath, "db", o.dbPath, "history database path")
	fs.StringVar(&o.catalogPath, "catalog", o.catalogPath, "JSON model-catalog override")
	fs.Int64Var(&o.maxInput, "max-input-tokens", o.maxInput, "fail when any model exceeds this input budget")
	fs.StringVar(&o.maxCost, "max-cost-usd", o.maxCost, "fail when any expected scenario exceeds this cost")
	fs.BoolVar(&o.failIncomplete, "fail-incomplete", o.failIncomplete, "fail when any source could not be scanned")
	fs.BoolVar(&o.failOverflow, "fail-overflow", o.failOverflow, "fail when input exceeds a context window")
	_ = fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if o.jsonOut && o.tui {
		a.error(errors.New("--json and --tui cannot be used together"))
		return ExitUsage
	}
	if !oneOf(o.processing, "native", "normalized-text") {
		a.error(fmt.Errorf("--processing must be native or normalized-text"))
		return ExitUsage
	}
	if !oneOf(o.imageDetail, "auto", "low", "high", "original") {
		a.error(fmt.Errorf("invalid --image-detail %q", o.imageDetail))
		return ExitUsage
	}
	if !oneOf(o.documentDetail, "auto", "text", "low", "high") {
		a.error(fmt.Errorf("invalid --document-detail %q", o.documentDetail))
		return ExitUsage
	}
	overrides := make([]tokeneyes.ProcessingOverride, 0, len(cfg.Overrides))
	for _, rule := range cfg.Overrides {
		if rule.Glob == "" {
			a.error(errors.New("configuration override requires glob"))
			return ExitUsage
		}
		if rule.Processing != "" && !oneOf(rule.Processing, "native", "normalized-text") {
			a.error(fmt.Errorf("override %q has invalid processing", rule.Glob))
			return ExitUsage
		}
		if rule.ImageDetail != "" && !oneOf(rule.ImageDetail, "auto", "low", "high", "original") {
			a.error(fmt.Errorf("override %q has invalid image_detail", rule.Glob))
			return ExitUsage
		}
		if rule.DocumentDetail != "" && !oneOf(rule.DocumentDetail, "auto", "text", "low", "high") {
			a.error(fmt.Errorf("override %q has invalid document_detail", rule.Glob))
			return ExitUsage
		}
		overrides = append(overrides, tokeneyes.ProcessingOverride{Glob: rule.Glob, Processing: rule.Processing, ImageDetail: rule.ImageDetail, DocumentDetail: rule.DocumentDetail})
	}
	if o.allowFileUpload && !o.verify {
		a.error(errors.New("--allow-file-upload requires --verify"))
		return ExitUsage
	}
	paths := fs.Args()
	for i := 0; i < len(paths); i++ {
		if paths[i] == "-" {
			o.readStdin = true
			paths = append(paths[:i], paths[i+1:]...)
			i--
		}
	}
	outputs, err := parseInts(o.outputTokens)
	if err != nil {
		a.error(fmt.Errorf("--output-tokens: %w", err))
		return ExitUsage
	}
	if o.systemFile != "" {
		o.system, err = readText(o.systemFile)
		if err != nil {
			a.error(err)
			return ExitUsage
		}
	}
	if o.toolsFile != "" {
		o.tools, err = readText(o.toolsFile)
		if err != nil {
			a.error(err)
			return ExitUsage
		}
	}
	if o.maxCost != "" {
		if _, err := parseUSD(o.maxCost); err != nil {
			a.error(fmt.Errorf("--max-cost-usd: %w", err))
			return ExitUsage
		}
	}
	catalog, err := tokeneyes.LoadCatalog(o.catalogPath)
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	models := []string{o.model}
	if command == "compare" {
		models = splitComma(o.models)
	}
	if len(models) == 0 {
		a.error(errors.New("at least one model is required"))
		return ExitUsage
	}
	verificationSends := false
	if o.verify {
		providers := make(map[string]bool)
		unavailable := make(map[string]bool)
		for _, name := range models {
			model, resolveErr := catalog.Resolve(name)
			if resolveErr != nil {
				a.error(resolveErr)
				return ExitUsage
			}
			if model.Verification == "none" {
				unavailable[model.Provider] = true
			} else {
				providers[model.Provider] = true
			}
		}
		var providerNames []string
		for provider := range providers {
			providerNames = append(providerNames, provider)
		}
		sort.Strings(providerNames)
		if len(providerNames) > 0 {
			verificationSends = true
			fmt.Fprintf(a.Stderr, "tokeneyes: verification enabled; assembled input will be sent to official counters for: %s\n", strings.Join(providerNames, ", "))
		}
		if len(unavailable) > 0 {
			var names []string
			for name := range unavailable {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Fprintf(a.Stderr, "tokeneyes: no non-generating count endpoint is configured for: %s\n", strings.Join(names, ", "))
		}
	}

	collection, err := tokeneyes.NewFileCollector().Collect(ctx, tokeneyes.CollectRequest{Paths: paths, Prompt: o.prompt, PromptFile: o.promptFile, Preset: o.preset, Root: root, Stdin: a.Stdin, ReadStdin: o.readStdin, MaxFileBytes: o.maxFile, MaxTotalBytes: o.maxTotal, IncludeHidden: o.includeHidden, MaxMediaBytes: o.maxMedia, MaxMediaCount: o.maxMediaCount, MaxPages: o.maxPages, MaxDuration: o.maxDuration, Transcripts: o.transcripts, Workers: o.workers})
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	if verificationSends && len(collection.Assets) > 0 {
		var labels []string
		var n int64
		for _, asset := range collection.Assets {
			labels = append(labels, asset.Label)
			n += asset.Bytes
		}
		fmt.Fprintf(a.Stderr, "tokeneyes: verification media (%d bytes): %s\n", n, strings.Join(labels, ", "))
	}
	engine := tokeneyes.NewEngine(catalog)
	run, err := engine.Analyze(ctx, tokeneyes.AnalyzeRequest{Collection: collection, Models: models, OutputTokens: outputs, ReasoningTokens: o.reasoning, CachedTokens: o.cached, System: o.system, Tools: o.tools, Profile: o.profile, Verify: o.verify, RequireVerify: o.requireVerify, Workers: o.workers, Preset: o.preset, Processing: o.processing, ImageDetail: o.imageDetail, DocumentDetail: o.documentDetail, AllowFileUpload: o.allowFileUpload, Overrides: overrides})
	if err != nil {
		a.error(err)
		if o.requireVerify {
			return ExitVerification
		}
		return ExitUsage
	}

	if !o.noSave {
		if err := os.MkdirAll(filepath.Dir(o.dbPath), 0o700); err != nil {
			a.error(err)
			return ExitUsage
		}
		store, openErr := tokeneyes.OpenSQLite(o.dbPath)
		if openErr != nil {
			a.error(openErr)
			return ExitUsage
		}
		if saveErr := store.Save(ctx, run); saveErr != nil {
			_ = store.Close()
			a.error(saveErr)
			return ExitUsage
		}
		_ = store.Close()
	}
	if o.jsonOut {
		_ = writeJSON(a.Stdout, run)
	} else if o.tui {
		printTUI(a.Stdout, run, !o.noSave)
	} else {
		printRun(a.Stdout, run, !o.noSave)
	}
	return thresholdExit(run, o)
}

func thresholdExit(run tokeneyes.Run, o analysisOptions) int {
	if o.failOverflow {
		for _, r := range run.Results {
			if r.InputTokens > r.ContextWindow {
				return ExitOverflow
			}
		}
	}
	if o.maxInput > 0 {
		for _, r := range run.Results {
			if r.InputTokens > o.maxInput {
				return ExitTokenBudget
			}
		}
	}
	if o.maxCost != "" {
		budget, err := parseUSD(o.maxCost)
		if err == nil {
			for _, r := range run.Results {
				for _, s := range r.Scenarios {
					if s.Name == "expected" && s.CostMicrosUSD > budget {
						return ExitCostBudget
					}
				}
			}
		}
	}
	if o.failIncomplete && run.Incomplete {
		return ExitIncomplete
	}
	if o.requireVerify {
		for _, r := range run.Results {
			if r.Verification.Error != "" || r.Verification.Method == "" {
				return ExitVerification
			}
		}
	}
	return ExitOK
}

func (a *App) history(ctx context.Context, args []string, cfg Config) int {
	db, _ := defaultDBPath()
	if cfg.Database != "" {
		db = cfg.Database
	}
	limit, jsonOut := 20, false
	fs := pflag.NewFlagSet("history", pflag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.IntVarP(&limit, "limit", "n", limit, "number of runs")
	fs.BoolVar(&jsonOut, "json", false, "emit JSON")
	fs.StringVar(&db, "db", db, "history database path")
	_ = fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	ids := fs.Args()
	if len(ids) > 1 {
		a.error(errors.New("history accepts at most one run ID"))
		return ExitUsage
	}
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		a.error(err)
		return ExitUsage
	}
	store, err := tokeneyes.OpenSQLite(db)
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	defer store.Close()
	if len(ids) == 1 {
		run, getErr := store.Get(ctx, ids[0])
		if getErr != nil {
			a.error(getErr)
			return ExitUsage
		}
		if jsonOut {
			_ = writeJSON(a.Stdout, run)
		} else {
			printHistoryRun(a.Stdout, run)
		}
		return ExitOK
	}
	runs, err := store.List(ctx, limit)
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	if jsonOut {
		_ = writeJSON(a.Stdout, map[string]any{"schema_version": "tokeneyes.history.v1", "runs": runs})
		return ExitOK
	}
	w := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCREATED\tMODELS\tBYTES\tINCOMPLETE")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%t\n", r.ID, r.CreatedAt.Local().Format("2006-01-02 15:04"), strings.Join(r.Models, ","), r.TotalBytes, r.Incomplete)
	}
	_ = w.Flush()
	return ExitOK
}

func (a *App) diff(ctx context.Context, args []string, cfg Config) int {
	db, _ := defaultDBPath()
	if cfg.Database != "" {
		db = cfg.Database
	}
	jsonOut := false
	fs := pflag.NewFlagSet("diff", pflag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.SetInterspersed(true)
	fs.BoolVar(&jsonOut, "json", false, "emit JSON")
	fs.StringVar(&db, "db", db, "history database path")
	_ = fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	ids := fs.Args()
	if len(ids) != 2 {
		a.error(errors.New("diff requires two run IDs"))
		return ExitUsage
	}
	store, err := tokeneyes.OpenSQLite(db)
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	defer store.Close()
	d, err := store.Diff(ctx, ids[0], ids[1])
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	if jsonOut {
		_ = writeJSON(a.Stdout, d)
	} else {
		printDiff(a.Stdout, d)
	}
	return ExitOK
}

func (a *App) models(args []string, cfg Config) int {
	catalogPath, jsonOut := cfg.Catalog, false
	fs := pflag.NewFlagSet("models", pflag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.SetInterspersed(true)
	fs.StringVar(&catalogPath, "catalog", catalogPath, "JSON catalog override")
	fs.BoolVar(&jsonOut, "json", false, "emit JSON")
	_ = fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		rest = []string{"list"}
	}
	catalog, err := tokeneyes.LoadCatalog(catalogPath)
	if err != nil {
		a.error(err)
		return ExitUsage
	}
	switch rest[0] {
	case "list":
		models := catalog.SortedModels()
		if jsonOut {
			_ = writeJSON(a.Stdout, map[string]any{"catalog_version": catalog.Version, "models": models})
			return ExitOK
		}
		w := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "MODEL\tPROVIDER\tCONTEXT\tCOUNTER\tPRICING DATE")
		for _, m := range models {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", m.ID, m.Provider, m.ContextWindow, m.Tokenizer, m.PricingDate)
		}
		_ = w.Flush()
	case "show":
		if len(rest) != 2 {
			a.error(errors.New("models show requires a model name"))
			return ExitUsage
		}
		m, err := catalog.Resolve(rest[1])
		if err != nil {
			a.error(err)
			return ExitUsage
		}
		if jsonOut {
			_ = writeJSON(a.Stdout, m)
		} else {
			_ = writeJSON(a.Stdout, m)
		}
	default:
		a.error(fmt.Errorf("unknown models command %q", rest[0]))
		return ExitUsage
	}
	return ExitOK
}

func printRun(w io.Writer, run tokeneyes.Run, saved bool) {
	printRunDetails(w, run, saved, false)
}

func printHistoryRun(w io.Writer, run tokeneyes.Run) {
	printRunDetails(w, run, false, true)
}

func printRunDetails(w io.Writer, run tokeneyes.Run, saved, showWarnings bool) {
	fmt.Fprintf(w, "Run %s (%s)\n", run.ID, run.CatalogVersion)
	fmt.Fprintf(w, "%d sources, %d bytes", len(run.Sources), totalBytes(run.Sources))
	if run.Incomplete {
		fmt.Fprint(w, " (incomplete)")
	}
	fmt.Fprintln(w)
	for _, result := range run.Results {
		fmt.Fprintf(w, "\n%s [%s; %s]\n", result.Model, result.CounterMethod, result.CapabilityStatus)
		if result.InputLow == result.InputHigh {
			fmt.Fprintf(w, "  Input:   %d tokens\n", result.InputTokens)
		} else {
			fmt.Fprintf(w, "  Input:   %d tokens (range %d–%d, %.0f%% confidence)\n", result.InputTokens, result.InputLow, result.InputHigh, result.Confidence*100)
		}
		fmt.Fprintf(w, "  Context: %.2f%% of %d\n", result.ContextUtilization*100, result.ContextWindow)
		fmt.Fprintln(w, "  Costs:")
		for _, s := range result.Scenarios {
			fmt.Fprintf(w, "    %-8s %6d output + %d reasoning: $%s\n", s.Name, s.OutputTokens, s.ReasoningTokens, s.CostUSD)
		}
		if len(result.Sources) > 0 {
			fmt.Fprintln(w, "  Sources:")
			unsupported := map[string]bool{}
			for _, part := range result.RequestPlan {
				if part.ProviderType == "unsupported" {
					unsupported[part.Source] = true
				}
			}
			for i, s := range result.Sources {
				if i == 5 {
					break
				}
				if unsupported[s.Label] {
					fmt.Fprintf(w, "    unsupported  %s (%s)\n", s.Label, s.Kind)
				} else if s.Count.Low == s.Count.High {
					fmt.Fprintf(w, "    %8d  %s (%s)\n", s.Count.Tokens, s.Label, s.Kind)
				} else {
					fmt.Fprintf(w, "    %d–%d  %s (%s)\n", s.Count.Low, s.Count.High, s.Label, s.Kind)
				}
			}
		}
		if len(result.CountComponents) > 0 {
			fmt.Fprintln(w, "  Modality components:")
			for _, c := range result.CountComponents {
				if c.Unit == "tokens" {
					cost := ""
					if c.InputMicrosUSD+c.CachedInputMicrosUSD > 0 {
						cost = " input $" + tokeneyes.FormatUSD(c.InputMicrosUSD+c.CachedInputMicrosUSD)
					}
					fmt.Fprintf(w, "    %-6s %d–%d  %s [%s]%s\n", c.Modality, c.Low, c.High, c.Source, c.Method, cost)
				} else {
					fmt.Fprintf(w, "    %-6s %d %s  %s [%s]\n", c.Modality, c.Expected, c.Unit, c.Source, c.Method)
				}
			}
		}
		if result.Verification.Method != "" {
			fmt.Fprintf(w, "  Official verification: %d tokens via %s (%s)\n", result.Verification.Tokens, result.Verification.Method, result.Verification.Transport)
		}
	}
	warnings := uniqueWarnings(run)
	if showWarnings && len(warnings) > 0 {
		fmt.Fprintf(w, "\nWarnings (%d):\n", len(warnings))
		for _, warning := range warnings {
			fmt.Fprintln(w, "  - "+warning)
		}
	} else if notice := warningNotice(run, saved); notice != "" {
		fmt.Fprintln(w, "\n"+notice)
	}
	if saved {
		fmt.Fprintln(w, "\nSaved to local history; source contents were not retained.")
	}
}

func printDiff(w io.Writer, d tokeneyes.RunDiff) {
	fmt.Fprintf(w, "Diff %s → %s\n", d.RunA, d.RunB)
	for _, m := range d.Models {
		fmt.Fprintf(w, "  %-28s tokens %+d (%d → %d), expected cost $%s\n", m.Model, m.InputTokenDelta, m.InputTokensA, m.InputTokensB, tokeneyes.FormatUSD(m.ExpectedCostDeltaMicrosUSD))
		for _, c := range m.Components {
			fmt.Fprintf(w, "    %-6s %-8s %+d (%d → %d)\n", c.Modality, c.Unit, c.Delta, c.ValueA, c.ValueB)
		}
	}
	fmt.Fprintf(w, "  Sources: +%d -%d ~%d\n", len(d.Sources.Added), len(d.Sources.Removed), len(d.Sources.Changed))
}

func (a *App) usage() {
	fmt.Fprintln(a.Stderr, `TokenEyes estimates context and API cost without sending content anywhere.

Usage:
  tokeneyes estimate [paths/globs] --model MODEL [--tui] [flags]
  tokeneyes compare [paths/globs] --models MODEL,... [--tui] [flags]
  tokeneyes history [RUN-ID] [--limit N] [--json]
  tokeneyes diff RUN-A RUN-B [--json]
  tokeneyes models list|show [MODEL] [--json]
  tokeneyes upgrade

Use --verify to explicitly send assembled input to Anthropic or Gemini token-counting endpoints.`)
}
func (a *App) error(err error) { fmt.Fprintln(a.Stderr, "tokeneyes:", err) }

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
func readText(path string) (string, error) {
	// #nosec G304 -- path comes from a flag whose purpose is to name a file to read.
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
func splitComma(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func parseInts(s string) ([]int64, error) {
	parts := splitComma(s)
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid non-negative integer %q", p)
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) == 0 {
		return nil, errors.New("at least one value is required")
	}
	return out, nil
}
func joinInts(v []int64) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return strings.Join(parts, ",")
}
func parseUSD(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "$"))
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, errors.New("invalid USD amount")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, errors.New("invalid USD amount")
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > 6 {
		return 0, errors.New("USD amount supports at most six decimal places")
	}
	frac += strings.Repeat("0", 6-len(frac))
	f := int64(0)
	if frac != "" {
		f, err = strconv.ParseInt(frac, 10, 64)
	}
	return whole*1_000_000 + f, err
}
func totalBytes(s []tokeneyes.Source) int64 {
	var n int64
	for _, v := range s {
		n += v.Bytes
	}
	return n
}
func findConfigArg(args []string) (string, bool) {
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(a, "--config=") {
			return strings.TrimPrefix(a, "--config="), true
		}
	}
	return "", false
}
func oneOf(value string, allowed ...string) bool {
	for _, v := range allowed {
		if value == v {
			return true
		}
	}
	return false
}
