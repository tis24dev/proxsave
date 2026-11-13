package notify

import "testing"

// Postfix prints the sender on the queue-ID line and each recipient on its own line, so
// counting every line with an "@" gave 7 for 3 queued messages (measured on pve-test).
// The count is one per queue-ID line; output without one (exim) keeps the line count.
func TestParseMailQueueCountsMessagesNotAddressLines(t *testing.T) {
	postfix := `-Queue ID-  --Size-- ----Arrival Time---- -Sender/Recipient-------
C65F548FA0      612 Mon Nov  3 10:01:02  root@pve.lan.local
(connect to 192.0.2.1[192.0.2.1]:25: Connection timed out)
                                         a@example.invalid

CDB2A48FA3      612 Mon Nov  3 10:01:03  root@pve.lan.local
(connect to 192.0.2.1[192.0.2.1]:25: Connection timed out)
                                         b@example.invalid

BE71448FA1*     640 Mon Nov  3 10:01:04  root@pve.lan.local
                                         c1@example.invalid
                                         c2@example.invalid

-- 0 Kbytes in 3 Requests.
`
	if count, _ := parseMailQueue(postfix, ""); count != 3 {
		t.Fatalf("postfix: count = %d, want 3", count)
	}
	exim := `25m  1.2K 1qABCD-000123-AB <root@pve.lan.local>
          a@example.invalid

`
	if count, _ := parseMailQueue(exim, ""); count != 2 {
		t.Fatalf("exim (no queue-ID line): count = %d, want the address-line count 2", count)
	}
}

// sendmail ends its listing with "Total requests: N": its first field, "Total", reads as a
// queue ID, so one queued message counted as two.
func TestParseMailQueueSkipsTheSendmailFooter(t *testing.T) {
	sendmail := `		/var/spool/mqueue (1 request)
-----Q-ID----- --Size-- -----Q-Time----- ------------Sender/Recipient-----------
5AD3H1sK012345      612 Mon Nov  3 10:01 <root@pve.lan.local>
                 (Deferred: Connection timed out with example.invalid.)
					 <a@example.invalid>
		Total requests: 1
`
	count, entries := parseMailQueue(sendmail, "a@example.invalid")
	if count != 1 {
		t.Fatalf("sendmail: count = %d, want 1", count)
	}
	if len(entries) != 1 || entries[0].id != "5AD3H1sK012345" {
		t.Fatalf("sendmail: entries = %+v, want the one message with its real queue ID", entries)
	}
}
