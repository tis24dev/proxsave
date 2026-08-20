package backup

import "testing"

func TestHasCustomNotificationObjects(t *testing.T) {
	tests := []struct {
		name    string
		summary pbsNotificationsSummary
		want    bool
	}{
		{
			name: "built-in objects only",
			summary: pbsNotificationsSummary{
				Targets:  pbsNotificationSnapshotSummary{Total: 1, BuiltIn: 1},
				Matchers: pbsNotificationSnapshotSummary{Total: 1, BuiltIn: 1},
				Endpoints: map[string]pbsNotificationSnapshotSummary{
					"sendmail": {Total: 1, BuiltIn: 1},
				},
			},
			want: false,
		},
		{
			name: "custom target",
			summary: pbsNotificationsSummary{
				Targets: pbsNotificationSnapshotSummary{Total: 2, BuiltIn: 1, Custom: 1},
			},
			want: true,
		},
		{
			name: "custom matcher",
			summary: pbsNotificationsSummary{
				Matchers: pbsNotificationSnapshotSummary{Total: 2, BuiltIn: 1, Custom: 1},
			},
			want: true,
		},
		{
			name: "custom endpoint",
			summary: pbsNotificationsSummary{
				Endpoints: map[string]pbsNotificationSnapshotSummary{
					"smtp": {Total: 1, Custom: 1},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasCustomNotificationObjects(tt.summary); got != tt.want {
				t.Fatalf("hasCustomNotificationObjects() = %v, want %v", got, tt.want)
			}
		})
	}
}
