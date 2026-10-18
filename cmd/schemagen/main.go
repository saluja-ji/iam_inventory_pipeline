// Command schemagen writes the Glue column schemas consumed by terraform/modules/glue.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/saluja-ji/iam-inventory/internal/transform"
)

func main() {
	out := "terraform/modules/glue/schemas.json"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	b, err := json.MarshalIndent(transform.Schemas(), "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
