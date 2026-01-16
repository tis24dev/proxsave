package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/input"
)

func promptYesNo(ctx context.Context, reader *bufio.Reader, prompt string) (bool, error) {
	fmt.Print(prompt)
	line, err := input.ReadLineWithContext(ctx, reader)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
