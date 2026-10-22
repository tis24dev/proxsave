package backup

import (
	"errors"
	"io"
	"testing"
	"time"
)

// When the compressor fails to start, the drain unblocks the tar writer by closing the
// pipe with the start error, and returns only once the writer has exited.
func TestDrainTarWriterWaitsForTheWriterToExit(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	startErr := errors.New("failed to start fake")

	release := make(chan struct{})
	writeErr := make(chan error, 1)
	errChan := make(chan error, 1)
	go func() {
		defer close(errChan)
		// Nobody reads pr: this write blocks until the drain closes the pipe.
		_, err := pw.Write([]byte("tar bytes"))
		writeErr <- err
		errChan <- err
		<-release
	}()

	returned := make(chan struct{})
	go func() {
		drainTarWriterAfterCompressorStartFailure(pw, errChan, startErr)
		close(returned)
	}()

	select {
	case err := <-writeErr:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("writer error = %v, want io.ErrClosedPipe once the pipe is closed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the writer is still blocked: the drain did not close the pipe")
	}

	select {
	case <-returned:
		t.Fatal("the drain returned before the writer exited")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("the drain did not return after the writer exited")
	}
}
