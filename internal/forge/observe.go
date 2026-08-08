package forge

import "context"

type RequestEvent struct {
	Kind    ForgeKind
	Method  string
	Attempt int
	Status  int
	Retry   bool
}

type requestObserverKey struct{}

func WithRequestObserver(ctx context.Context, observer func(RequestEvent)) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, requestObserverKey{}, observer)
}

func observeRequest(ctx context.Context, event RequestEvent) {
	if observer, ok := ctx.Value(requestObserverKey{}).(func(RequestEvent)); ok {
		observer(event)
	}
}
