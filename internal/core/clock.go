package core

import "time"

// SampleClock は 100Hz サンプルへ時刻を割り当てる。USB 受信時刻はジッタが大きいため
// サンプル番号ベースの理想時刻を使い、アンカーの追従でドリフトのみ吸収する。
type SampleClock struct {
	anchor float64 // ms (unix epoch)
	n      int64
	ok     bool
}

const (
	sampleIntervalMs = 10.0
	// これを超える誤差は不連続とみなしてアンカーを張り直す
	reanchorThresholdMs = 500.0
	driftGain           = 0.001 // 誤差の0.1%/サンプル ≒ 10%/秒
)

func (c *SampleClock) Reset() {
	c.ok = false
}

// Stamp は今回のサンプルの時刻(ms epoch)を返す。
func (c *SampleClock) Stamp(recv time.Time) int64 {
	t := float64(recv.UnixNano()) / 1e6
	if !c.ok {
		c.anchor = t
		c.n = 0
		c.ok = true
	}
	ideal := c.anchor + float64(c.n)*sampleIntervalMs
	err := t - ideal
	if err > reanchorThresholdMs || err < -reanchorThresholdMs {
		c.anchor = t - float64(c.n)*sampleIntervalMs
		ideal = t
	} else {
		c.anchor += err * driftGain
	}
	c.n++
	return int64(ideal)
}
