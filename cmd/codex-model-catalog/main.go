package main

import (
	"os"

	modelcatalog "codex-model-catalog"
)

func main() {
	os.Exit(modelcatalog.Run(os.Args[1:]))
}
