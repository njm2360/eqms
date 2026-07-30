// Package nmea は ingen-seismometer が出力する NMEA 風センテンスを解析する。
package nmea

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Acc は XSACC (フィルタ後加速度 gal, 100Hz)。
type Acc struct {
	X, Y, Z float64
}

// Intensity は XSINT (計測震度相当, 5Hz)。
type Intensity struct {
	Value  float64 // Stable=false のとき NaN
	Stable bool
}

// HWInfo は XSHWI (HWINFO コマンド応答)。
type HWInfo struct {
	InfoVersion int
	Firmware    string
	Device      string
	Sensor      string
	ADC         string
	StepGal     float64
}

// DevErr は XSEER (デバイス内部エラー)。
type DevErr struct {
	ID string
}

// Format はペイロードを $...*XX 形式にする (シミュレータとテスト用)。
func Format(payload string) string {
	var sum byte
	for i := 0; i < len(payload); i++ {
		sum ^= payload[i]
	}
	return fmt.Sprintf("$%s*%02X", payload, sum)
}

// Parse は1行を解析する。無視対象のセンテンス (XSRAW や XSCFG) は (nil, nil) を返す。
func Parse(line string) (any, error) {
	line = strings.TrimSpace(line)
	if len(line) < 4 || line[0] != '$' {
		return nil, fmt.Errorf("nmea: not a sentence: %q", line)
	}
	star := strings.LastIndexByte(line, '*')
	if star < 0 || len(line)-star != 3 {
		return nil, fmt.Errorf("nmea: missing checksum: %q", line)
	}
	payload := line[1:star]
	want, err := strconv.ParseUint(line[star+1:], 16, 8)
	if err != nil {
		return nil, fmt.Errorf("nmea: bad checksum field: %q", line)
	}
	var sum byte
	for i := 0; i < len(payload); i++ {
		sum ^= payload[i]
	}
	if sum != byte(want) {
		return nil, fmt.Errorf("nmea: checksum mismatch (got %02X want %02X): %q", sum, want, line)
	}

	f := strings.Split(payload, ",")
	switch f[0] {
	case "XSACC":
		if len(f) != 4 {
			return nil, fmt.Errorf("nmea: XSACC needs 3 fields: %q", line)
		}
		var v [3]float64
		for i := range 3 {
			v[i], err = strconv.ParseFloat(f[i+1], 64)
			if err != nil {
				return nil, fmt.Errorf("nmea: XSACC field %d: %w", i+1, err)
			}
			// 非有限値を通すと PGA と波形アーカイブが汚染され、JSON にも載らなくなる
			if math.IsNaN(v[i]) || math.IsInf(v[i], 0) {
				return nil, fmt.Errorf("nmea: XSACC field %d: non-finite value %q", i+1, f[i+1])
			}
		}
		return Acc{X: v[0], Y: v[1], Z: v[2]}, nil

	case "XSINT":
		// $XSINT,-1.0,計測震度 (第1フィールドは固定値で無意味)
		if len(f) != 3 {
			return nil, fmt.Errorf("nmea: XSINT needs 2 fields: %q", line)
		}
		v, err := strconv.ParseFloat(f[2], 64)
		if err != nil {
			return nil, fmt.Errorf("nmea: XSINT value: %w", err)
		}
		if math.IsNaN(v) {
			return Intensity{Value: math.NaN(), Stable: false}, nil
		}
		// NaN は「安定前」を表す仕様値だが Inf は単なる異常値
		if math.IsInf(v, 0) {
			return nil, fmt.Errorf("nmea: XSINT value: non-finite %q", f[2])
		}
		return Intensity{Value: v, Stable: true}, nil

	case "XSHWI":
		if len(f) != 7 {
			return nil, fmt.Errorf("nmea: XSHWI needs 6 fields: %q", line)
		}
		ver, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("nmea: XSHWI version: %w", err)
		}
		step, err := strconv.ParseFloat(f[6], 64)
		if err != nil {
			return nil, fmt.Errorf("nmea: XSHWI step: %w", err)
		}
		return HWInfo{InfoVersion: ver, Firmware: f[2], Device: f[3], Sensor: f[4], ADC: f[5], StepGal: step}, nil

	case "XSEER":
		if len(f) != 2 {
			return nil, fmt.Errorf("nmea: XSEER needs 1 field: %q", line)
		}
		return DevErr{ID: f[1]}, nil

	default:
		return nil, nil
	}
}
