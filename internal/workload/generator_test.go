package workload

import (
	"math"
	"reflect"
	"testing"
)

const (
	testSeed       = 42
	testTotalTicks = 100_000
)

// Core invariants

// TestDeterminism is the most important test in this package.
// Identical (seed, totalTicks) must produce byte-identical output on every
// call. A regression here breaks the entire platform's reproducibility guarantee.
func TestDeterminism(t *testing.T) {
	a := Generate(testSeed, testTotalTicks)
	b := Generate(testSeed, testTotalTicks)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Generate is not deterministic: two runs with the same seed produced different output")
	}
}

// TestTotalTickCount verifies the output slice length equals totalTicks exactly.
func TestTotalTickCount(t *testing.T) {
	ticks := Generate(testSeed, testTotalTicks)
	if len(ticks) != testTotalTicks {
		t.Fatalf("want %d ticks, got %d", testTotalTicks, len(ticks))
	}
}

// TestDifferentSeedsDifferentOutput verifies that two distinct seeds produce
// distinct sequences. Identical output would indicate a broken RNG seed path.
func TestDifferentSeedsDifferentOutput(t *testing.T) {
	a := Generate(1, 1_000)
	b := Generate(2, 1_000)
	if reflect.DeepEqual(a, b) {
		t.Fatal("different seeds produced identical output")
	}
}

// Cancel correctness invariants

// TestCancelReferencesOnlyPriorAdds verifies that every CANCEL target was
// previously emitted as an ADD. A cancel against an unknown ID is a phantom
// cancel — it exercises only the trivial CancelNotFound path.
func TestCancelReferencesOnlyPriorAdds(t *testing.T) {
	ticks := Generate(testSeed, testTotalTicks)
	seen := make(map[string]struct{}, testTotalTicks)

	for i, tick := range ticks {
		switch tick.Type {
		case TickAdd:
			seen[tick.OrderID] = struct{}{}
		case TickCancel:
			if _, ok := seen[tick.OrderID]; !ok {
				t.Fatalf("tick %d: CANCEL references unknown order %q", i, tick.OrderID)
			}
		}
	}
}

// TestNoCancelBeforeItsAdd verifies the strict ordering property:
// the ADD for any cancelled order must appear at a strictly lower index
// than the CANCEL. Tested concretely against the first cancel in the sequence.
func TestNoCancelBeforeItsAdd(t *testing.T) {
	ticks := Generate(testSeed, testTotalTicks)

	firstCancelIdx := -1
	for i, tick := range ticks {
		if tick.Type == TickCancel {
			firstCancelIdx = i
			break
		}
	}
	if firstCancelIdx == -1 {
		t.Skip("no cancel ticks generated; skipping")
	}

	targetID := ticks[firstCancelIdx].OrderID
	for i := 0; i < firstCancelIdx; i++ {
		if ticks[i].Type == TickAdd && ticks[i].OrderID == targetID {
			return // ADD found at strictly lower index — pass
		}
	}
	t.Errorf("CANCEL at index %d references %q but no prior ADD found",
		firstCancelIdx, targetID)
}

// TestCancelPoolInvariant is stricter than TestCancelReferencesOnlyPriorAdds.
// It maintains a running resting set and verifies that:
//  1. Every CANCEL target is currently resting at the moment of cancellation.
//  2. The same order is never cancelled twice.
//
// This mirrors the generator's internal resting slice invariant.
func TestCancelPoolInvariant(t *testing.T) {
	ticks := Generate(testSeed, testTotalTicks)
	resting := make(map[string]struct{}, testTotalTicks)

	for i, tick := range ticks {
		switch tick.Type {
		case TickAdd:
			resting[tick.OrderID] = struct{}{}
		case TickCancel:
			if _, ok := resting[tick.OrderID]; !ok {
				t.Fatalf("tick %d: CANCEL references %q which is not currently resting "+
					"(already cancelled or never added)", i, tick.OrderID)
			}
			delete(resting, tick.OrderID)
		}
	}
}

// ADD tick field invariants

// TestLimitPricePositive verifies that every limit ADD tick has Price > 0.
// A zero or negative price is invalid in the ×10000 fixed-point scheme.
func TestLimitPricePositive(t *testing.T) {
	ticks := Generate(testSeed, testTotalTicks)
	for i, tick := range ticks {
		if tick.Type == TickAdd && tick.OrdType == 'L' && tick.Price <= 0 {
			t.Errorf("tick %d: limit order has non-positive price %d", i, tick.Price)
		}
	}
}

// TestMarketOrderPriceIsZero verifies that every market ADD tick has Price == 0.
// Non-zero prices on market orders would confuse the protocol layer.
func TestMarketOrderPriceIsZero(t *testing.T) {
	ticks := Generate(testSeed, testTotalTicks)
	for i, tick := range ticks {
		if tick.Type == TickAdd && tick.OrdType == 'M' && tick.Price != 0 {
			t.Errorf("tick %d: market order has non-zero price %d", i, tick.Price)
		}
	}
}

// TestAddFieldsPopulated verifies that all ADD ticks carry valid, non-zero
// values in every required field.
func TestAddFieldsPopulated(t *testing.T) {
	ticks := Generate(testSeed, 10_000)
	for i, tick := range ticks {
		if tick.Type != TickAdd {
			continue
		}
		if tick.OrderID == "" {
			t.Errorf("tick %d: ADD has empty OrderID", i)
		}
		if tick.Side != 'B' && tick.Side != 'S' {
			t.Errorf("tick %d: ADD has invalid Side %q", i, tick.Side)
		}
		if tick.OrdType != 'L' && tick.OrdType != 'M' {
			t.Errorf("tick %d: ADD has invalid OrdType %q", i, tick.OrdType)
		}
		if tick.Qty < qtyMin || tick.Qty > qtyMax {
			t.Errorf("tick %d: ADD Qty %d out of range [%d, %d]", i, tick.Qty, qtyMin, qtyMax)
		}
	}
}

// TestCancelFieldsClean verifies that CANCEL ticks are zero-valued in every
// field except Type and OrderID. The protocol layer and validator rely on this.
func TestCancelFieldsClean(t *testing.T) {
	ticks := Generate(testSeed, 10_000)
	for i, tick := range ticks {
		if tick.Type != TickCancel {
			continue
		}
		if tick.OrderID == "" {
			t.Errorf("tick %d: CANCEL has empty OrderID", i)
		}
		if tick.Side != 0 || tick.OrdType != 0 || tick.Price != 0 || tick.Qty != 0 {
			t.Errorf("tick %d: CANCEL has non-zero payload fields: side=%d ordType=%d price=%d qty=%d",
				i, tick.Side, tick.OrdType, tick.Price, tick.Qty)
		}
	}
}

// STATISTICAL DISTRIBUTION TESTS

// TestCancelFractionInRange verifies the overall cancel fraction over the full
// workload stays within the theoretical range of the regime parameters.
// The blended mean is expected around 0.25–0.30 given the Gaussian widths.
func TestCancelFractionInRange(t *testing.T) {
	ticks := Generate(testSeed, testTotalTicks)

	var cancelCount int
	for _, tick := range ticks {
		if tick.Type == TickCancel {
			cancelCount++
		}
	}

	frac := float64(cancelCount) / float64(testTotalTicks)

	// Loose bounds: must be within the regime parameter range [0.10, 0.70]
	// with generous slack for Gaussian tails and noise.
	const lo, hi = 0.05, 0.75
	if frac < lo || frac > hi {
		t.Errorf("cancel fraction %.4f out of expected range [%.2f, %.2f]", frac, lo, hi)
	}
}

// TestSlidingWindowNonDetectability is the critical anti-fingerprinting test.
//
// It divides the tick sequence into non-overlapping 1K-tick windows and
// computes the cancel fraction per window. For a sequential-phase generator,
// the window straddling a phase boundary would produce a large first-difference
// (e.g. ~0.40 at the Normal→CancelStorm transition). The Gaussian blend must
// keep all first-differences below 0.15.
//
// Mathematical basis: with the narrowest non-trivial Gaussian (CancelStorm,
// width = 0.06 * totalTicks = 6000 ticks) and the largest cancel-fraction
// delta between adjacent regimes (0.70 - 0.30 = 0.40), the maximum gradient
// of the blended cancelFrac with respect to tick index is approximately
// 0.40 / (6000 * sqrt(2π) * 0.5) ≈ 0.00007 per tick. Over a 1K-tick window
// this yields ≈ 0.07 of smooth drift, well below the 0.15 threshold.
// Binomial sampling noise adds ±~0.03 at p≈0.50, keeping total variance small.
func TestSlidingWindowNonDetectability(t *testing.T) {
	const windowSize = 1_000
	ticks := Generate(testSeed, testTotalTicks)

	numWindows := len(ticks) / windowSize
	if numWindows < 2 {
		t.Skip("too few ticks for sliding window test")
	}

	fracs := make([]float64, numWindows)
	for w := 0; w < numWindows; w++ {
		var count int
		start := w * windowSize
		for j := start; j < start+windowSize; j++ {
			if ticks[j].Type == TickCancel {
				count++
			}
		}
		fracs[w] = float64(count) / float64(windowSize)
	}

	const maxDelta = 0.15
	for w := 1; w < numWindows; w++ {
		delta := math.Abs(fracs[w] - fracs[w-1])
		if delta > maxDelta {
			t.Errorf(
				"step discontinuity between windows %d and %d: delta=%.4f exceeds max %.2f\n"+
					"  window %d (ticks %d–%d): cancel fraction = %.4f\n"+
					"  window %d (ticks %d–%d): cancel fraction = %.4f",
				w-1, w, delta, maxDelta,
				w-1, (w-1)*windowSize, w*windowSize-1, fracs[w-1],
				w, w*windowSize, (w+1)*windowSize-1, fracs[w],
			)
		}
	}
}
