package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Source fetches raw config bytes. One method, so a Lambda cold start and a CLI run
// differ only in which Source they hold.
type Source interface {
	Fetch(ctx context.Context) (raw []byte, description string, err error)
}

// FileSource reads config from the filesystem, for the CLI and for tests.
type FileSource struct{ Path string }

// Fetch reads the file.
func (f FileSource) Fetch(context.Context) ([]byte, string, error) {
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, f.Path, fmt.Errorf("config: reading %s: %w", f.Path, err)
	}
	return raw, f.Path, nil
}

// ObjectGetter is the subset of an object store this package needs. Declared here
// rather than importing an S3 client, so this package has no cloud dependency and its
// tests need no fake AWS.
type ObjectGetter interface {
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
}

// ObjectSource reads config from object storage.
//
// **Why config lives in S3 rather than being embedded in the binary.** §7.4 requires
// that "config changes must not require a code change or rebuild — reload on deploy."
// A `go:embed` would satisfy the first half and fail the second: changing a threshold
// would mean recompiling. Reading it at cold start means a config-only change is an
// upload plus a function update, and `GET /v1/health` then reports the config version
// actually in force rather than the one that was compiled in.
//
// The cost is one S3 GET per cold start, which is why the result is cached in module
// scope by the Cached wrapper (§10.2's "fetch at cold start and cache in module scope",
// stated there about secrets and applying equally here).
type ObjectSource struct {
	Getter ObjectGetter
	Bucket string
	Key    string
}

// Fetch reads the object.
func (o ObjectSource) Fetch(ctx context.Context) ([]byte, string, error) {
	desc := "s3://" + o.Bucket + "/" + o.Key
	body, err := o.Getter.GetObject(ctx, o.Bucket, o.Key)
	if err != nil {
		return nil, desc, fmt.Errorf("config: fetching %s: %w", desc, err)
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, desc, fmt.Errorf("config: reading %s: %w", desc, err)
	}
	return raw, desc, nil
}

// Cached loads once and reuses the result, which is the cold-start caching pattern
// §10.2 prescribes. Safe for concurrent use.
type Cached struct {
	Source Source

	once sync.Once
	cfg  *Config
	err  error
}

// Get loads and validates the config, at most once.
//
// **Fails loudly and at cold start**, which is what §Phase 0 requires: "Config loader
// with schema validation; fails loudly and at cold start on invalid config." A handler
// must not start serving with a half-valid config, because the alternative is
// discovering a missing threshold on the request that needed it.
func (c *Cached) Get(ctx context.Context) (*Config, error) {
	c.once.Do(func() {
		raw, desc, err := c.Source.Fetch(ctx)
		if err != nil {
			c.err = err
			return
		}
		c.cfg, c.err = Parse(raw, desc)
	})
	return c.cfg, c.err
}

// ResolveSecretRefs substitutes {env} in every secret_ref with the instance name.
//
// Returns the resolved *paths*, never values. Resolution of a path to a value is the
// Lambda execution role's job at runtime; the build environment and the agent must not
// be able to read one (§9.4), and `kms:Decrypt` on these paths is denied to the agent
// principal.
func (c *Config) ResolveSecretRefs() map[string]string {
	out := map[string]string{}
	sub := func(ref string) string {
		return strings.ReplaceAll(ref, "{env}", c.Instance)
	}
	for name, e := range c.Providers.STT.Catalog {
		out["stt/"+name] = sub(e.SecretRef)
	}
	for name, e := range c.Providers.LLM.Catalog {
		out["llm/"+name] = sub(e.SecretRef)
	}
	for name, e := range c.Providers.Embeddings.Catalog {
		out["embeddings/"+name] = sub(e.SecretRef)
	}
	return out
}
