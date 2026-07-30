package source

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/njm2360/eqms/internal/nmea"
)

// RunSim は実機なしの動作確認用に、周期的な疑似地震を含む 100Hz データを生成する。
func RunSim(ctx context.Context, ch chan<- Event) {
	const (
		stabilizeSec  = 10
		quakePeriod   = 90.0
		quakeDuration = 15.0
	)
	send(ctx, ch, Connected{Port: "(simulator)"})
	send(ctx, ch, Line{Text: nmea.Format("XSHWI,1,eqms-sim;0.0.1,SIM,SIM,SIM,1.0"), Recv: time.Now()})

	start := time.Now()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	n := 0
	envelope := 0.0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			el := now.Sub(start).Seconds()
			phase := math.Mod(el, quakePeriod)

			x := rand.NormFloat64() * 0.05
			y := rand.NormFloat64() * 0.05
			z := rand.NormFloat64() * 0.05
			if el > float64(stabilizeSec)+5 && phase < quakeDuration {
				amp := 120.0 * math.Exp(-phase/6.0) * (1 - math.Exp(-phase*2))
				x += amp * math.Sin(2*math.Pi*2.5*phase)
				y += amp * 0.8 * math.Sin(2*math.Pi*3.1*phase+1.0)
				z += amp * 0.4 * math.Sin(2*math.Pi*5.0*phase+2.0)
			}
			comp := math.Sqrt(x*x + y*y + z*z)
			envelope = math.Max(comp, envelope*0.995)

			if !send(ctx, ch, Line{Text: nmea.Format(fmt.Sprintf("XSACC,%.2f,%.2f,%.2f", x, y, z)), Recv: now}) {
				return
			}

			if n%20 == 0 {
				var val string
				if el < float64(stabilizeSec) {
					val = "nan"
				} else {
					// 計測震度の近似式 I = 2*log10(a) + 0.94
					i := 0.0
					if envelope >= 0.5 {
						i = 2*math.Log10(envelope) + 0.94
					}
					val = fmt.Sprintf("%.1f", i)
				}
				send(ctx, ch, Line{Text: nmea.Format("XSINT,-1.0," + val), Recv: now})
			}
			n++
		}
	}
}
