package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// Output is selected explicitly, never inferred from whether stdout is a TTY.
var jsonOutput bool

func outputFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ExitOnError)
	flags.BoolVar(&jsonOutput, "json", jsonOutput, "print machine-readable JSON")
	return flags
}

// parseFlags permits options before or after positional arguments, but keeps
// option values (including values spelled --json) and the -- terminator intact.
func parseFlags(flags *flag.FlagSet, args []string) error {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		option := flags.Lookup(name)
		if option == nil {
			// Let flag report unknown options/help before processing later args.
			return flags.Parse(options)
		}
		boolean, isBoolean := option.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && !(isBoolean && boolean.IsBoolFlag()) {
			if i+1 < len(args) {
				i++
				options = append(options, args[i])
			} else {
				return flags.Parse(options)
			}
		}
	}
	return flags.Parse(append(append(options, "--"), positional...))
}

func positionalArgs(command string, args []string, count int) []string {
	flags := outputFlags("radar " + command)
	_ = parseFlags(flags, args)
	if flags.NArg() != count {
		fmt.Fprintf(os.Stderr, "radar %s: expected %d argument(s); see radar help\n", command, count)
		os.Exit(2)
	}
	return flags.Args()
}

func rejectJSON(command string) {
	if jsonOutput {
		fmt.Fprintf(os.Stderr, "radar %s does not produce a JSON result\n", command)
		os.Exit(2)
	}
}
