package client

import (
	"fmt"
	"os"
	"syscall"
)

func checkKVM() error { return checkKVMAt("/dev/kvm") }

func checkKVMAt(path string) error {
	device, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("native Linux SBX requires accessible /dev/kvm (enable virtualization and refresh the session's kvm group membership): %w", err)
	}
	defer device.Close()
	version, _, errno := syscall.Syscall(syscall.SYS_IOCTL, device.Fd(), 0xAE00, 0) // KVM_GET_API_VERSION; no VM is allocated.
	if errno != 0 || version != 12 {
		return fmt.Errorf("native Linux SBX requires KVM API 12; got %d: %v", version, errno)
	}
	return nil
}
