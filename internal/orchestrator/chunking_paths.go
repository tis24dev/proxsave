package orchestrator

import "strings"

func originalPathFromChunk(relPath string) (string, bool) {
	if !strings.HasSuffix(relPath, ".chunk") {
		return "", false
	}
	withoutSuffix := strings.TrimSuffix(relPath, ".chunk")
	dot := strings.LastIndexByte(withoutSuffix, '.')
	if dot < 0 {
		return "", false
	}
	idx := withoutSuffix[dot+1:]
	if idx == "" {
		return "", false
	}
	for i := 0; i < len(idx); i++ {
		if idx[i] < '0' || idx[i] > '9' {
			return "", false
		}
	}
	original := withoutSuffix[:dot]
	if original == "" {
		return "", false
	}
	return original, true
}
