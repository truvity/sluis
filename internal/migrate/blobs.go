package migrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/sluis/blobprefix"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/store"
)

// reportPrefixes are the blobs a copy keeps: what each controller last reported,
// under the module's prefix (blobprefix). A store that still has them at the
// root of the bucket, where layout v4 kept them, is read from there too, and
// they are written to the module's prefix.
var reportPrefixes = []string{blobprefix.GitHubReports, blobprefix.SlackReports}

func blobSteps() []step {
	return []step{{
		domain: DomainBlobs, name: "reports",
		read: func(ctx context.Context, k kit) ([]entry, error) {
			if k.ports.Blob == nil {
				return nil, fmt.Errorf("%w: there is no Blob port", port.ErrUnsupported)
			}
			var out []entry
			seen := map[string]bool{}
			read := func(name, id string) error {
				if seen[id] {
					return nil
				}
				obj, err := k.ports.Blob.Read(ctx, name)
				if errors.Is(err, port.ErrNotFound) {
					return nil // gone between the listing and the read
				}
				if err != nil {
					return err
				}
				seen[id] = true
				out = append(out, entry{id: id, canon: obj.Body})
				return nil
			}
			for _, prefix := range reportPrefixes {
				names, err := k.ports.Blob.List(ctx, prefix)
				if err != nil {
					return nil, err
				}
				for _, name := range names {
					if err := read(name, name); err != nil {
						return nil, err
					}
				}
				// The layout v4 location. A store that has no such family
				// (the ConfigMaps keep theirs under the new names) says so.
				old := blobprefix.Legacy(prefix)
				names, err = k.ports.Blob.List(ctx, old)
				if errors.Is(err, port.ErrUnsupported) {
					continue
				}
				if err != nil {
					return nil, err
				}
				for _, name := range names {
					if err := read(name, prefix+strings.TrimPrefix(name, old)); err != nil {
						return nil, err
					}
				}
			}
			return out, nil
		},
		write: func(ctx context.Context, k kit, e entry, _ *entry) error {
			_, err := k.ports.Blob.Write(ctx, e.id, e.canon)
			return err
		},
	}}
}

// BlobID says where a configuration keeps its blobs, so that two sides that keep
// them in one place are known to: the same S3 bucket and prefix, or, with no
// `ports.blob`, the same release's objects. The memory adapter keeps them
// nowhere another process reads, so it is never "the same".
func BlobID(c store.Config) string {
	switch {
	case c.Blob != nil && c.Blob.S3 != nil:
		s := c.Blob.S3
		return fmt.Sprintf("s3|%s|%s|%s|%s", s.Region, s.Endpoint, s.Bucket, s.Prefix)
	case c.Adapter == store.AdapterMemory:
		return ""
	}
	return fmt.Sprintf("namespace|%s|%s", c.Release, c.Valkey.Address)
}
