package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

type newInstallPlan struct {
	ResolvedConfigPath string
	BaseDir            string
	BuildSignature     string
	PreservedEntries   []string
}

var newInstallBuildSignature = buildSignature

func buildNewInstallPlan(configPath string) (newInstallPlan, error) {
	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return newInstallPlan{}, err
	}

	buildSig := strings.TrimSpace(newInstallBuildSignature())
	if buildSig == "" {
		buildSig = "n/a"
	}

	baseDir, _ := detectedBaseDirOrFallback()
	return newInstallPlan{
		ResolvedConfigPath: resolvedPath,
		BaseDir:            baseDir,
		BuildSignature:     buildSig,
		PreservedEntries:   newInstallPreservedEntries(),
	}, nil
}

func newInstallPreservedEntries() []string {
	preserved := []string{"env", "identity", "build"}
	sort.Strings(preserved)
	return preserved
}

func newInstallPreserveSet() map[string]struct{} {
	preserved := newInstallPreservedEntries()
	result := make(map[string]struct{}, len(preserved))
	for _, entry := range preserved {
		result[entry] = struct{}{}
	}
	return result
}

func formatNewInstallPreservedEntries(entries []string) string {
	formatted := make([]string, 0, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		trimmed = strings.TrimRight(trimmed, "/")
		if trimmed == "" {
			continue
		}
		formatted = append(formatted, trimmed+"/")
	}
	if len(formatted) == 0 {
		return "(none)"
	}
	return strings.Join(formatted, " ")
}

func confirmNewInstallCLI(ctx context.Context, reader *bufio.Reader, plan newInstallPlan) (bool, error) {
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}

	fmt.Println()
	fmt.Println("--- New installation reset ---")
	fmt.Printf("Base directory: %s\n", plan.BaseDir)
	fmt.Printf("Build signature: %s\n", plan.BuildSignature)
	fmt.Printf("Preserved entries: %s\n", formatNewInstallPreservedEntries(plan.PreservedEntries))
	fmt.Println("Everything else under the base directory will be removed.")

	return promptYesNo(ctx, reader, "Continue? [y/N]: ", false)
}
