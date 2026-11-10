package apitypes

// `dock connect` (ADR 0023): a local port tunnelled to a service's private
// port. GET /projects/{p}/environments/{e}/services/{s}/connect checks
// access and describes the target; each tunnelled TCP connection is one
// GET .../services/{s}/tunnel with `Connection: Upgrade` and
// `Upgrade: dock-tunnel`, answered with 101 and then raw bytes both ways.

// TunnelProtocol is the Upgrade token of a tunnel request.
const TunnelProtocol = "dock-tunnel"

// TunnelsPerActor is the most tunnel connections one user or token may
// have open at once; past it a tunnel request is 429.
const TunnelsPerActor = 16

// Audit actions of tunnels. Neither carries any of the traffic.
const (
	// AuditDataConnect: a tunnel connection opened (who, which service and
	// port, from which address).
	AuditDataConnect = "data.connect"
	// AuditDataDisconnect: it closed (duration and bytes each way).
	AuditDataDisconnect = "data.disconnect"
)

// ConnectInfo describes what `dock connect` reaches, for a caller who may
// open tunnels to it.
type ConnectInfo struct {
	// Service is "project/environment/service".
	Service string `json:"service"`
	// Port is the container port tunnels reach.
	Port int `json:"port"`
	// Engine is set when the service is a detected database.
	Engine DataEngine `json:"engine,omitempty" enum:"postgres,mysql"`
	// User and Database are the owner's login and database (Postgres),
	// from the active release. Not secret; the password is only in URL.
	User     string `json:"user,omitempty"`
	Database string `json:"database,omitempty"`
	// URL is the connection URL with its credentials, only with
	// reveal=true (audited as data.reveal). Its host is the service's
	// private name; clients point it at their local port.
	URL string `json:"url,omitempty"`
	// Confirm: the environment is production, so tunnels need its name
	// as confirm.
	Confirm bool `json:"confirm"`
	// Access is the caller's data access there, with the steps that
	// decided it.
	Access DataPermission `json:"access"`
}
