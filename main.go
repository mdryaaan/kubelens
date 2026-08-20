// Command kubelens watches a Kubernetes cluster, detects failing workloads, and
// explains them with cited evidence.
package main

import (
	"embed"
	"io/fs"
	"log"
	"os"

	"github.com/mdryaaan/kubelens/cmd"
)

// The eval corpus is compiled in so `kubelens eval` runs from any directory,
// in any container, with no network. It lives at the repository root because
// the tests read it from there too, and a second copy under a package directory
// would drift from the first.
//
//go:embed all:testdata/eval
var evalCorpus embed.FS

func main() {
	corpus, err := fs.Sub(evalCorpus, "testdata/eval")
	if err != nil {
		log.Fatalf("kubelens: %v", err)
	}

	cmd.SetEmbedded(corpus)
	os.Exit(cmd.Execute())
}
