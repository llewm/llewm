package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/llewm/llewm/components/action-control/internal/validator"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "validate-commit" {
		fmt.Fprintln(stderr, "usage: action-control validate-commit --policy FILE --message FILE [--json]")
		return 2
	}
	flags := flag.NewFlagSet("validate-commit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "", "policy JSON file")
	messagePath := flags.String("message", "", "commit-message file, or - for stdin")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *policyPath == "" || *messagePath == "" {
		if err == nil {
			fmt.Fprintln(stderr, "--policy and --message are required")
		}
		return 2
	}
	policyFile, err := os.Open(*policyPath)
	if err != nil {
		fmt.Fprintln(stderr, "action-control:", err)
		return 2
	}
	policy, err := validator.DecodePolicy(policyFile)
	policyFile.Close()
	if err != nil {
		fmt.Fprintln(stderr, "action-control:", err)
		return 2
	}
	message, err := readMessage(*messagePath)
	if err != nil {
		fmt.Fprintln(stderr, "action-control:", err)
		return 2
	}
	result, err := validator.ValidateCommitMessage(policy, message)
	if err != nil {
		fmt.Fprintln(stderr, "action-control:", err)
		return 2
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintln(stderr, "action-control:", err)
			return 2
		}
	} else if result.Valid {
		fmt.Fprintf(stdout, "valid: policy %s\n", result.PolicyVersion)
	} else {
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Line > 0 {
				fmt.Fprintf(stderr, "line %d: %s\n", diagnostic.Line, diagnostic.Message)
			} else {
				fmt.Fprintln(stderr, diagnostic.Message)
			}
		}
	}
	if !result.Valid {
		return 1
	}
	return 0
}

func readMessage(path string) ([]byte, error) {
	var reader io.Reader
	var file *os.File
	if path == "-" {
		reader = os.Stdin
	} else {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	body, err := io.ReadAll(io.LimitReader(reader, validator.MaxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > validator.MaxInputBytes {
		return nil, fmt.Errorf("commit message exceeds 1 MiB limit")
	}
	return body, nil
}
