package demo

import "fmt"

// Deliberate go vet failure to validate CI (M0.2). Do not merge.
func vetCheck() { fmt.Printf("%d\n", "not a number") }
