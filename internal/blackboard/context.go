package blackboard

import "context"

type boardIDKey struct{}

// WithBoardID returns a new context carrying the given boardID.
func WithBoardID(ctx context.Context, boardID string) context.Context {
	return context.WithValue(ctx, boardIDKey{}, boardID)
}

// BoardIDFrom extracts the boardID from ctx. Returns "" if not set.
func BoardIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(boardIDKey{}).(string)
	return v
}
