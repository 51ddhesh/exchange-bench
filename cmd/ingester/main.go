package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/51ddhesh/exchange-bench/internal/telemetry"
)

func main() {
	brokers := flag.String("brokers", "localhost:19092", "comma-separated Redpanda broker addresses")
	topic := flag.String("topic", "telemetry-events", "Redpanda topic to consume")
	dsn := flag.String("dsn", "", "TimescaleDB connection string (required)")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "error: --dsn is required")
		flag.Usage()
		os.Exit(1)
	}

	ing, err := telemetry.NewIngester(strings.Split(*brokers, ","), *topic, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ingester: %v\n", err)
		os.Exit(1)
	}
	defer ing.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
		<-quit
		cancel()
	}()

	fmt.Printf("[ingester] consuming %s from %s\n", *topic, *brokers)

	if err := ing.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "ingester: %v\n", err)
		os.Exit(1)
	}
}
