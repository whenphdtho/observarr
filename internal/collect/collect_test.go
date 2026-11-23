package collect

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// none is a Collector with no sources; it proves the contract can be implemented as written.
type none struct{}

func (none) Host(context.Context) (Host, error)              { return Host{}, ErrUnavailable }
func (none) Containers(context.Context) ([]Container, error) { return nil, ErrUnavailable }
func (none) Usage(context.Context, []Container) ([]ContainerUsage, error) {
	return nil, ErrUnavailable
}
func (none) Events(context.Context) (<-chan Event, error) { return nil, ErrUnavailable }

var _ Collector = none{}

func TestErrUnavailableWraps(t *testing.T) {
	_, err := none{}.Host(context.Background())
	if !errors.Is(fmt.Errorf("host: %w", err), ErrUnavailable) {
		t.Fatal("ErrUnavailable must survive wrapping")
	}
}
