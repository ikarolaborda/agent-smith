package llamacpp

import "testing"

func TestParseByteLimitDistinguishesUnlimitedAndInvalid(t *testing.T) {
	value, unlimited, err := parseByteLimit([]byte("max\n"), true)
	if err != nil || value != 0 || !unlimited {
		t.Fatalf("max = value %d unlimited %v err %v", value, unlimited, err)
	}
	value, unlimited, err = parseByteLimit([]byte("1048576\n"), true)
	if err != nil || value != 1048576 || unlimited {
		t.Fatalf("numeric = value %d unlimited %v err %v", value, unlimited, err)
	}
	value, unlimited, err = parseByteLimit([]byte("0\n"), true)
	if err != nil || value != 0 || unlimited {
		t.Fatalf("zero finite limit = value %d unlimited %v err %v", value, unlimited, err)
	}
	for _, raw := range []string{"", "max", "not-a-number", "-1"} {
		allowMax := raw != "max"
		if _, _, err := parseByteLimit([]byte(raw), allowMax); err == nil {
			t.Fatalf("parseByteLimit(%q, %v) accepted invalid input", raw, allowMax)
		}
	}
}
