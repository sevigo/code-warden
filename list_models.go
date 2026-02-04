package main

import (
	"fmt"

	"github.com/anush008/fastembed-go"
)

func main() {
	models := fastembed.ListSupportedModels()
	fmt.Printf("Supported models:\n")
	for _, m := range models {
		fmt.Printf("- %s (Dim: %d): %s\n", m.Model, m.Dim, m.Description)
	}
}
