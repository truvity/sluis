package s3blob

import (
	"context"

	"github.com/truvity/sluis/internal/port"
)

func init() {
	port.Register(port.Descriptor{
		Name: "s3", Concern: port.ConcernBlobs,
		Summary:  "Blobs in an S3 bucket, optionally under SSE-KMS.",
		Requires: port.Requires{AWS: true},
		Factory: func(ctx context.Context, s port.Settings) (any, error) {
			var cfg Config
			if err := s.Decode(&cfg); err != nil {
				return nil, err
			}
			return New(ctx, cfg)
		},
	})
}
