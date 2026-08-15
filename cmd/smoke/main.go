package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/smoke"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "chat smoke failed:", smoke.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, _ io.Writer) error {
	if len(args) < 2 || args[0] != "smoke" || args[1] != "run" {
		return errors.New("usage: chat smoke run --run-id <id> --target <chat-origin> --core-target <core-origin> --environment <local|staging> --credentials-stdin [--timeout 10m]")
	}
	flags := flag.NewFlagSet("chat smoke run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runID := flags.String("run-id", "", "stable deployment/run identifier")
	target := flags.String("target", "", "Chat HTTP origin")
	coreTarget := flags.String("core-target", "", "Core HTTP origin")
	environment := flags.String("environment", "", "local or staging")
	credentialsStdin := flags.Bool("credentials-stdin", false, "read Core credentials from stdin")
	timeout := flags.Duration("timeout", 10*time.Minute, "overall smoke timeout")
	if err := flags.Parse(args[2:]); err != nil {
		return errors.New("invalid smoke arguments")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional smoke arguments")
	}
	if *environment != "local" && *environment != "staging" {
		if *environment == "production" {
			return errors.New("production smoke targets are explicitly forbidden")
		}
		return errors.New("--environment must be local or staging")
	}
	if !*credentialsStdin {
		return errors.New("--credentials-stdin is required; credential flags and files are unsupported")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 30m")
	}

	manifest, err := smoke.DecodeManifest(stdin, *runID, time.Now().UTC())
	if err != nil {
		return err
	}
	client, err := smoke.NewClient(*target, min(*timeout, 30*time.Second))
	if err != nil {
		return err
	}
	coreClient, err := smoke.NewCoreClient(*coreTarget, min(*timeout, 30*time.Second))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	summary, err := smoke.Run(ctx, client, coreClient, manifest)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout,
		"chat smoke passed: run=%s users=%d conversations=%d messages=%d realtime=%d\nauthorization=passed\n",
		summary.RunID, summary.Users, summary.Conversations, summary.Messages, summary.Realtime,
	)
	return nil
}
