-- Host ports still held (ListUsedHostPorts, ListRetiredPortHolders). A
-- release keeps its port until its containers are removed, whatever its
-- status, so the index covers every release holding a port rather than the
-- live statuses. Few releases hold one, so it stays small; without it every
-- deploy scanned all releases to allocate a port.
CREATE INDEX releases_host_port ON releases (service_id, host_port) WHERE host_port > 0;
