package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/numinous-technology/vitvm/internal/remote"
)

// parseRemote turns a target into an object store.
//
//	s3://bucket/prefix     S3 or any S3-compatible service; credentials and
//	                       endpoint come from the environment (see below)
//	dir:///abs/path        a local or shared directory
//	/abs/path or ./path    shorthand for a directory store
//
// S3 environment:
//
//	AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY   credentials (required)
//	AWS_REGION or AWS_DEFAULT_REGION           region (default us-east-1)
//	AWS_ENDPOINT_URL or S3_ENDPOINT_URL        for MinIO, R2, B2, Ceph
//	VIT_S3_PATH_STYLE=1                         address as endpoint/bucket/key
func parseRemote(target string) (remote.ObjectStore, error) {
	switch {
	case strings.HasPrefix(target, "s3://"):
		rest := strings.TrimPrefix(target, "s3://")
		bucket, prefix, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return nil, fmt.Errorf("s3 target needs a bucket: s3://bucket/prefix")
		}
		ak, sk := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
		if ak == "" || sk == "" {
			return nil, fmt.Errorf("set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY for s3")
		}
		region := firstEnv("AWS_REGION", "AWS_DEFAULT_REGION")
		if region == "" {
			region = "us-east-1"
		}
		return remote.NewS3(remote.S3Config{
			Endpoint: firstEnv("AWS_ENDPOINT_URL", "S3_ENDPOINT_URL"),
			Region:   region, Bucket: bucket, Prefix: prefix,
			AccessKey: ak, SecretKey: sk,
			PathStyle: os.Getenv("VIT_S3_PATH_STYLE") != "",
		})
	case strings.HasPrefix(target, "dir://"):
		return remote.NewDirStore(strings.TrimPrefix(target, "dir://"))
	case strings.HasPrefix(target, "/"), strings.HasPrefix(target, "./"), strings.HasPrefix(target, "../"):
		return remote.NewDirStore(target)
	default:
		return nil, fmt.Errorf("unknown remote %q (use s3://bucket/prefix, dir:///path, or a filesystem path)", target)
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func cmdPush(args []string) error {
	fs := parseFlags(args)
	to := fs.str("to")
	if to == "" {
		return fmt.Errorf("usage: vit push [SANDBOX] --to s3://bucket/prefix")
	}
	id := ""
	if len(fs.rest) > 0 {
		id = fs.rest[0]
	}
	e, s, _, err := sandboxEngine(id)
	if err != nil {
		return err
	}
	store, err := parseRemote(to)
	if err != nil {
		return err
	}
	st, err := e.Push(s.ID, store)
	if err != nil {
		return err
	}
	fmt.Printf("pushed %s to %s: %d checkpoints, %d blobs uploaded, %d already there\n",
		s.Name, to, st.Checkpoints, st.Blobs, st.BlobsSkipped)
	return nil
}

func cmdPull(args []string) error {
	fs := parseFlags(args)
	from := fs.str("from")
	if len(fs.rest) < 1 || from == "" {
		return fmt.Errorf("usage: vit pull CHECKPOINT --from s3://bucket/prefix [--as NAME] [--backend B]")
	}
	repo, root, cfg, err := openRepo()
	if err != nil {
		return err
	}
	store, err := parseRemote(from)
	if err != nil {
		return err
	}
	backend := fs.str("backend")
	if backend == "" {
		backend = cfg.get("backend")
	}
	e, err := engineFor(repo, cfg, backend)
	if err != nil {
		return err
	}
	c, err := e.Pull(store, fs.rest[0])
	if err != nil {
		return err
	}
	fmt.Printf("pulled checkpoint %s\n", c.ID)
	fork, err := e.Fork(c.ID, fs.str("as"))
	if err != nil {
		return err
	}
	setCurrent(root, fork.ID)
	fmt.Printf("forked into sandbox %s (%s) on %s, now current\n", fork.Name, fork.ID, fork.Backend)
	return nil
}
