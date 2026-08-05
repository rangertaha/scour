// SPDX-License-Identifier: GPL-3.0-or-later

package plugin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/plugin"
)

// The stage under test carries strings, because what a chain carries is the
// stage's business and none of this package's.
type (
	wrapper  = chain.Wrapper[string, string]
	handler  = chain.Handler[string, string]
	registry = plugin.Registry[string, string]
)

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
` + blocks + `
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

// noting returns a middleware that appends its name as it passes through, so a
// built chain can be asked what order it runs in.
func noting(name string, log *[]string) wrapper {
	return func(next handler) handler {
		return chain.Func[string, string](func(ctx context.Context, in string) (string, error) {
			*log = append(*log, name)
			return next.Handle(ctx, in)
		})
	}
}

func core() handler {
	return chain.Func[string, string](func(_ context.Context, in string) (string, error) {
		return in, nil
	})
}

func TestBuildsInCataloguedOrder(t *testing.T) {
	var log []string

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(context.Context, plugin.Config) (wrapper, error) {
		return noting("cache", &log), nil
	})
	reg.Register("offsite", func(context.Context, plugin.Config) (wrapper, error) {
		return noting("offsite", &log), nil
	})

	j := job(t, `
  downloader {
    plugin "cache" {}
    plugin "offsite" {}
  }
`)

	built, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer built.Close()

	if len(built.Links) != 2 {
		t.Fatalf("built %d links", len(built.Links))
	}
	if got := strings.Join(built.Names(), " "); got != "offsite cache" {
		t.Errorf("Names() = %q, want the way-out order", got)
	}

	// The catalogue puts offsite at 500 and cache at 900, and the chain has to
	// come out in that order however the document listed them.
	h := built.Handler(core())
	if _, err := h.Handle(context.Background(), "page"); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := strings.Join(log, " "); got != "offsite cache" {
		t.Errorf("ran %q, want offsite before cache", got)
	}
}

func TestExplicitOrderWins(t *testing.T) {
	var log []string

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	for _, name := range []string{"cache", "offsite"} {
		reg.Register(name, func(_ context.Context, cfg plugin.Config) (wrapper, error) {
			return noting(cfg.Name, &log), nil
		})
	}

	j := job(t, `
  downloader {
    plugin "cache" {
      order = 10
    }

    plugin "offsite" {}
  }
`)

	built, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer built.Close()

	h := built.Handler(core())
	if _, err := h.Handle(context.Background(), "page"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, " "); got != "cache offsite" {
		t.Errorf("ran %q, want the explicit order to win", got)
	}
}

// TestUnregisteredIsRefusedHere is the whole point of this package. Validation
// runs offline and cannot know what a node has compiled in; building the chain
// can, and this is where a job asking for something that does not exist stops.
func TestUnregisteredIsRefusedHere(t *testing.T) {
	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(context.Context, plugin.Config) (wrapper, error) {
		return noting("cache", nil), nil
	})

	j := job(t, `
  downloader {
    plugin "cache" {}

    plugin "somebody-elses" {
      order = 42
    }
  }
`)

	// The document itself is fine: it validated above.
	_, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("built a chain containing something nothing implements")
	}
	for _, want := range []string{"news", "somebody-elses", "downloader", "cache"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// TestEveryMissingPluginAtOnce: a job loading six on a node that has four
// should be told which two, not sent round the loop twice.
func TestEveryMissingPluginAtOnce(t *testing.T) {
	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(context.Context, plugin.Config) (wrapper, error) {
		return noting("cache", nil), nil
	})

	j := job(t, `
  downloader {
    plugin "cache" {}

    plugin "first-missing" {
      order = 10
    }

    plugin "second-missing" {
      order = 20
    }
  }
`)

	_, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("built it anyway")
	}
	for _, want := range []string{"first-missing", "second-missing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("only some were reported: %v", err)
		}
	}
}

// TestEmptyRegistrySaysWhy: nothing registered at all is almost always a
// missing side-effect import rather than six typos.
func TestEmptyRegistrySaysWhy(t *testing.T) {
	reg := plugin.NewRegistry[string, string](engine.StageDownloader)

	j := job(t, `
  downloader {
    plugin "cache" {}
  }
`)

	_, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("built a chain from an empty registry")
	}
	if !strings.Contains(err.Error(), "import") {
		t.Errorf("the error does not suggest the likely cause: %v", err)
	}
}

func TestAFactoryThatFailsNamesItself(t *testing.T) {
	boom := errors.New("no bucket configured")

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(context.Context, plugin.Config) (wrapper, error) {
		return nil, boom
	})

	j := job(t, `
  downloader {
    plugin "cache" {}
  }
`)

	_, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("a plugin that would not build produced a chain")
	}
	if !strings.Contains(err.Error(), "cache") || !strings.Contains(err.Error(), "no bucket") {
		t.Errorf("the error lost either the plugin or the reason: %v", err)
	}
}

// TestDisabledIsNeverBuilt: turning a plugin off keeps its configuration, so
// the seam must not try to construct it and must not need it to be registered.
func TestDisabledIsNeverBuilt(t *testing.T) {
	constructed := map[string]bool{}

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(_ context.Context, cfg plugin.Config) (wrapper, error) {
		constructed[cfg.Name] = true
		return noting(cfg.Name, nil), nil
	})

	j := job(t, `
  downloader {
    plugin "cache" {}

    plugin "somebody-elses" {
      enabled = false
      order   = 42
    }
  }
`)

	built, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err != nil {
		t.Fatalf("a disabled plugin nothing implements refused the chain: %v", err)
	}
	defer built.Close()

	if len(built.Links) != 1 {
		t.Errorf("built %d links, want only the enabled one", len(built.Links))
	}
	if constructed["somebody-elses"] {
		t.Error("a disabled plugin was constructed")
	}
}

func TestNoPluginsIsNoChain(t *testing.T) {
	reg := plugin.NewRegistry[string, string](engine.StageDownloader)

	built, err := plugin.Build(context.Background(), reg, job(t, ""), engine.StageDownloader, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(built.Links) != 0 {
		t.Errorf("built %d links for a job that listed none", len(built.Links))
	}
	// Still a chain, so a caller may close it and run a core through it without
	// checking whether the job configured anything.
	if err := built.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if _, err := built.Handler(core()).Handle(context.Background(), "page"); err != nil {
		t.Errorf("handle: %v", err)
	}
}

// TestTheBodyArrivesUndecoded is what makes a plugin something somebody else
// can write: this package never learns what "bucket" means.
func TestTheBodyArrivesUndecoded(t *testing.T) {
	type cacheConfig struct {
		Backend string   `hcl:"backend,optional"`
		Bucket  string   `hcl:"bucket,optional"`
		Extra   hcl.Body `hcl:",remain"`
	}
	var got cacheConfig

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(_ context.Context, cfg plugin.Config) (wrapper, error) {
		if err := cfg.Decode(&got); err != nil {
			return nil, err
		}
		return noting(cfg.Name, nil), nil
	})

	j := job(t, `
  downloader {
    plugin "cache" {
      backend = "s3"
      bucket  = "pages"
    }
  }
`)

	if _, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.Backend != "s3" || got.Bucket != "pages" {
		t.Errorf("the plugin read %+v", got)
	}
}

// TestABadFieldGetsAPosition: the plugin decodes its own body, so a field it
// does not recognise is a diagnostic with a line and a column rather than a
// value silently ignored.
func TestABadFieldGetsAPosition(t *testing.T) {
	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(_ context.Context, cfg plugin.Config) (wrapper, error) {
		var into struct {
			Backend string `hcl:"backend,optional"`
		}
		if err := cfg.Decode(&into); err != nil {
			return nil, err
		}
		return noting(cfg.Name, nil), nil
	})

	j := job(t, `
  downloader {
    plugin "cache" {
      backend  = "s3"
      buckt    = "typo"
    }
  }
`)

	_, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("a field the plugin does not know was accepted")
	}
	if !strings.Contains(err.Error(), "job.hcl") {
		t.Errorf("the error has no position: %v", err)
	}
	if !strings.Contains(err.Error(), "cache") {
		t.Errorf("the error does not name the plugin: %v", err)
	}
}

// TestStagesDoNotShare: a spider plugin cannot be loaded into a downloader,
// because they are different registries carrying different things.
func TestStagesDoNotShare(t *testing.T) {
	down := plugin.NewRegistry[string, string](engine.StageDownloader)
	down.Register("cache", func(context.Context, plugin.Config) (wrapper, error) {
		return noting("cache", nil), nil
	})

	spider := plugin.NewRegistry[string, string](engine.StageSpider)
	spider.Register("depth", func(context.Context, plugin.Config) (wrapper, error) {
		return noting("depth", nil), nil
	})

	j := job(t, `
  downloader {
    plugin "cache" {}
  }

  spider {
    plugin "depth" {}
  }
`)

	ctx := context.Background()
	if built, err := plugin.Build(ctx, down, j, engine.StageDownloader, nil); err != nil || len(built.Links) != 1 {
		t.Errorf("downloader chain = %v, %v", built, err)
	}
	if built, err := plugin.Build(ctx, spider, j, engine.StageSpider, nil); err != nil || len(built.Links) != 1 {
		t.Errorf("spider chain = %v, %v", built, err)
	}

	// The downloader's registry has never heard of the spider's plugin.
	if _, err := plugin.Build(ctx, down, j, engine.StageSpider, nil); err == nil {
		t.Error("a spider plugin built against the downloader's registry")
	}
}

// TestDecodeWithNothingToDecode: a Config built by hand rather than parsed has
// no body, and a plugin that decodes anyway gets its zero schema rather than a
// nil dereference.
func TestDecodeWithNothingToDecode(t *testing.T) {
	var into struct {
		Backend string `hcl:"backend,optional"`
	}
	if err := (plugin.Config{Name: "cache"}).Decode(&into); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if into.Backend != "" {
		t.Errorf("decoded %q from nothing", into.Backend)
	}
}

// TestASecretResolvesHereAndNowhereElse is the claim the package doc makes, so
// it had better be true: the document holds a call, not a value, and the call
// is made once, on the node that builds the plugin.
func TestASecretResolvesHereAndNowhereElse(t *testing.T) {
	j := job(t, `
  downloader {
    plugin "cache" {
      backend = "s3"
      key     = secret("acme-s3-key")
    }
  }
`)

	// Parsing and validating already happened in job(), with no secrets
	// available. Whatever the plugin block holds, it was not evaluated: the
	// value is still in the document as text.
	if plugins := j.Chain(engine.StageDownloader); len(plugins) != 1 {
		t.Fatalf("chain has %d plugins", len(plugins))
	}

	asked := []string{}
	eval := &hcl.EvalContext{
		Functions: map[string]function.Function{
			"secret": function.New(&function.Spec{
				Params: []function.Parameter{{Name: "name", Type: cty.String}},
				Type:   function.StaticReturnType(cty.String),
				Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
					asked = append(asked, args[0].AsString())
					return cty.StringVal("s3cr3t"), nil
				},
			}),
		},
	}

	var got struct {
		Backend string `hcl:"backend,optional"`
		Key     string `hcl:"key,optional"`
	}

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(_ context.Context, cfg plugin.Config) (wrapper, error) {
		if err := cfg.Decode(&got); err != nil {
			return nil, err
		}
		return noting(cfg.Name, nil), nil
	})

	if _, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, eval); err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.Key != "s3cr3t" {
		t.Errorf("the plugin got key %q", got.Key)
	}
	if len(asked) != 1 || asked[0] != "acme-s3-key" {
		t.Errorf("the secret store was asked for %v", asked)
	}
}

// TestASecretIsNotAvailableUntilThen: the same job, built on a node with no
// way to resolve secrets, is refused with the line the call is on. It does not
// quietly become an empty string, which at runtime would look like a request
// that was never meant to carry a credential.
func TestASecretIsNotAvailableUntilThen(t *testing.T) {
	j := job(t, `
  downloader {
    plugin "cache" {
      key = secret("acme-s3-key")
    }
  }
`)

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(_ context.Context, cfg plugin.Config) (wrapper, error) {
		var into struct {
			Key string `hcl:"key,optional"`
		}
		if err := cfg.Decode(&into); err != nil {
			return nil, err
		}
		return noting(cfg.Name, nil), nil
	})

	_, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("a secret resolved without anything to resolve it against")
	}
	if !strings.Contains(err.Error(), "job.hcl:13") {
		t.Errorf("the error does not point at the call: %v", err)
	}
	if !strings.Contains(err.Error(), "cache") {
		t.Errorf("the error does not name the plugin: %v", err)
	}
}

// A plugin's lifetime.

// opener is a plugin that holds something open, which is what a cache plugin
// with a bucket is.
func opener(closed *[]string, name string, err error) plugin.Factory[string, string] {
	return func(_ context.Context, cfg plugin.Config) (wrapper, error) {
		cfg.Defer(func() error {
			*closed = append(*closed, name)
			return err
		})
		return noting(name, nil), nil
	}
}

// TestWhatAPluginOpensIsClosed: the seam hands back a function, and a function
// has nowhere to keep a Close method. Without this a server building a chain
// per job leaks a handle per job.
func TestWhatAPluginOpensIsClosed(t *testing.T) {
	var closed []string

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("offsite", opener(&closed, "offsite", nil))
	reg.Register("cache", opener(&closed, "cache", nil))

	j := job(t, `
  downloader {
    plugin "offsite" {}
    plugin "cache" {}
  }
`)

	built, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(closed) != 0 {
		t.Fatalf("closed %v before anything asked", closed)
	}

	if err := built.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Last opened, first closed: a plugin built later may be holding something
	// an earlier one handed it.
	if got := strings.Join(closed, " "); got != "cache offsite" {
		t.Errorf("closed %q, want the reverse of the order they opened", got)
	}
}

// TestClosingTwiceIsNotAnError, so a caller may defer it and close again on the
// path that has already done so.
func TestClosingTwiceIsNotAnError(t *testing.T) {
	var closed []string

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", opener(&closed, "cache", nil))

	built, err := plugin.Build(context.Background(), reg, job(t, `
  downloader {
    plugin "cache" {}
  }
`), engine.StageDownloader, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if err := built.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := built.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if len(closed) != 1 {
		t.Errorf("closed %d times", len(closed))
	}
}

// TestEveryCloseRuns: a bucket that will not close must not keep a database
// open behind it.
func TestEveryCloseRuns(t *testing.T) {
	var closed []string
	stuck := errors.New("bucket would not close")

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("offsite", opener(&closed, "offsite", nil))
	reg.Register("cache", opener(&closed, "cache", stuck))

	built, err := plugin.Build(context.Background(), reg, job(t, `
  downloader {
    plugin "offsite" {}
    plugin "cache" {}
  }
`), engine.StageDownloader, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	err = built.Close()
	if !errors.Is(err, stuck) {
		t.Errorf("close reported %v", err)
	}
	if len(closed) != 2 {
		t.Errorf("only %v closed", closed)
	}
}

// TestARefusedChainClosesWhatItOpened: a job refused for its second plugin must
// not leave the bucket its first one opened, and the caller has no chain to
// close it with.
func TestARefusedChainClosesWhatItOpened(t *testing.T) {
	var closed []string

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", opener(&closed, "cache", nil))

	j := job(t, `
  downloader {
    plugin "cache" {}

    plugin "somebody-elses" {
      order = 42
    }
  }
`)

	built, err := plugin.Build(context.Background(), reg, j, engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("built a chain naming something nothing implements")
	}
	if built != nil {
		t.Error("a refused build returned a chain to close")
	}
	if len(closed) != 1 || closed[0] != "cache" {
		t.Errorf("closed %v, want the one plugin that had opened something", closed)
	}
}

// TestARefusedChainSaysWhatWouldNotClose, because a plugin that fails to build
// and a bucket that will not release are two different problems and the second
// one is the one somebody has to go and look at.
func TestARefusedChainSaysWhatWouldNotClose(t *testing.T) {
	var closed []string
	stuck := errors.New("bucket would not close")

	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", opener(&closed, "cache", stuck))
	reg.Register("offsite", func(context.Context, plugin.Config) (wrapper, error) {
		return nil, errors.New("no domains configured")
	})

	_, err := plugin.Build(context.Background(), reg, job(t, `
  downloader {
    plugin "offsite" {}
    plugin "cache" {}
  }
`), engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("built it anyway")
	}
	if !errors.Is(err, stuck) {
		t.Errorf("the close failure was lost: %v", err)
	}
	if !strings.Contains(err.Error(), "no domains configured") {
		t.Errorf("the build failure was lost: %v", err)
	}
}

// TestAHandBuiltConfigHasNowhereToDeferTo, which is what a test constructing
// one middleware on its own wants.
func TestAHandBuiltConfigHasNowhereToDeferTo(t *testing.T) {
	cfg := plugin.Config{Name: "cache"}
	cfg.Defer(func() error { return errors.New("never runs") })

	// And a nil close is not registered either, so a plugin may pass one
	// without checking.
	cfg.Defer(nil)
}

// TestANilChainCloses, so a caller that deferred a close before checking the
// error does not panic on the way out.
func TestANilChainCloses(t *testing.T) {
	var built *plugin.Chain[string, string]
	if err := built.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestANodeWithNoSecretsRefusesByName.
//
// A nil evaluation context makes HCL refuse any function call with "Functions
// may not be called here", which tells somebody whose job used secret() nothing
// about what went wrong or what to do about it. Every binary produced exactly
// that message, because nothing ever built a real context: the secret store was
// written, tested and unreachable.
func TestANodeWithNoSecretsRefusesByName(t *testing.T) {
	reg := plugin.NewRegistry[string, string](engine.StageDownloader)
	reg.Register("cache", func(_ context.Context, cfg plugin.Config) (wrapper, error) {
		var into struct {
			Key string `hcl:"key,optional"`
		}
		if err := cfg.Decode(&into); err != nil {
			return nil, err
		}
		return noting("cache", nil), nil
	})

	// No Eval at all, which is what a node without a secret store passes.
	_, err := plugin.Build(context.Background(), reg, job(t, `
  downloader {
    plugin "cache" {
      key = secret("acme-s3-key")
    }
  }
`), engine.StageDownloader, nil)
	if err == nil {
		t.Fatal("a node with no secrets resolved one")
	}
	if !strings.Contains(err.Error(), "acme-s3-key") {
		t.Errorf("the error does not name the secret: %v", err)
	}
	if !strings.Contains(err.Error(), "no secrets") {
		t.Errorf("the error does not say what this node is missing: %v", err)
	}
}
