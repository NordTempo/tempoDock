package core

import "github.com/NordTempo/tempoDock/internal/datapanel"

// Core's database panel operations are the shared datapanel.Ops (ADR
// 0024): the API's data handlers and the MCP data tools are written
// against that interface, so tempoBase serves the same operations.
var _ datapanel.Ops[Actor, ServiceRef] = (*Core)(nil)
