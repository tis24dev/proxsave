package orchestrator

import (
	"crypto/sha256"
	"fmt"
)

func checksumHexForBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

func checksumLineForBytes(filename string, data []byte) []byte {
	return []byte(fmt.Sprintf("%s  %s", checksumHexForBytes(data), filename))
}
