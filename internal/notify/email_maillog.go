package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

// newMessageID returns the Message-ID, without its angle brackets, of one email built
// with the given From address: "proxsave-" and 32 random hex digits, at the From
// address's domain ("localhost" without one). It is how the mail log is searched for
// this very message (findQueueIDByMessageID). Tests replace it to keep their output
// stable.
var newMessageID = func(from string) string {
	var token [16]byte
	// crypto/rand.Read never fails (Go 1.24+): the error is always nil.
	_, _ = rand.Read(token[:])
	return "proxsave-" + hex.EncodeToString(token[:]) + "@" + messageIDDomain(from)
}

// messageIDDomain is the domain of a From header value ("user@domain" or
// "Name <user@domain>"), "localhost" when it has none usable in a Message-ID.
func messageIDDomain(from string) string {
	addr := strings.TrimSpace(from)
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		addr = strings.TrimSuffix(addr[i+1:], ">")
	}
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return "localhost"
	}
	domain := strings.TrimSpace(addr[i+1:])
	if domain == "" || strings.ContainsAny(domain, " \t<>@\"()[]\\,;:") {
		return "localhost"
	}
	return domain
}

// mailLogQueueIDRegex is the form of a postfix or sendmail queue ID in a log line.
var mailLogQueueIDRegex = regexp.MustCompile(`^[A-Za-z0-9]{5,}$`)

// eximLogFlags are the fields exim writes right after a message's ID: arrival,
// deliveries, failure, deferral, completion.
var eximLogFlags = map[string]bool{"<=": true, "=>": true, "->": true, "*>": true, "**": true, "==": true, "Completed": true}

// queueIDForMessageID returns the queue ID a mail log line gives the message with the
// Message-ID messageID (without its angle brackets), empty when the line does not
// record it:
//
//	postfix:  "... postfix/cleanup[123]: <queue>: message-id=<id>"
//	sendmail: "... sm-mta[123]: <queue>: from=<...>, size=..., msgid=<id>, ..."
//	exim:     "... <queue> <= sender ... id=<id>"
func queueIDForMessageID(line, messageID string) string {
	if messageID == "" {
		return ""
	}
	fields := strings.Fields(line)
	for i, field := range fields {
		switch strings.TrimRight(field, ",;") {
		case "message-id=<" + messageID + ">", "msgid=<" + messageID + ">":
			return syslogQueueID(fields[:i])
		case "id=" + messageID, "id=<" + messageID + ">":
			for j := i - 1; j >= 1; j-- {
				if fields[j] == "<=" {
					return fields[j-1]
				}
			}
		}
	}
	return ""
}

// syslogQueueID is the queue ID a postfix or sendmail syslog line opens with: the
// "<queue>:" field right after the "program[pid]:" tag.
func syslogQueueID(fields []string) string {
	for i := 0; i+1 < len(fields); i++ {
		if !strings.HasSuffix(fields[i], "]:") {
			continue
		}
		next := fields[i+1]
		if id := strings.TrimSuffix(next, ":"); id != next && mailLogQueueIDRegex.MatchString(id) {
			return id
		}
		return ""
	}
	return ""
}

// mailLogLineNamesQueueID reports whether the line is one of queueID's: postfix and
// sendmail open it with "<ID>:", exim with "<ID>" followed by one of its flags. A line
// that only mentions the ID is not one of them.
func mailLogLineNamesQueueID(line, queueID string) bool {
	fields := strings.Fields(line)
	for i, field := range fields {
		if field == queueID+":" {
			return true
		}
		if field == queueID && i+1 < len(fields) && eximLogFlags[fields[i+1]] {
			return true
		}
	}
	return false
}

// eximLogFlag is the flag exim wrote after queueID in the line, empty in any other line.
func eximLogFlag(line, queueID string) string {
	fields := strings.Fields(line)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == queueID && eximLogFlags[fields[i+1]] {
			return fields[i+1]
		}
	}
	return ""
}

// mailLogLineStatus is the delivery status a line of the message reports, empty when
// it reports none: postfix "status=", sendmail "stat=", exim's flags, and the
// connection failures any of them logs.
func mailLogLineStatus(line, queueID string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "status=sent"), strings.Contains(lower, "stat=sent"):
		return "sent"
	case strings.Contains(lower, "status=deferred"), strings.Contains(lower, "stat=deferred"):
		return "deferred"
	case strings.Contains(lower, "status=bounced"), strings.Contains(lower, "status=softbounce"):
		return "bounced"
	case strings.Contains(lower, "status=expired"):
		return "expired"
	case strings.Contains(lower, "status=rejected") || strings.Contains(lower, "rejected "):
		return "rejected"
	case strings.Contains(lower, "stat=") && strings.Contains(lower, "dsn=5."):
		// sendmail: a permanent failure carries its reason in stat= and a 5.x.x DSN.
		return "bounced"
	}
	switch eximLogFlag(line, queueID) {
	case "=>", "->", "*>":
		return "sent"
	case "**":
		return "bounced"
	case "==":
		return "deferred"
	}
	switch {
	case strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "host not found"),
		strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "timeout"):
		return "error"
	}
	return ""
}

// findQueueIDByMessageID searches the recent mail log for the queue ID of the email
// with the Message-ID messageID (without its angle brackets). The last line that
// records it wins: a later hop of the same message (a local alias resubmitting it,
// sendmail's MTA queue after its submission queue) is where it went next. readable is
// false when no mail log could be read at all.
func (e *EmailNotifier) findQueueIDByMessageID(ctx context.Context, messageID string) (queueID string, readable bool) {
	lines, source := e.tailMailLog(ctx, 80)
	if source == "" {
		return "", false
	}
	read := 0
	var matched string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		read++
		if id := queueIDForMessageID(line, messageID); id != "" {
			queueID, matched = id, line
		}
	}
	e.logger.Debug("mail log: source=%s lines=%d message_id=<%s>", source, read, messageID)
	if queueID == "" {
		e.logger.Debug("mail log: message-id not found in %d lines", read)
		return "", true
	}
	e.logger.Debug("mail log: message-id found queue_id=%s line=%s", queueID, matched)
	return queueID, true
}

// logQueueStatusDebug writes what the mail log says of the queue ID's status.
func (e *EmailNotifier) logQueueStatusDebug(queueID, status, matchedLine string) {
	if status == "" || status == "unknown" {
		e.logger.Debug("mail log: queue_id=%s status not logged yet", queueID)
		return
	}
	e.logger.Debug("mail log: queue_id=%s status=%s line=%s", queueID, status, matchedLine)
}

// mailQueueEntry is one message of the mail queue with a line naming the recipient.
type mailQueueEntry struct {
	id   string
	line string
}

// parseMailQueue reads mailq output: the count checkMailQueue reports (lines with an
// "@", the header and footer left out) and the entries with a line that names the
// recipient, in queue order, each once.
func parseMailQueue(output, recipient string) (count int, entries []mailQueueEntry) {
	if strings.Contains(output, "Mail queue is empty") {
		return 0, nil
	}
	lowerRecipient := strings.ToLower(strings.TrimSpace(recipient))
	var currentID string
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if len(line) > 10 && strings.Contains(line, "@") && !strings.Contains(line, "Mail queue") && !strings.Contains(line, "Total requests") {
			count++
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) > 0 && mailQueueIDLineRegex.MatchString(fields[0]) {
			currentID = strings.TrimSuffix(strings.TrimSuffix(fields[0], "*"), "!")
			continue
		}
		if currentID != "" && lowerRecipient != "" && !seen[currentID] && strings.Contains(strings.ToLower(trimmed), lowerRecipient) {
			seen[currentID] = true
			entries = append(entries, mailQueueEntry{id: currentID, line: trimmed})
		}
	}
	return count, entries
}

// newQueueEntryFor is the mail queue's answer when the mail log has no line of this
// email yet: the message has not been processed, so no notice about it can be queued,
// and the one entry for the recipient that was not in the queue before sending is this
// email. More than one, or none, and no ID is given; nor without the listing before.
func (e *EmailNotifier) newQueueEntryFor(ctx context.Context, recipient string, before map[string]bool) string {
	e.logger.Debug("Sendmail did not report a queue ID; attempting to detect from mail queue output")
	if before == nil {
		e.logger.Debug("Mail queue was not listed before sending; no queue entry is attributed to %s", recipient)
		return ""
	}
	_, entries, err := e.checkMailQueueFor(ctx, recipient)
	if err != nil {
		e.logger.Debug("Unable to inspect mail queue entries for %s: %v", recipient, err)
		return ""
	}
	var fresh []mailQueueEntry
	for _, entry := range entries {
		if !before[entry.id] {
			fresh = append(fresh, entry)
		}
	}
	switch len(fresh) {
	case 0:
		e.logger.Debug("No matching mail queue entry found for %s immediately after sending", recipient)
		return ""
	case 1:
	default:
		e.logger.Debug("Mail queue holds %d new entries for %s; none is attributed to this email", len(fresh), recipient)
		return ""
	}
	e.logger.Info("Detected queue ID %s for %s by inspecting mail queue output", fresh[0].id, recipient)
	e.logger.Debug("Mail queue entry: %s", fresh[0].line)
	return fresh[0].id
}
