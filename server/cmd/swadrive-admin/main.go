package main

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/Doni-15/SwaDrive/server/internal/admincli"
)

func main() {
	readPassword := func(prompt string) (string, error) {
		_, _ = fmt.Fprint(os.Stderr, prompt)
		password, err := term.ReadPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(os.Stderr)
		return string(password), err
	}
	if err := admincli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, readPassword); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
