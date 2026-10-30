package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Storage connections (ADR 0034): S3-compatible buckets defined once, by
// name, for what keeps data in object storage (the registry's images).

// newStorageCmd is `dock storage`: the list, and add, set and remove.
func newStorageCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "S3-compatible storage connections (admin)",
		Long: `List the storage connections: S3-compatible buckets (AWS, Hetzner Object
Storage, Cloudflare R2, Backblaze B2, MinIO, Garage) defined once and used
by what keeps data in object storage. The registry keeps its images in one
after ` + "`dock registry migrate --to s3`" + `. Keys are never shown.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.Storage](cmd.Context(), s, apiPrefix+"/storage")
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("No storage connections. Add one with `dock storage add`.")
					return nil
				}
				t := g.io.Table("name", "endpoint", "bucket", "region", "addressing", "used by")
				for _, st := range list {
					t.Row(st.Name, st.Endpoint, st.Bucket, orDash(st.Region), addressing(st.PathStyle), orDash(strings.Join(st.UsedBy, ", ")))
				}
				return t.Flush()
			})
		},
	}
	cmd.AddCommand(newStorageAddCmd(g), newStorageSetCmd(g), newStorageRemoveCmd(g))
	return cmd
}

// addressing names how a connection addresses its bucket.
func addressing(pathStyle bool) string {
	if pathStyle {
		return "path"
	}
	return "host"
}

// orDash shows an empty table cell as "-".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// readSecret reads a secret access key piped to stdin.
func readSecret(g *globals, cmdline string) (string, error) {
	b, err := io.ReadAll(io.LimitReader(g.io.In, maxSecretInput))
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", usageError("--secret-access-key-stdin read nothing", fmt.Sprintf("Pipe the secret in: `printf %%s \"$SECRET\" | %s`.", cmdline))
	}
	return secret, nil
}

// newStorageAddCmd is `dock storage add`.
func newStorageAddCmd(g *globals) *cobra.Command {
	var req apitypes.CreateStorageRequest
	pathStyle := true
	var secretStdin bool
	cmd := &cobra.Command{
		Use:   "add <name> --endpoint <url> --bucket <bucket> --access-key-id <id> --secret-access-key-stdin",
		Short: "Add an S3-compatible storage connection (admin)",
		Long: `Add a bucket in any S3-compatible store. The endpoint is the store's base
URL, without the bucket. Dock lists the bucket with the keys before saving,
so a wrong endpoint, bucket, region or key fails here. Path-style
addressing (https://endpoint/bucket) is the default and works everywhere;
--path-style=false puts the bucket in the host name.`,
		Example: `  printf %s "$SECRET" | dock storage add hetzner --endpoint https://fsn1.your-objectstorage.com \
      --region fsn1 --bucket dock-images --access-key-id "$KEY_ID" --secret-access-key-stdin
  printf %s "$SECRET" | dock storage add r2 --endpoint https://<account>.r2.cloudflarestorage.com \
      --region auto --bucket dock-images --access-key-id "$KEY_ID" --secret-access-key-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !secretStdin {
				return usageError("the secret access key is required", "Pipe it in with --secret-access-key-stdin; it never goes in an argument.")
			}
			secret, err := readSecret(g, "dock storage add ... --secret-access-key-stdin")
			if err != nil {
				return err
			}
			req.Name, req.SecretAccessKey, req.PathStyle = args[0], secret, &pathStyle
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := send[apitypes.Storage](cmd.Context(), s, http.MethodPost, apiPrefix+"/storage", req)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Added storage %s: bucket %s at %s", st.Name, st.Bucket, st.Endpoint)
				g.io.Detail("Move the registry's images there with `dock registry migrate --to s3 --storage " + st.Name + "`.")
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&req.Endpoint, "endpoint", "", "the store's base URL, like https://fsn1.your-objectstorage.com")
	fs.StringVar(&req.Region, "region", "", "region to sign requests for (default us-east-1)")
	fs.StringVar(&req.Bucket, "bucket", "", "the bucket")
	fs.StringVar(&req.AccessKeyID, "access-key-id", "", "access key ID")
	fs.BoolVar(&secretStdin, "secret-access-key-stdin", false, "read the secret access key from stdin")
	fs.BoolVar(&pathStyle, "path-style", pathStyle, "address the bucket in the URL's path; false puts it in the host name")
	return cmd
}

// newStorageSetCmd is `dock storage set`.
func newStorageSetCmd(g *globals) *cobra.Command {
	var endpoint, region, bucket, accessKey string
	var pathStyle, secretStdin bool
	cmd := &cobra.Command{
		Use:   "set <name> [flags]",
		Short: "Change a storage connection: keys, region, addressing (admin)",
		Long: `Change a storage connection. Flags left out stay as they are; the bucket is
listed again with the result. While the registry keeps its images in it,
the endpoint and bucket stay (move the images with ` + "`dock registry migrate`" + `),
and new keys restart the registry for a second.`,
		Example: `  printf %s "$NEW_SECRET" | dock storage set hetzner --access-key-id "$NEW_KEY_ID" --secret-access-key-stdin`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.UpdateStorageRequest
			fs := cmd.Flags()
			for name, v := range map[string]struct {
				src *string
				dst **string
			}{
				"endpoint": {&endpoint, &req.Endpoint}, "region": {&region, &req.Region},
				"bucket": {&bucket, &req.Bucket}, "access-key-id": {&accessKey, &req.AccessKeyID},
			} {
				if fs.Changed(name) {
					*v.dst = v.src
				}
			}
			if fs.Changed("path-style") {
				req.PathStyle = &pathStyle
			}
			if secretStdin {
				secret, err := readSecret(g, "dock storage set "+args[0]+" --secret-access-key-stdin")
				if err != nil {
					return err
				}
				req.SecretAccessKey = &secret
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := send[apitypes.Storage](cmd.Context(), s, http.MethodPatch, apiPrefix+"/storage/"+url.PathEscape(args[0]), req)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Saved storage %s", st.Name)
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&endpoint, "endpoint", "", "the store's base URL")
	fs.StringVar(&region, "region", "", "region to sign requests for")
	fs.StringVar(&bucket, "bucket", "", "the bucket")
	fs.StringVar(&accessKey, "access-key-id", "", "access key ID")
	fs.BoolVar(&secretStdin, "secret-access-key-stdin", false, "read the secret access key from stdin")
	fs.BoolVar(&pathStyle, "path-style", true, "address the bucket in the URL's path; false puts it in the host name")
	return cmd
}

// newStorageRemoveCmd is `dock storage remove`.
func newStorageRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a storage connection nothing uses (admin)",
		Long:  `Remove a storage connection. The bucket and what is in it are left alone. Refused while the registry keeps its images in it.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove storage connection %s? The bucket is left as it is.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/storage/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed storage connection %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
