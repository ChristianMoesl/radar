package git

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestGitStatxIdentityRequiresBirthTime(t *testing.T) {
	for _, mask := range []uint32{0, unix.STATX_INO, unix.STATX_BTIME} {
		if key, err := gitStatxIdentity(unix.Statx_t{Mask: mask, Ino: 42}); key != "" || err == nil {
			t.Fatalf("incomplete filesystem lifetime identity: key=%q error=%v", key, err)
		}
	}
}

func TestGitStatxIdentityDistinguishesReusedInode(t *testing.T) {
	original := unix.Statx_t{
		Mask: unix.STATX_INO | unix.STATX_BTIME, Dev_major: 8, Dev_minor: 1, Ino: 42,
		Btime: unix.StatxTimestamp{Sec: 1700000000, Nsec: 123456789},
	}
	first, err := gitStatxIdentity(original)
	if err != nil {
		t.Fatal(err)
	}
	activity := original
	activity.Ctime = unix.StatxTimestamp{Sec: 1700000100}
	activity.Mtime = unix.StatxTimestamp{Sec: 1700000100}
	if key, err := gitStatxIdentity(activity); err != nil || key != first {
		t.Fatalf("mutable timestamps changed binding: first=%q key=%q error=%v", first, key, err)
	}
	for _, change := range []struct {
		name string
		edit func(*unix.Statx_t)
	}{
		{"device", func(s *unix.Statx_t) { s.Dev_minor++ }},
		{"inode", func(s *unix.Statx_t) { s.Ino++ }},
		{"birth-second", func(s *unix.Statx_t) { s.Btime.Sec++ }},
		{"birth-nanosecond", func(s *unix.Statx_t) { s.Btime.Nsec++ }},
	} {
		t.Run(change.name, func(t *testing.T) {
			replacement := original
			change.edit(&replacement)
			if key, err := gitStatxIdentity(replacement); err != nil || key == first {
				t.Fatalf("reused filesystem identity inherited old binding: first=%q key=%q error=%v", first, key, err)
			}
		})
	}
}
