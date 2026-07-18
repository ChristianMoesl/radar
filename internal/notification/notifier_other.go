//go:build !darwin

package notification

import "context"

type platformSender struct{}

func newPlatformSender() Sender {
	return platformSender{}
}

func (platformSender) Send(context.Context, string, string) error {
	return nil
}
