package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gew/internal/forge"
)

type syncOptions struct {
	Progress string
	Timings  bool
}

type syncObserver struct {
	mu        sync.Mutex
	out       io.Writer
	progress  bool
	timings   bool
	provider  ForgeKind
	started   map[string]time.Time
	durations map[string]time.Duration
	order     []string
	requests  int64
	retries   int64
	files     int64
	bytes     int64
	finished  bool
}

func newSyncObserver(output io.Writer, options syncOptions) (*syncObserver, error) {
	mode := strings.TrimSpace(options.Progress)
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "always" && mode != "never" {
		return nil, fmt.Errorf("progress must be auto, always, or never")
	}
	show := mode == "always" || (mode == "auto" && writerIsTerminal(output))
	return &syncObserver{out: output, progress: show, timings: options.Timings, started: make(map[string]time.Time), durations: make(map[string]time.Duration)}, nil
}

func writerIsTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (a app) withSync(options syncOptions) (app, error) {
	observer, err := newSyncObserver(a.errOut, options)
	if err != nil {
		return a, err
	}
	a.sync = observer
	return a, nil
}

func (o *syncObserver) context(ctx context.Context) context.Context {
	if o == nil {
		return ctx
	}
	return forge.WithRequestObserver(ctx, o.request)
}

func (o *syncObserver) request(event forge.RequestEvent) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests++
	if event.Kind != "" {
		o.provider = event.Kind
	}
	if event.Retry {
		o.retries++
		if o.progress {
			fmt.Fprintf(o.out, "retry %s request (attempt %d)\n", event.Method, event.Attempt+1)
		}
	}
}

func (o *syncObserver) phase(name string) func() {
	if o == nil {
		return func() {}
	}
	name = strings.TrimSpace(name)
	o.mu.Lock()
	if _, exists := o.started[name]; !exists {
		o.started[name] = time.Now()
		o.order = append(o.order, name)
		if o.progress {
			fmt.Fprintf(o.out, "%s...\n", name)
		}
	}
	o.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			if start, exists := o.started[name]; exists {
				o.durations[name] += time.Since(start)
				delete(o.started, name)
			}
			o.mu.Unlock()
		})
	}
}

func (o *syncObserver) add(files, bytes int64) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.files += files
	o.bytes += bytes
	o.mu.Unlock()
}

func (o *syncObserver) finish() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return nil
	}
	o.finished = true
	if !o.timings {
		return nil
	}
	provider := o.provider
	if provider == "" {
		provider = "unknown"
	}
	fmt.Fprintf(o.out, "Sync timings: provider=%s requests=%d retries=%d files=%d bytes=%d\n", provider, o.requests, o.retries, o.files, o.bytes)
	seen := make(map[string]struct{}, len(o.order))
	ordered := make([]string, 0, len(o.order))
	for _, name := range o.order {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			ordered = append(ordered, name)
		}
	}
	// Unknown/custom phases remain deterministic even if a future caller did
	// not explicitly start them through phase.
	extra := make([]string, 0)
	for name := range o.durations {
		if _, ok := seen[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	ordered = append(ordered, extra...)
	for _, name := range ordered {
		fmt.Fprintf(o.out, "  %s=%s\n", name, o.durations[name].Round(time.Microsecond))
	}
	return nil
}

func finishSync(observer *syncObserver, primary *error) {
	if observer == nil {
		return
	}
	*primary = errors.Join(*primary, observer.finish())
}
