package tempobase

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/secrets"
)

// Sealed values are bound to where they are stored (ADR 0041). A
// connection's URL is bound to its name, so a rename seals it again; the
// role passwords to the connection's ID (datapanel.AccessColumns).
var (
	sealedConnURL                      = secrets.Column{Table: "connections", Name: "url_secret", Key: []string{"name"}}
	sealedDataReader, sealedDataWriter = datapanel.AccessColumns("connection_id")

	// sealedColumns is every sealed column of the store, for resealSecrets.
	sealedColumns = []secrets.Column{sealedConnURL, sealedDataReader, sealedDataWriter}
)

// secretsBound is install.secrets_format once every value is bound.
const secretsBound = 2

// resealSecrets binds the values sealed before ADR 0041 to where they are
// stored, once per database, then has the box refuse unbound values. Open
// runs it before anything reads a value; it is idempotent and safe to
// interrupt (secrets.Box.ResealColumns).
func (a *App) resealSecrets(ctx context.Context) error {
	format, err := a.store.GetSecretsFormat(ctx)
	if err != nil {
		return err
	}
	if format < secretsBound {
		if err := a.box.ResealColumns(ctx, a.store.DB, sealedColumns, a.log); err != nil {
			return err
		}
		if err := a.store.SetSecretsFormat(ctx, secretsBound); err != nil {
			return err
		}
	}
	a.box.RefuseUnbound()
	return nil
}
