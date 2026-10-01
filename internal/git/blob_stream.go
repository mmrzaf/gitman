package git

import (
	"context"
	"fmt"
	"io"
	"time"
)

// StreamBlob copies an immutable blob without buffering its contents.
func (r *Repo) StreamBlob(ctx context.Context, hash string, dst io.Writer) error {
	if !IsHash(hash) {
		return fmt.Errorf("invalid blob hash")
	}
	opts := r.opts()
	opts.timeout = 15 * time.Minute
	return stream(ctx, opts, func(src io.Reader) error {
		_, err := io.Copy(dst, src)
		return err
	}, "cat-file", "blob", hash)
}

// BlobSize reads metadata without loading a blob's contents.
func (r *Repo) BlobSize(ctx context.Context, hash string) (int64, error) {
	if !IsHash(hash) {
		return 0, fmt.Errorf("invalid blob hash")
	}
	var size int64
	err := r.source.with(ctx, func(reader *reader) error {
		info, err := reader.info(ctx, hash)
		if err != nil {
			return err
		}
		if info.typ != TypeBlob {
			return fmt.Errorf("object is not a blob")
		}
		size = info.size
		return nil
	})
	return size, err
}
