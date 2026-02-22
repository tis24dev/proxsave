package backup

import "strings"

type privilegeSensitiveMatch struct {
	Reason string
	Match  string
}

func isPrivilegeSensitiveCommand(command string) bool {
	switch strings.TrimSpace(command) {
	case "dmidecode", "blkid", "sensors", "smartctl":
		return true
	default:
		return false
	}
}

func privilegeSensitiveFailureMatch(command string, exitCode int, outputText string) privilegeSensitiveMatch {
	command = strings.TrimSpace(command)
	if command == "" {
		return privilegeSensitiveMatch{}
	}

	outputText = strings.TrimSpace(outputText)
	lower := strings.ToLower(outputText)

	switch command {
	case "dmidecode":
		// dmidecode typically fails due to restricted access to DMI tables (/sys/firmware/dmi or /dev/mem).
		switch {
		case strings.Contains(lower, "/dev/mem"):
			return privilegeSensitiveMatch{Reason: "DMI tables not accessible", Match: "stderr contains /dev/mem"}
		case strings.Contains(lower, "permission denied"):
			return privilegeSensitiveMatch{Reason: "DMI tables not accessible", Match: "stderr contains permission denied"}
		case strings.Contains(lower, "operation not permitted"):
			return privilegeSensitiveMatch{Reason: "DMI tables not accessible", Match: "stderr contains operation not permitted"}
		case exitCode != 0 && outputText == "":
			return privilegeSensitiveMatch{Reason: "DMI tables not accessible", Match: "exit!=0 and empty output"}
		}
		return privilegeSensitiveMatch{}
	case "blkid":
		// In unprivileged LXC, blkid often exits 2 with empty output when block devices are not accessible.
		const blkidReason = "block devices not accessible (restore hint: fstab remap may be limited)"
		switch {
		case exitCode == 2 && outputText == "":
			return privilegeSensitiveMatch{
				Reason: blkidReason,
				Match:  "exit=2 and empty output",
			}
		case strings.Contains(lower, "permission denied"):
			return privilegeSensitiveMatch{
				Reason: blkidReason,
				Match:  "stderr contains permission denied",
			}
		case strings.Contains(lower, "operation not permitted"):
			return privilegeSensitiveMatch{
				Reason: blkidReason,
				Match:  "stderr contains operation not permitted",
			}
		}
		return privilegeSensitiveMatch{}
	case "sensors":
		// In containers, sensors may not be available or may report no sensors found.
		switch {
		case strings.Contains(lower, "permission denied"):
			return privilegeSensitiveMatch{Reason: "hardware sensors not accessible", Match: "stderr contains permission denied"}
		case strings.Contains(lower, "operation not permitted"):
			return privilegeSensitiveMatch{Reason: "hardware sensors not accessible", Match: "stderr contains operation not permitted"}
		case strings.Contains(lower, "no sensors found"):
			return privilegeSensitiveMatch{Reason: "hardware sensors not accessible", Match: "stderr contains no sensors found"}
		}
		return privilegeSensitiveMatch{}
	case "smartctl":
		switch {
		case strings.Contains(lower, "permission denied"):
			return privilegeSensitiveMatch{Reason: "SMART devices not accessible", Match: "stderr contains permission denied"}
		case strings.Contains(lower, "operation not permitted"):
			return privilegeSensitiveMatch{Reason: "SMART devices not accessible", Match: "stderr contains operation not permitted"}
		}
		return privilegeSensitiveMatch{}
	default:
		return privilegeSensitiveMatch{}
	}
}

func privilegeSensitiveFailureReason(command string, exitCode int, outputText string) string {
	return privilegeSensitiveFailureMatch(command, exitCode, outputText).Reason
}
