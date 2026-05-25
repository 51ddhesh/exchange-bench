package workload

import (
	"fmt"
	"math"
	"math/rand"
)

// MARKET STRUCTURE CONSTANTS

// tickSize: minimum price increment in * 10000 scale
const tickSize = 100

// baseMid: starting mid price
const baseMid = 10_000_000

// midFloor: hard lower bound on mid-price to prevent collapse to zero
const midFloor = 1_000_000

// midSigma: per tick log-normal volatility applied to the mid-price walk
const midSigma = 0.0002

// spreadTicks: half width of limit price band around mid in ticks
// LO prices are drawn uniformly from [mid - spreadTicks * tickSize, mid + spreadTicks * tickSize]
const spreadTicks = 10

// Quantity bounds for all generated orders
const (
	qtyMin int64 = 1
	qtyMax int64 = 100
)

// REGIME DEFINITION

// regime describes one of the five internal market regimes.
//
// center and width are expressed as fractions of totalTicks so that the
// Gaussian geometry scales correctly for any value of totalTicks (100K or 1M).
//
// limitFrac - fraction of ADD ticks that are limit orders (rest are market).
// cancelFrac - fraction of all ticks that are cancels.

type regime struct {
	center     float64
	width      float64
	limitFrac  float64
	cancelFrac float64
}

// The five regimes and their intended stress targets:
//	Warmup - establish baseline book depth, basic add/fill/cancel
//	Normal - mixed order flow, moderate crossing
//	MarketMaking - tight spread, partial fills, FIFO queue stress
//	CancelStorm - book integrity under mass cancellation, zombie-fill risk
//	Spike - peak throughput, lock contention, queue depth pressure

var regimes = [5]regime{
	{center: 0.05, width: 0.04, limitFrac: 0.90, cancelFrac: 0.10}, // Warmup
	{center: 0.30, width: 0.10, limitFrac: 0.60, cancelFrac: 0.15}, // Normal
	{center: 0.55, width: 0.08, limitFrac: 0.80, cancelFrac: 0.30}, // MarketMaking
	{center: 0.75, width: 0.06, limitFrac: 0.30, cancelFrac: 0.70}, // CancelStorm
	{center: 0.92, width: 0.04, limitFrac: 0.60, cancelFrac: 0.15}, // Spike
}

// RNG
func Generate(seed int64, totalTicks int) []Tick {
	rng := rand.New(rand.NewSource(seed))
	ticks := make([]Tick, totalTicks)

	resting := make([]string, 0, totalTicks/2)
	midPrice := int64(baseMid)
	counter := 0

	for i := 0; i < totalTicks; i++ {
		u1 := rng.Float64()
		u2 := rng.Float64()

		if u1 < 1e-10 {
			u1 = 1e-10
		}

		z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
		midPrice = int64(float64(midPrice) * math.Exp(midSigma*z))

		if midPrice < midFloor {
			midPrice = midFloor
		}

		e1 := rng.Float64()
		e2 := rng.Float64()

		r := rng.Float64()

		limitFrac, cancelFrac := blend(i, totalTicks, e1, e2)

		if r < cancelFrac && len(resting) > 0 {
			idx := rng.Intn(len(resting))
			id := resting[idx]

			resting[idx] = resting[len(resting)-1]
			resting = resting[:len(resting)-1]

			ticks[i] = Tick{Type: TickCancel, OrderID: id}

		} else {
			sideDraw := rng.Float64()
			limitDraw := rng.Float64()
			offsetDraw := rng.Int63n(int64(spreadTicks)*2 + 1)
			qtyDraw := qtyMin + rng.Int63n(qtyMax-qtyMin+1)

			var side byte

			if sideDraw < 0.5 {
				side = 'B'
			} else {
				side = 'S'
			}

			var ordType byte
			var price int64

			if limitDraw < limitFrac {
				ordType = 'L'
				offset := offsetDraw - int64(spreadTicks)
				price = quantize(midPrice + offset*tickSize)
			} else {
				ordType = 'M'
			}

			counter++
			id := fmt.Sprintf("o%d", counter)
			resting = append(resting, id)

			ticks[i] = Tick{
				Type:    TickAdd,
				OrderID: id,
				Side:    side,
				OrdType: ordType,
				Price:   price,
				Qty:     qtyDraw,
			}
		}
	}

	return ticks
}

// blend computes the Gaussian-blended limitFrac and cancelFrac at tick index i.
//
// Each regime contributes a Gaussian weight centred at its position (as a
// fraction of totalTicks). Weights are normalised so the blend is always a
// proper convex combination — this prevents the parameters from collapsing
// toward zero in the tails where all Gaussian bumps are small.
//
// e1 and e2 are Uniform(0,1) draws that add independent ±0.05 noise to each
// parameter after blending. The results are clamped to [0.0, 1.0].
func blend(i, totalTicks int, e1, e2 float64) (limitFrac, cancelFrac float64) {
	x := float64(i) / float64(totalTicks)

	var weights [5]float64
	var wSum float64
	for r := range regimes {
		d := (x - regimes[r].center) / regimes[r].width
		w := math.Exp(-0.5 * d * d)
		weights[r] = w
		wSum += w
	}

	for r := range regimes {
		w := weights[r] / wSum
		limitFrac += w * regimes[r].limitFrac
		cancelFrac += w * regimes[r].cancelFrac
	}

	// Add independent noise: (e*2 - 1) maps Uniform(0,1) → Uniform(-1, 1);
	// scaling by 0.05 gives Uniform(-0.05, 0.05).
	limitFrac = clamp01(limitFrac + (e1*2-1)*0.05)
	cancelFrac = clamp01(cancelFrac + (e2*2-1)*0.05)
	return
}

// quantize rounds price down to the nearest tickSize multiple.
// Returns tickSize if the result would be zero or negative, ensuring
// limit-order prices are always strictly positive.
func quantize(price int64) int64 {
	q := (price / tickSize) * tickSize
	if q < tickSize {
		return tickSize
	}
	return q
}

// clamp01 clamps v to the closed interval [0.0, 1.0].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
