# Remote checkpoint stores

Checkpoints persist to an object store so a sandbox's history survives the
machine it ran on, and so another machine can pull a checkpoint and fork from
it. The store is content addressed, so a push uploads only blobs the bucket
does not already have, and pushing the same sandbox again moves only what
changed.

## What a remote holds

A push writes three kinds of object under an optional prefix:

```
blobs/<sha256>          file contents and serialised trees, shared and deduplicated
checkpoints/<id>.json   checkpoint metadata (parent, tree hash, command, counts)
sandboxes/<id>.json     sandbox metadata
```

A pull reads a checkpoint's metadata, then the tree, then every blob the tree
references, verifying each blob against its hash as it lands.

## S3 and S3-compatible services

```bash
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=us-east-1
vit push agent --to s3://my-bucket/checkpoints
vit pull ck-2c52a0bf6a14 --from s3://my-bucket/checkpoints
```

The client signs each request with AWS Signature Version 4 using only the
standard library, so it needs no SDK. It works with:

- **AWS S3**: set `AWS_REGION`. The endpoint defaults to the regional S3 host.
- **MinIO, Ceph**: set `AWS_ENDPOINT_URL` to the service and `VIT_S3_PATH_STYLE=1`.
- **Cloudflare R2**: set `AWS_ENDPOINT_URL` to your account's R2 endpoint and
  `AWS_REGION=auto`.
- **Backblaze B2**: set `AWS_ENDPOINT_URL` to the B2 S3 endpoint.

Environment:

| variable | meaning |
|---|---|
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | credentials, required |
| `AWS_REGION` or `AWS_DEFAULT_REGION` | region, default `us-east-1` |
| `AWS_ENDPOINT_URL` or `S3_ENDPOINT_URL` | endpoint for non-AWS services |
| `VIT_S3_PATH_STYLE` | address as `endpoint/bucket/key` when set |

## A directory remote

For a shared filesystem, an NFS mount, or a bucket mounted locally, a directory
store needs no credentials:

```bash
vit push agent --to dir:///mnt/shared/vit-checkpoints
vit pull ck-... --from /mnt/shared/vit-checkpoints
```

## Notes

- Push is safe to repeat and safe to run from several machines at once; blobs
  are immutable and keyed by content.
- Pull writes into the local repo without creating a sandbox, then `vit pull`
  forks the checkpoint so you have a working sandbox to run. To pull without
  forking, use the library `Engine.Pull`.
- Firecracker checkpoints push their memory and disk manifests and every chunk
  the bucket does not already have, so a pulled checkpoint forks warm.
