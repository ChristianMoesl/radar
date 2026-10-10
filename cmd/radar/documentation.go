package main

import (
	"fmt"
	"os"

	documentation "radar"
	"radar/internal/version"
)

// A non-interactive transport for the host Pi extension. Humans can read the
// installed man pages; no daemon, onboarding, network or config access is needed.
func runDocumentation(args []string) {
	flags := outputFlags("radar documentation")
	topic := flags.String("topic", "", "canonical documentation source path from the index")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: radar documentation [--topic <source>] [--json]")
		os.Exit(2)
	}
	result := struct {
		Version  string                  `json:"version"`
		Commit   string                  `json:"commit"`
		Topics   []documentation.Topic   `json:"topics,omitempty"`
		Document *documentation.Document `json:"document,omitempty"`
	}{Version: version.Number, Commit: version.Commit}
	if *topic == "" {
		result.Topics = documentation.Topics()
	} else {
		doc, err := documentation.Read(*topic)
		if err != nil {
			fatal(err)
		}
		result.Document = &doc
	}
	if jsonOutput {
		printResult(result)
	} else if result.Document != nil {
		fmt.Print(result.Document.Content)
	} else {
		fmt.Printf("Radar %s documentation\n", result.Version)
		for _, topic := range result.Topics {
			fmt.Printf("%s — %s\n", topic.Source, topic.Title)
		}
	}
}
