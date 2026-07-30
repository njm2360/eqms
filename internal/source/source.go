// Package source はエンジンへ NMEA 行を供給するデータソース(シリアル/シミュレータ)。
package source

import "time"

type Event any

type Connected struct {
	Port string
}

type Disconnected struct {
	Err string
}

type Line struct {
	Text string
	Recv time.Time
}
