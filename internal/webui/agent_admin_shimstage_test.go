package webui

import "testing"

// The staged shim pair is reclaimable only while nothing is mounted over the
// Bose binary. With the wrapper live, lib/SoftwareUpdate-real is the only
// original left, so dropping it would break what the wrapper execs. The mount
// parse is therefore the load-bearing part and is pinned here.
func TestMountActiveIn(t *testing.T) {
	cases := []struct {
		name    string
		mounts  string
		wantHit bool
	}{
		{
			name:    "wrapper live",
			mounts:  "ubi0:rootfs / ubifs ro,relatime 0 0\n/dev/loop0 " + softwareUpdateBinPath + " squashfs ro 0 0\n",
			wantHit: true,
		},
		{
			name: "ordinary box, no shim",
			mounts: "ubi0:rootfs / ubifs ro,relatime 0 0\n" +
				"ubi1:persistent_volume /mnt/nv ubifs rw,relatime 0 0\n",
			wantHit: false,
		},
		{
			// The staged copy itself shares the prefix; matching on prefix
			// instead of the exact mount point would keep the file forever.
			name:    "prefix lookalike",
			mounts:  "/dev/x " + softwareUpdateBinPath + "-real ext4 rw 0 0\n",
			wantHit: false,
		},
		{
			name:    "empty",
			mounts:  "",
			wantHit: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mountActiveIn(c.mounts); got != c.wantHit {
				t.Fatalf("mountActiveIn = %v, want %v", got, c.wantHit)
			}
		})
	}
}
