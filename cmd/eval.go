package cmd

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/mdryaaan/kubelens/internal/eval"
	"github.com/mdryaaan/kubelens/internal/llm"
)

var evalOpts struct {
	Corpus   string
	Dir      string
	Format   string
	Output   string
	MinScore float64
}

func newEvalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Score the explanation engine against the labelled corpus",
		Long: `Eval runs the configured provider over 30 hand-labelled incidents and reports
accuracy, per-category precision and recall, a confusion matrix, and how many of the
provider's citations actually appear in the evidence it was given.

The ground-truth category is deliberately withheld from the prompt: handing a model
the right answer and scoring whether it repeats it would measure copying.

The corpus ships inside the binary, so this runs anywhere with no cluster.`,
		Args: cobra.NoArgs,
		Example: `  kubelens eval --provider offline
  kubelens eval --provider ollama --model llama3
  kubelens eval --provider ollama --format markdown -o eval.md`,
		RunE: runEval,
	}

	cmd.Flags().StringVar(&evalOpts.Corpus, "corpus", "labeled-incidents.json",
		"corpus file name within the eval directory")
	cmd.Flags().StringVar(&evalOpts.Dir, "dir", "",
		"read the corpus from this directory instead of the bundled one")
	cmd.Flags().StringVarP(&evalOpts.Format, "format", "f", "text",
		"output format: text, markdown, or json")
	cmd.Flags().StringVarP(&evalOpts.Output, "output", "o", "",
		"write the output to a file instead of stdout")
	cmd.Flags().Float64Var(&evalOpts.MinScore, "min-score", 0,
		"exit non-zero if accuracy falls below this")

	return cmd
}

func runEval(cmd *cobra.Command, _ []string) error {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return err
	}

	var fsys fs.FS
	switch {
	case evalOpts.Dir != "":
		fsys = os.DirFS(evalOpts.Dir)
	case embeddedEval != nil:
		fsys = embeddedEval
	default:
		return fmt.Errorf("this binary was built without the eval corpus; pass --dir")
	}

	provider, err := llm.New(llm.Options{
		Provider: cfg.Provider, Model: cfg.Model,
		BaseURL: cfg.BaseURL, APIKey: cfg.ResolveAPIKey(),
		Temperature: cfg.Temperature,
	})
	if err != nil {
		return err
	}

	result, err := (&eval.Harness{
		FS: fsys, Corpus: evalOpts.Corpus, Provider: provider,
	}).Run(cmd.Context())
	if err != nil {
		return err
	}

	// Baseline numbers announce themselves on stderr as well as in the report,
	// because a report is skimmed and stderr is where a CI log looks.
	if result.Disclaimer != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "kubelens: "+result.Disclaimer)
	}

	out := cmd.OutOrStdout()
	if evalOpts.Output != "" {
		file, err := os.Create(evalOpts.Output)
		if err != nil {
			return fmt.Errorf("creating %s: %w", evalOpts.Output, err)
		}
		defer func() { _ = file.Close() }()
		out = file
	}

	switch evalOpts.Format {
	case "text", "":
		err = eval.WriteText(out, result)
	case "markdown", "md":
		err = eval.WriteMarkdown(out, result)
	case "json":
		err = eval.WriteJSON(out, result)
	default:
		return fmt.Errorf("unknown format %q (want text, markdown, or json)", evalOpts.Format)
	}
	if err != nil {
		return err
	}

	if evalOpts.MinScore > 0 && result.Scores.Accuracy < evalOpts.MinScore {
		return fmt.Errorf("accuracy %.3f is below the required %.3f",
			result.Scores.Accuracy, evalOpts.MinScore)
	}
	return nil
}
