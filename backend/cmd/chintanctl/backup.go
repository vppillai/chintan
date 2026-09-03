package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
)

// Backup layout, all under --out:
//
//	backup.json    the header. Written LAST, so its presence is the signal
//	               that the backup completed. restore refuses without it.
//	items.jsonl    one verbatim DynamoDB item per line.
//	objects.jsonl  one {key,size,sha256} record per object.
//	objects/<key>  the object bodies, byte for byte.
//
// The two manifests are themselves hashed into backup.json, so a truncated or
// edited manifest is caught before a single byte is put back.
const (
	backupHeaderName  = "backup.json"
	backupItemsName   = "items.jsonl"
	backupObjectsName = "objects.jsonl"
	backupObjectsDir  = "objects"
	backupFormat      = 1
)

type backupHeader struct {
	Format        int      `json:"format"`
	CreatedAt     string   `json:"created_at"`
	Instance      string   `json:"instance"`
	Environment   string   `json:"environment"`
	Table         string   `json:"table"`
	Bucket        string   `json:"bucket"`
	Tenants       []string `json:"tenants"`
	ItemCount     int      `json:"item_count"`
	ObjectCount   int      `json:"object_count"`
	TotalBytes    int64    `json:"total_bytes"`
	ItemsSHA256   string   `json:"items_sha256"`
	ObjectsSHA256 string   `json:"objects_sha256"`
}

// objectRecord is one line of objects.jsonl.
type objectRecord struct {
	TenantID string `json:"tenant_id,omitempty"`
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type backupResult struct {
	Target      target   `json:"target"`
	Out         string   `json:"out"`
	Tenants     []string `json:"tenants"`
	ItemCount   int      `json:"item_count"`
	ObjectCount int      `json:"object_count"`
	TotalBytes  int64    `json:"total_bytes"`
}

func (r *backupResult) human(w *lineWriter) {
	w.printf("backup %s (%s) -> %s\n", r.Target.Instance, r.Target.Environment, r.Out)
	w.printf("  tenants: %s\n", strings.Join(r.Tenants, ", "))
	w.printf("  %d index items, %d objects, %s\n", r.ItemCount, r.ObjectCount, humanBytes(r.TotalBytes))
	w.printf("  manifest: %s (every object carries a sha256; restore checks all of them)\n",
		filepath.Join(r.Out, backupHeaderName))
}

func cmdBackup(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var out string
	fs := newFlagSet("backup", stderr)
	g.register(fs, true, false)
	fs.StringVar(&out, "out", "", "output directory (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if out == "" {
		return fmt.Errorf("--out is required")
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runBackup(ctx, e, out, g.tenants)
	if err != nil {
		return err
	}
	return report(stdout, g.jsonOut, res)
}

// hashedWriter writes through to a file while accumulating a hash, so a
// manifest is hashed as it is produced rather than read back afterwards.
type hashedWriter struct {
	file *os.File
	buf  *bufio.Writer
	sum  hash.Hash
	w    io.Writer
}

func newHashedWriter(path string) (*hashedWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	h := sha256.New()
	buf := bufio.NewWriter(f)
	return &hashedWriter{file: f, buf: buf, sum: h, w: io.MultiWriter(buf, h)}, nil
}

func (h *hashedWriter) writeLine(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := h.w.Write(append(body, '\n')); err != nil {
		return err
	}
	return nil
}

func (h *hashedWriter) close() (string, error) {
	if err := h.buf.Flush(); err != nil {
		_ = h.file.Close()
		return "", err
	}
	if err := h.file.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.sum.Sum(nil)), nil
}

// runBackup copies the instance's state to a local directory at full fidelity:
// items exactly as DynamoDB holds them, objects exactly as S3 holds them.
func runBackup(ctx context.Context, e *env, out string, explicitTenants []string) (*backupResult, error) {
	tenants, err := resolveTenants(ctx, e.Blobs, explicitTenants)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}

	items, err := newHashedWriter(filepath.Join(out, backupItemsName))
	if err != nil {
		return nil, err
	}
	objects, err := newHashedWriter(filepath.Join(out, backupObjectsName))
	if err != nil {
		return nil, err
	}

	res := &backupResult{Target: e.Target, Out: out, Tenants: tenants}

	for _, tenantID := range tenants {
		tctx := obs.WithTenant(ctx, tenantID)

		// The partition, whole. No sort-key filter, no kind switch: whatever
		// is in the partition is in the backup. See enumerate.go.
		err := e.Part.Scan(tctx, tenantPK(tenantID), "", func(it Item) error {
			res.ItemCount++
			return items.writeLine(it)
		})
		if err != nil {
			return nil, err
		}

		// The prefix, whole.
		err = e.Blobs.List(tctx, tenantPrefix(tenantID), func(info ObjectInfo) error {
			sum, n, err := copyToBackup(tctx, e, out, info)
			if err != nil {
				return err
			}
			res.ObjectCount++
			res.TotalBytes += n
			return objects.writeLine(objectRecord{
				TenantID: tenantID,
				Key:      info.Key,
				Size:     n,
				SHA256:   sum,
			})
		})
		if err != nil {
			return nil, err
		}

		obs.Log(tctx).Info("backed up tenant",
			slog.Int("items", res.ItemCount),
			slog.Int("objects", res.ObjectCount),
		)
	}

	itemsSum, err := items.close()
	if err != nil {
		return nil, err
	}
	objectsSum, err := objects.close()
	if err != nil {
		return nil, err
	}

	header := backupHeader{
		Format:        backupFormat,
		CreatedAt:     model.Now(),
		Instance:      e.Target.Instance,
		Environment:   e.Target.Environment,
		Table:         e.Target.Table,
		Bucket:        e.Target.Bucket,
		Tenants:       tenants,
		ItemCount:     res.ItemCount,
		ObjectCount:   res.ObjectCount,
		TotalBytes:    res.TotalBytes,
		ItemsSHA256:   itemsSum,
		ObjectsSHA256: objectsSum,
	}
	body, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return nil, err
	}
	// Last, deliberately: an interrupted backup has no header, and restore
	// refuses a directory without one rather than restoring half a tenant.
	if err := os.WriteFile(filepath.Join(out, backupHeaderName), append(body, '\n'), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", backupHeaderName, err)
	}
	return res, nil
}

// copyToBackup streams one object to disk and returns its hash and size. The
// body is never held: it goes reader -> (file, hasher) a buffer at a time.
func copyToBackup(ctx context.Context, e *env, out string, info ObjectInfo) (string, int64, error) {
	rel, err := safeRelPath(info.Key)
	if err != nil {
		return "", 0, err
	}
	dest := filepath.Join(out, backupObjectsDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", 0, fmt.Errorf("create directory for %s: %w", rel, err)
	}

	src, err := e.Blobs.Open(ctx, info.Key)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", info.Key, err)
	}
	defer func() { _ = src.Close() }()

	f, err := os.Create(dest)
	if err != nil {
		return "", 0, fmt.Errorf("create %s: %w", dest, err)
	}
	sum := sha256.New()
	buf := bufio.NewWriter(f)
	n, err := io.Copy(io.MultiWriter(buf, sum), src)
	if err != nil {
		_ = f.Close()
		return "", 0, fmt.Errorf("copy %s: %w", info.Key, err)
	}
	if err := buf.Flush(); err != nil {
		_ = f.Close()
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(sum.Sum(nil)), n, nil
}

// backupAge is only used to make a header readable in a summary.
func backupAge(created string) string {
	t, err := model.ParseTime(created)
	if err != nil {
		return created
	}
	return t.Format(time.RFC3339)
}
