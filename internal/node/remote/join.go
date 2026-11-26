package remote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// StateFile in a node's data dir holds what it knows about its control
// plane after joining (0600, written by Join, read by Node.Run; the config
// package calls it NodeStatePath). install.sh takes its presence to mean
// "joined".
const StateFile = "node.toml"

// Link is a node's pinned control plane and its own server identity.
type Link struct {
	ServerID int64  `toml:"server_id"`
	Server   string `toml:"server"`
	// Addrs are the control plane's node addresses, tried in order.
	Addrs []string `toml:"addrs"`
	// ControlPlaneKey is the pinned fingerprint of the control plane's key.
	ControlPlaneKey string `toml:"control_plane_key"`
}

// LoadLink reads StateFile from dataDir.
func LoadLink(dataDir string) (Link, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return Link{}, errors.New("this server hasn't joined a control plane; run `dock node join <token>` first")
	}
	if err != nil {
		return Link{}, err
	}
	var l Link
	if err := toml.Unmarshal(raw, &l); err != nil {
		return Link{}, fmt.Errorf("%s: %w", StateFile, err)
	}
	if len(l.Addrs) == 0 {
		return Link{}, fmt.Errorf("%s has no control plane address; join again", StateFile)
	}
	if _, err := fingerprintBytes(l.ControlPlaneKey); err != nil {
		return Link{}, fmt.Errorf("%s: %w", StateFile, err)
	}
	return l, nil
}

// SetControlPlane points a joined node at a control plane that moved
// (ADR 0021): StateFile gets addr as its only address, the pinned key and
// the server identity stay. A running node dials it from its next
// connection attempt on, so no restart and no re-join are needed.
func SetControlPlane(dataDir, addr string) (Link, error) {
	l, err := LoadLink(dataDir)
	if err != nil {
		return Link{}, err
	}
	l.Addrs = []string{addr}
	raw, err := toml.Marshal(l)
	if err != nil {
		return Link{}, err
	}
	return l, writeFileAtomic(filepath.Join(dataDir, StateFile), raw)
}

// joinWait bounds a whole join, dial to answer.
const joinWait = 30 * time.Second

// Join joins this server to the control plane in token (ADR 0014
// section 2) and writes NodeKeyFile and StateFile to dataDir. It creates a
// new key, dials the token's addresses pinning the control plane's
// fingerprint (an impostor fails the TLS handshake, before the secret is
// sent), answers the control plane's join call with the secret and facts,
// and receives the server's ID and name. Afterwards, Node.Run connects with
// the new key.
func Join(ctx context.Context, token, dataDir string, facts Facts) (Link, error) {
	t, err := ParseJoinToken(token)
	if err != nil {
		return Link{}, err
	}
	id, err := NewIdentity()
	if err != nil {
		return Link{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, joinWait)
	defer cancel()
	conn, _, err := dialControlPlane(ctx, t.Addrs, id, t.ControlPlane, joinALPN, DefaultKeepAlive)
	if err != nil {
		var pe *pinError
		if errors.As(err, &pe) {
			return Link{}, fmt.Errorf("refusing to join: %w", pe)
		}
		if remoteAlert(err, 120) {
			return Link{}, errVersion
		}
		return Link{}, unreachable(t.Addrs, err)
	}

	answer := make(chan joinedRequest, 1)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/"+opJoin, unary(func(ctx context.Context, _ empty) (joinResult, error) {
		h, err := gatherHello(ctx, func(context.Context) (Facts, error) { return facts, nil })
		return joinResult{Hello: h, Secret: t.Secret[:]}, err
	}))
	mux.Handle("POST /v1/"+opJoined, unary(func(_ context.Context, a joinedRequest) (empty, error) {
		select {
		case answer <- a:
		default:
		}
		return empty{}, nil
	}))
	served := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = serveConn(ctx, conn, mux, DefaultKeepAlive, slog.New(slog.DiscardHandler))
		close(served)
	}()

	var a joinedRequest
	select {
	case a = <-answer:
		// The control plane closes the connection once it has our reply.
		<-served
	case <-served:
		select {
		case a = <-answer:
		default:
			err := serveErr
			if remoteAlert(err, 42) {
				return Link{}, errors.New("the control plane refused the join connection (too many attempts?); try again in a minute or see its logs")
			}
			if err == nil {
				err = errors.New("see its logs")
			}
			return Link{}, fmt.Errorf("the control plane closed the connection during the join: %w", err)
		}
	}
	if a.Error != "" {
		return Link{}, fmt.Errorf("join refused: %s", a.Error)
	}
	link := Link{ServerID: a.ServerID, Server: a.Server, Addrs: t.Addrs, ControlPlaneKey: t.ControlPlane}
	if err := saveJoin(dataDir, id, link); err != nil {
		return Link{}, fmt.Errorf("joined as %s, but saving the key failed (remove the server and join again): %w", link.Server, err)
	}
	return link, nil
}

// unreachable is a join that reached none of the control plane's
// addresses, with what to do about it.
func unreachable(addrs []string, err error) error {
	return fmt.Errorf("can't reach the control plane at %s. Open that port on the control plane's firewall, or put this server on the same Tailscale network or LAN: %w",
		strings.Join(addrs, " or "), err)
}

// CheckReach dials the addresses in token over TCP and returns the first
// that answers, without a handshake: install.sh runs it (`dock node join
// --check`) before installing anything, so a server that can't reach the
// control plane fails in seconds. The token stays unused.
func CheckReach(ctx context.Context, token string) (string, error) {
	t, err := ParseJoinToken(token)
	if err != nil {
		return "", err
	}
	var errs []error
	for i, addr := range t.Addrs {
		timeout := dialFirstTimeout
		if i == len(t.Addrs)-1 {
			timeout = dialLastTimeout
		}
		c, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = c.Close()
			return addr, nil
		}
		errs = append(errs, err)
	}
	return "", unreachable(t.Addrs, errors.Join(errs...))
}

// saveJoin writes the node's key and link, replacing any earlier join.
func saveJoin(dataDir string, id Identity, link Link) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, err := toml.Marshal(link)
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dataDir, nextKeyFile))
	if err := id.save(filepath.Join(dataDir, NodeKeyFile)); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dataDir, StateFile), raw)
}
