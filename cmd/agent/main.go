package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/runner"
	"github.com/51ddhesh/exchange-bench/internal/validator"
	"github.com/51ddhesh/exchange-bench/internal/workload"
)

func main() {
	seed := flag.Int64("seed", 42, "workload RNG seed")
	ticks := flag.Int("ticks", 1_000, "number of ticks to generate")
	rate := flag.Int("rate", 500, "ticks per second")
	artifact := flag.String("artifact", "", "path to compiled contestant binary")
	language := flag.String("language", "go", "contestant language (go, cpp, rust, python, zig)")
	timeout := flag.Duration("timeout", 120*time.Second, "wall-clock timeout for the run")
	flag.Parse()

	if *artifact == "" {
		fmt.Fprintln(os.Stderr, "error: --artifact is required")
		flag.Usage()
		os.Exit(1)
	}

	tickSlice := workload.Generate(*seed, *ticks)
	fmt.Printf("[agent] generated %d ticks  seed=%d\n", len(tickSlice), *seed)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	sb := runner.NewSandbox("deployments/docker/seccomp/contestant.json")
	if err := sb.Start(ctx, *artifact, *language); err != nil {
		fmt.Fprintf(os.Stderr, "[agent] sandbox start: %v\n", err)
		os.Exit(1)
	}
	defer sb.Kill()

	r := runner.New(sb)
	v := validator.New()

	verdictCh := v.Consume(r.Results())

	metrics, runErr := r.Run(ctx, tickSlice, *rate)

	var verdicts []validator.TickVerdict
	for vd := range verdictCh {
		verdicts = append(verdicts, vd)
	}

	var correctTicks int64
	vcounts := make(map[validator.ViolationType]int64)
	for _, vd := range verdicts {
		if vd.Correct {
			correctTicks++
		} else {
			vcounts[vd.Violation]++
		}
	}
	metrics.TicksCorrect = correctTicks

	fmt.Println()
	fmt.Println("=== ExchangeBench Results ===")
	fmt.Printf("Ticks sent     : %d\n", metrics.TicksSent)
	fmt.Printf("Ticks acked    : %d\n", metrics.TicksAcked)
	fmt.Printf("P50 latency    : %d µs\n", metrics.P50LatencyUs)
	fmt.Printf("P90 latency    : %d µs\n", metrics.P90LatencyUs)
	fmt.Printf("P99 latency    : %d µs\n", metrics.P99LatencyUs)
	fmt.Printf("Timed out      : %v\n", metrics.TimedOut)
	if runErr != nil {
		fmt.Printf("Run error      : %v\n", runErr)
	}

	total := int64(len(verdicts))
	var pct float64
	if total > 0 {
		pct = 100 * float64(correctTicks) / float64(total)
	}
	fmt.Printf("\nCorrectness    : %d / %d  (%.2f%%)\n", correctTicks, total, pct)

	if len(vcounts) > 0 {
		fmt.Println("Violations:")
		for v, c := range vcounts {
			fmt.Printf("  %-25s %d\n", v, c)
		}
	}

	hasCritical := vcounts[validator.ViolationOverfill]+vcounts[validator.ViolationZombieFill] > 0
	if hasCritical {
		fmt.Println("\n⚠  CRITICAL violations present (OVERFILL / ZOMBIE_FILL): score flagged.")
	}
}
