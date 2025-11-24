package orchestrator

import (
	"context"
	"os"
	"testing"
)

func TestCreateDecompressionReaderUnsupported(t *testing.T) {
	f, err := os.CreateTemp("", "archive-*.foo")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if _, err := createDecompressionReader(context.Background(), f, f.Name()); err == nil {
		t.Fatalf("expected error for unsupported extension")
	}
}

func TestCreateDecompressionReaderTar(t *testing.T) {
	f, err := os.CreateTemp("", "archive-*.tar")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	reader, err := createDecompressionReader(context.Background(), f, f.Name())
	if err != nil {
		t.Fatalf("expected tar reader, got %v", err)
	}
	if reader == nil {
		t.Fatalf("reader should not be nil for tar")
	}
}
