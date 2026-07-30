package nmea

import (
	"math"
	"testing"
)

func TestParseAcc(t *testing.T) {
	line := Format("XSACC,1.23,-4.56,0.01")
	v, err := Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	acc, ok := v.(Acc)
	if !ok {
		t.Fatalf("want Acc, got %T", v)
	}
	if acc.X != 1.23 || acc.Y != -4.56 || acc.Z != 0.01 {
		t.Fatalf("bad values: %+v", acc)
	}
}

func TestParseIntensity(t *testing.T) {
	v, err := Parse(Format("XSINT,-1.0,3.4"))
	if err != nil {
		t.Fatal(err)
	}
	in := v.(Intensity)
	if !in.Stable || in.Value != 3.4 {
		t.Fatalf("bad: %+v", in)
	}

	v, err = Parse(Format("XSINT,-1.0,nan"))
	if err != nil {
		t.Fatal(err)
	}
	in = v.(Intensity)
	if in.Stable || !math.IsNaN(in.Value) {
		t.Fatalf("nan should be unstable: %+v", in)
	}
}

func TestParseHWInfo(t *testing.T) {
	v, err := Parse(Format("XSHWI,1,ingen-seismometer;0.2.1,PiDAS,KXR94-2050,MCP3204,1.197393"))
	if err != nil {
		t.Fatal(err)
	}
	hw := v.(HWInfo)
	if hw.InfoVersion != 1 || hw.Device != "PiDAS" || hw.ADC != "MCP3204" {
		t.Fatalf("bad: %+v", hw)
	}
}

func TestParseIgnored(t *testing.T) {
	v, err := Parse(Format("XSRAW,100,200,300"))
	if err != nil || v != nil {
		t.Fatalf("XSRAW should be ignored: %v %v", v, err)
	}
}

func TestChecksumMismatch(t *testing.T) {
	if _, err := Parse("$XSACC,1,2,3*00"); err == nil {
		t.Fatal("want checksum error")
	}
}

func TestGarbage(t *testing.T) {
	for _, s := range []string{"", "$", "XSACC,1,2,3", "$XSACC,1,2,3", "$XSACC,a,b,c*7F"} {
		if v, err := Parse(s); err == nil && v != nil {
			t.Fatalf("want error for %q", s)
		}
	}
}

// Inf を通すと PGA と震度が汚染され JSON にも載らなくなるので、パースエラーにすること。
func TestParseRejectsNonFinite(t *testing.T) {
	for _, line := range []string{
		"XSACC,inf,0.00,0.00",
		"XSACC,0.00,-Inf,0.00",
		"XSACC,0.00,0.00,nan",
		"XSINT,-1.0,inf",
	} {
		if v, err := Parse(Format(line)); err == nil {
			t.Errorf("accepted %q: %#v", line, v)
		}
	}
	// NaN の震度は「安定前」を表す仕様値なので通ること
	v, err := Parse(Format("XSINT,-1.0,nan"))
	if err != nil {
		t.Fatal(err)
	}
	if in := v.(Intensity); in.Stable || !math.IsNaN(in.Value) {
		t.Fatalf("NaN was not treated as not-yet-stable: %+v", in)
	}
}
