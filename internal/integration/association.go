package integration

import (
	"context"

	"radar/internal/protocol"
)

type AssociationProvider interface {
	Integration
	NormalizeAssociation(ctx context.Context, value string) (protocol.TaskAssociation, error)
}
