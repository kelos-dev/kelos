/*
Copyright 2025 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kelos-dev/kelos/internal/sessionrunner"
)

func selfCopy(dest string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--self-copy" {
		if err := selfCopy(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Self-copy failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg := sessionrunner.ConfigFromEnv()

	if cfg.PodName == "" || cfg.PodNamespace == "" {
		fmt.Fprintln(os.Stderr, "KELOS_POD_NAME and KELOS_POD_NAMESPACE must be set")
		os.Exit(1)
	}

	runner, err := sessionrunner.NewRunner(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session runner: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := runner.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Session runner error: %v\n", err)
		os.Exit(1)
	}
}
