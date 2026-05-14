package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/server"
)

var errConfigGeneratedExit = errors.New("config generated; exiting")

func loadConfig(path string, in io.Reader, out io.Writer, interactive bool) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil || path != "relaycode.yaml" || !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if err := config.WriteExample(path); err != nil {
		return nil, err
	}
	if !interactive {
		printGeneratedConfigExit(path, out)
		return nil, errConfigGeneratedExit
	}
	if !confirmGeneratedConfig(path, in, out) {
		return nil, errConfigGeneratedExit
	}
	return config.Load(path)
}

func printGeneratedConfigExit(path string, out io.Writer) {
	_, _ = fmt.Fprintf(out, "Config file not found.\nCreated %s from embedded example.\nEdit provider base_url/api_key values, then run again.\nNon-interactive stdin detected; exiting.\n", path)
}

func confirmGeneratedConfig(path string, in io.Reader, out io.Writer) bool {
	_, _ = fmt.Fprintf(out, "Config file not found.\nCreated %s from embedded example.\nEdit provider base_url/api_key values before real use.\nContinue now? [continue/exit]: ", path)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		_, _ = fmt.Fprintln(out, "exit")
		return false
	}
	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "continue", "c", "yes", "y":
		return true
	default:
		return false
	}
}

func main() {
	configPath := flag.String("config", "relaycode.yaml", "path to YAML config")
	flag.Parse()

	cfg, err := loadConfig(*configPath, os.Stdin, os.Stdout, isTerminal(os.Stdin))
	if err != nil {
		if errors.Is(err, errConfigGeneratedExit) {
			return
		}
		log.Fatalf("config: %v", err)
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("relaycode listening on %s (routes=%d)", srv.Addr(), len(cfg.Routes))
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
