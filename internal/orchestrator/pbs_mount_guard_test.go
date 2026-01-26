package orchestrator

import "testing"

func TestPBSMountGuardRootForDatastorePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "mnt nested", in: "/mnt/datastore/Data1", want: "/mnt/datastore"},
		{name: "mnt deep", in: "/mnt/Synology_NFS/PBS_Backup", want: "/mnt/Synology_NFS"},
		{name: "media", in: "/media/USB/PBS", want: "/media/USB"},
		{name: "run media", in: "/run/media/root/USB/PBS", want: "/run/media/root/USB"},
		{name: "not mount style", in: "/srv/pbs", want: ""},
		{name: "empty", in: "", want: ""},
		{name: "root", in: "/", want: ""},
		{name: "mnt root", in: "/mnt", want: ""},
		{name: "mnt slash", in: "/mnt/", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := pbsMountGuardRootForDatastorePath(tt.in); got != tt.want {
				t.Fatalf("pbsMountGuardRootForDatastorePath(%q)=%q want %q", tt.in, got, tt.want)
			}
		})
	}
}
