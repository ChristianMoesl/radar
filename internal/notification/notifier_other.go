//go:build !darwin

package notification

func newPlatformSender() Sender {
	return nil
}
