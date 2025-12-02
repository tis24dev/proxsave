package orchestrator

import "testing"

func TestZeroBytesClearsSlice(t *testing.T) {
	data := []byte{1, 2, 3, 4}
	zeroBytes(data)
	for i, b := range data {
		if b != 0 {
			t.Fatalf("byte %d not zeroed: %d", i, b)
		}
	}
	zeroBytes(nil) // should not panic
}

func TestResetStringClearsValue(t *testing.T) {
	value := "secret"
	resetString(&value)
	if value != "" {
		t.Fatalf("expected string to be reset, got %q", value)
	}
	resetString(nil) // should not panic
}
