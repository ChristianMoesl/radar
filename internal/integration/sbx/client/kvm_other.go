//go:build !linux

package client

import "fmt"

func checkKVM() error { return fmt.Errorf("KVM validation requires a Linux host") }
