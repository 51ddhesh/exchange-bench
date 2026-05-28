package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/coordinator"
	"github.com/51ddhesh/exchange-bench/internal/workload"
)

func main() {
	workers := flag.String("workers", "localhost:9090", "comma-separated worker gRPC addresses")
	image := flag.String("image", "", "Docker image for contestant sandbox")
	seed := flag.Int64("seed", 42, "workload RNG seed")
	ticks := flag.Int("ticks", 100_000, "total tick count")
	initRate := flag.Int("init-rate", 1_000, "starting rate per worker (ticks/sec)")
	maxRate := flag.Int("max-rate", 50_000, "rate cap per worker (ticks/sec)")
	ramp := flag.Duration("ramp", 5*time.Second, "ramp interval")
	timeout := flag.Duration("timeout", 120*time.Second, "wall-clock timeout")
	runID := flag.String("run-id", fmt.Sprintf("run-%d", time.Now().UnixNano()), "unique run ID")
	flag.Parse()

	if *image == "" {
		fmt.Fprintln(os.Stderr, "error: --image is required")
		flag.Usage()
		os.Exit(1)
	}

	addrs := strings.Split(*workers, ",")
	cfg := coordinator.Config{
		WorkerAddrs:  addrs,
		Image:        *image,
		RunID:        *runID,
		InitialRate:  *initRate,
		MaxRate:      *maxRate,
		RampInterval: *ramp,
	}

	c, err := coordinator.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coordinator: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	tickSlice := workload.Generate(*seed, *ticks)
	fmt.Printf("[coordinator] generated %d ticks  seed=%d  workers=%v\n", len(tickSlice), *seed, addrs)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	metrics, err := c.Run(ctx, tickSlice)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coordinator: run: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("=== ExchangeBench Distributed Results ===")
	fmt.Printf("Ticks sent     : %d\n", metrics.TicksSent)
	fmt.Printf("Ticks acked    : %d\n", metrics.TicksAcked)
	fmt.Printf("Peak TPS       : %.0f\n", metrics.PeakTPS)
	fmt.Printf("P50 latency    : %d µs\n", metrics.P50LatencyUs)
	fmt.Printf("P90 latency    : %d µs\n", metrics.P90LatencyUs)
	fmt.Printf("P99 latency    : %d µs\n", metrics.P99LatencyUs)
	fmt.Printf("Timed out      : %v\n", metrics.TimedOut)
}
