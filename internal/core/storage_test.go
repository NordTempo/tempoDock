package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// fakeS3 answers ListObjectsV2 for bucket "images" with an empty listing
// and NoSuchBucket for any other bucket (path style).
func fakeS3(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchBucket</Code><Message>no</Message></Error>`)
			return
		}
		_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Storage connections are for operators; the bucket is checked before a
// connection is saved, and its keys are sealed and never returned.
func TestStorageConnections(t *testing.T) {
	h := newHarness(t)
	endpoint := fakeS3(t)
	req := apitypes.CreateStorageRequest{Name: "hetzner", Endpoint: endpoint + "/", Bucket: "images", AccessKeyID: "AKID", SecretAccessKey: "top-secret"}

	admin := Actor{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin}
	if _, err := h.c.AddStorage(h.ctx, admin, req); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin added storage: %v", err)
	}
	if _, err := h.c.ListStorage(h.ctx, admin); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin listed storage: %v", err)
	}

	missing := req
	missing.Bucket = "nope"
	if _, err := h.c.AddStorage(h.ctx, testSystem, missing); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "doesn't exist") {
		t.Fatalf("added a missing bucket: %v", err)
	}
	st, err := h.c.AddStorage(h.ctx, testSystem, req)
	if err != nil {
		t.Fatal(err)
	}
	if st.Endpoint != endpoint || !st.PathStyle || len(st.UsedBy) != 0 {
		t.Fatalf("added %+v", st)
	}
	if _, err := h.c.AddStorage(h.ctx, testSystem, req); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("added the same name twice: %v", err)
	}
	list, err := h.c.ListStorage(h.ctx, testSystem)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if b, _ := json.Marshal(list); bytes.Contains(b, []byte("top-secret")) || bytes.Contains(b, []byte("AKID")) {
		t.Fatalf("keys in the listing: %s", b)
	}
	row, err := h.store.GetStorageConnection(h.ctx, "hetzner")
	if err != nil || bytes.Contains(row.Credentials, []byte("top-secret")) {
		t.Fatalf("keys not sealed: %v", err)
	}

	// A secret may end in a slash; only the endpoint loses its trailing one.
	if _, err := h.c.UpdateStorage(h.ctx, testSystem, "hetzner", apitypes.UpdateStorageRequest{SecretAccessKey: new(" abc/ ")}); err != nil {
		t.Fatal(err)
	}
	if _, b, err := h.c.storageByName(h.ctx, "hetzner"); err != nil || b.SecretAccessKey != "abc/" {
		t.Fatalf("secret = %q, %v", b.SecretAccessKey, err)
	}

	if err := h.c.RemoveStorage(h.ctx, testSystem, "hetzner"); err != nil {
		t.Fatal(err)
	}
	if err := h.c.RemoveStorage(h.ctx, testSystem, "hetzner"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("removed twice: %v", err)
	}
}

// waitMigrated waits until no migration runs and returns the registry.
func waitMigrated(t *testing.T, h *harness) apitypes.Registry {
	t.Helper()
	var reg apitypes.Registry
	eventually(t, "the migration to finish", func() bool {
		var err error
		reg, err = h.c.GetRegistry(h.ctx, testSystem)
		return err == nil && reg.Migration != nil && !reg.Migration.Running
	})
	return reg
}

// A migration copies the images, records the new storage, drops the old
// copy, and the setting holds the connection: in use, the bucket can't
// change or the connection go, new keys reach the registry, and a
// restart points the registry at it again.
func TestMigrateRegistry(t *testing.T) {
	h := newHarness(t)
	reg := &fakeRegistry{}
	h.opts.Registry = reg
	h.restart()
	if ops := reg.Ops(); !slices.Equal(ops, []string{"use local"}) {
		t.Fatalf("start: ops = %v", ops)
	}
	if _, err := h.c.MigrateRegistry(h.ctx, testSystem, apitypes.MigrateRegistryRequest{To: "s3"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("migrated with no storage: %v", err)
	}
	if _, err := h.c.MigrateRegistry(h.ctx, testSystem, apitypes.MigrateRegistryRequest{To: "local"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("migrated to where the images are: %v", err)
	}
	endpoint := fakeS3(t)
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "hetzner", Endpoint: endpoint, Bucket: "images", AccessKeyID: "k1", SecretAccessKey: "s1"}); err != nil {
		t.Fatal(err)
	}

	// A failed copy leaves the images where they are.
	reg.migrateErr = errors.New("bucket full")
	if _, err := h.c.MigrateRegistry(h.ctx, testSystem, apitypes.MigrateRegistryRequest{To: "s3"}); err != nil {
		t.Fatal(err)
	}
	if r := waitMigrated(t, h); r.Storage != "local" || r.Migration.Error != "bucket full" {
		t.Fatalf("after a failed migration: storage %s, %+v", r.Storage, r.Migration)
	}

	reg.migrateErr, reg.gate = nil, make(chan struct{})
	r, err := h.c.MigrateRegistry(h.ctx, testSystem, apitypes.MigrateRegistryRequest{To: "s3", Storage: "hetzner"})
	if err != nil || r.Migration == nil || !r.Migration.Running || r.Migration.To != "hetzner" {
		t.Fatalf("migrate = %+v, %v", r.Migration, err)
	}
	if _, err := h.c.MigrateRegistry(h.ctx, testSystem, apitypes.MigrateRegistryRequest{To: "s3"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a second migration started: %v", err)
	}
	if _, err := h.c.PruneRegistry(h.ctx, testSystem); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("pruned while migrating: %v", err)
	}
	if err := h.c.RemoveStorage(h.ctx, testSystem, "hetzner"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removed the target of a migration: %v", err)
	}
	if _, err := h.c.UpdateStorage(h.ctx, testSystem, "hetzner", apitypes.UpdateStorageRequest{Bucket: new("other")}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("changed the target of a migration: %v", err)
	}
	close(reg.gate)
	r = waitMigrated(t, h)
	if r.Storage != "hetzner" || r.Migration.Error != "" || r.Migration.Objects != 3 || r.Migration.Bytes != 300 {
		t.Fatalf("after the migration: storage %s, %+v", r.Storage, r.Migration)
	}
	if ops := reg.Ops(); !slices.Equal(ops[len(ops)-2:], []string{"migrate to images:k1", "drop local"}) {
		t.Fatalf("ops = %v", ops)
	}

	if _, err := h.c.UpdateStorage(h.ctx, testSystem, "hetzner", apitypes.UpdateStorageRequest{Bucket: new("other")}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("moved the registry's bucket under it: %v", err)
	}
	if err := h.c.RemoveStorage(h.ctx, testSystem, "hetzner"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removed the registry's storage: %v", err)
	}
	st, err := h.c.UpdateStorage(h.ctx, testSystem, "hetzner", apitypes.UpdateStorageRequest{AccessKeyID: new("k2"), SecretAccessKey: new("s2")})
	if err != nil || !slices.Equal(st.UsedBy, []string{"registry"}) {
		t.Fatalf("rotate keys = %+v, %v", st, err)
	}
	if ops := reg.Ops(); ops[len(ops)-1] != "use images:k2" {
		t.Fatalf("new keys didn't reach the registry: %v", ops)
	}
	h.restart()
	if ops := reg.Ops(); ops[len(ops)-1] != "use images:k2" {
		t.Fatalf("restart: ops = %v", ops)
	}

	// A rename leaves the registry alone; it follows by ID.
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "spare", Endpoint: endpoint, Bucket: "images", AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.UpdateStorage(h.ctx, testSystem, "hetzner", apitypes.UpdateStorageRequest{Name: new("spare")}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("renamed onto another connection: %v", err)
	}
	before := len(reg.Ops())
	if st, err := h.c.UpdateStorage(h.ctx, testSystem, "hetzner", apitypes.UpdateStorageRequest{Name: new("hel1")}); err != nil || st.Name != "hel1" || !slices.Equal(st.UsedBy, []string{"registry"}) {
		t.Fatalf("rename = %+v, %v", st, err)
	}
	if r, err := h.c.GetRegistry(h.ctx, testSystem); err != nil || r.Storage != "hel1" || len(reg.Ops()) != before {
		t.Fatalf("after the rename: storage %q, ops %v, %v", r.Storage, reg.Ops()[before:], err)
	}
	if _, b, err := h.c.storageByName(h.ctx, "hel1"); err != nil || b.SecretAccessKey != "s2" {
		t.Fatalf("the rename lost the keys: %+v, %v", b, err)
	}

	if _, err := h.c.MigrateRegistry(h.ctx, testSystem, apitypes.MigrateRegistryRequest{To: "local"}); err != nil {
		t.Fatal(err)
	}
	if r := waitMigrated(t, h); r.Storage != "local" {
		t.Fatalf("back to local: storage %s, %+v", r.Storage, r.Migration)
	}
	if ops := reg.Ops(); !slices.Equal(ops[len(ops)-2:], []string{"migrate to local", "drop images:k2"}) {
		t.Fatalf("ops = %v", ops)
	}
}

// install.sh keeps a new registry's images in a bucket: the connection is
// saved and the registry points at it; running it again changes nothing,
// and another connection is refused once the registry uses one.
func TestInstallRegistryStorage(t *testing.T) {
	h := newHarness(t)
	endpoint := fakeS3(t)
	req := apitypes.CreateStorageRequest{Name: "images", Endpoint: endpoint, Bucket: "images", AccessKeyID: "k", SecretAccessKey: "s"}
	for range 2 {
		st, err := h.c.InstallRegistryStorage(h.ctx, System, req)
		if err != nil || !slices.Equal(st.UsedBy, []string{"registry"}) {
			t.Fatalf("install = %+v, %v", st, err)
		}
	}
	b, err := h.c.RegistryBucket(h.ctx)
	if err != nil || b == nil || b.Name != "images" || b.SecretAccessKey != "s" {
		t.Fatalf("registry bucket = %+v, %v", b, err)
	}
	other := req
	other.Name = "other"
	if _, err := h.c.InstallRegistryStorage(h.ctx, System, other); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("switched the registry's storage on install: %v", err)
	}
}
