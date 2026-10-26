package openurl

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLaunchFixedOSHandler(t *testing.T) {
	for _, platform := range []struct{ goos, name string }{{"darwin", "open"}, {"linux", "xdg-open"}} {
		t.Run(platform.goos, func(t *testing.T) {
			called := false
			err := launch(context.Background(), "https://github.com/acme/app/pull/123/files", platform.goos, func(ctx context.Context, name string, args ...string) error {
				called = true
				if name != platform.name || !reflect.DeepEqual(args, []string{"https://github.com/acme/app/pull/123/files"}) {
					t.Fatalf("%s %#v", name, args)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second {
					t.Fatal("unbounded launcher")
				}
				return nil
			})
			if err != nil || !called {
				t.Fatalf("%v %v", called, err)
			}
		})
	}
}

func TestLaunchRejectsUnsupportedDestinationsBeforeExecution(t *testing.T) {
	for _, value := range []string{"file:///tmp/page.html", "javascript:evil()", "data:text/html,hi", "https://user:pass@github.com/acme/app", "http://github.com/acme/app", "https://github.com:443/acme/app", "https://github.com/acme/app?x=y", "https://github.com/acme/app#x", "https://github.com/acme/app\n", "https://github.com/acme/a b", "https://"} {
		called := false
		err := launch(context.Background(), value, "darwin", func(context.Context, string, ...string) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("%q: %v %v", value, called, err)
		}
	}
	sentinel := errors.New("missing launcher")
	if err := launch(context.Background(), "https://github.com/acme/app", "linux", func(context.Context, string, ...string) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := launch(context.Background(), "https://github.com/acme/app", "unsupported", func(context.Context, string, ...string) error { t.Fatal("unexpected launch"); return nil }); err == nil {
		t.Fatal("accepted platform")
	}
}
