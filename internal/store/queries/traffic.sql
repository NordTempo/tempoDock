-- ListTrafficHosts maps the hostnames Caddy proxies to services (redirect
-- domains aren't counted) and their server, for the traffic the minute
-- tick drains. The collector reads it again only when a drained hostname
-- is missing.
-- name: ListTrafficHosts :many
SELECT domains.hostname, domains.service_id, services.environment_id, environments.server_id, environments.project_id,
       projects.org_id, projects.name AS project_name, environments.name AS environment_name, services.name AS service_name
FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE domains.redirect_to = '';

-- AddTraffic adds to a service's point: a new minute, or the hour so far.
-- visitors is the interval's whole estimate, which only grows.
-- name: AddTraffic :exec
INSERT INTO traffic_stats (service_id, res, at, requests, status_2xx, status_3xx, status_4xx, status_5xx, bytes, duration_ms, visitors)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (service_id, res, at) DO UPDATE SET
    requests = requests + excluded.requests,
    status_2xx = status_2xx + excluded.status_2xx,
    status_3xx = status_3xx + excluded.status_3xx,
    status_4xx = status_4xx + excluded.status_4xx,
    status_5xx = status_5xx + excluded.status_5xx,
    bytes = bytes + excluded.bytes,
    duration_ms = duration_ms + excluded.duration_ms,
    visitors = max(visitors, excluded.visitors);

-- name: SetTrafficVisitors :exec
INSERT OR REPLACE INTO traffic_visitors (service_id, at, sketch) VALUES (?, ?, ?);

-- name: AddTrafficPath :exec
INSERT INTO traffic_paths (service_id, at, method, path, requests) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (service_id, at, method, path) DO UPDATE SET requests = requests + excluded.requests;

-- ListHourVisitors is every service's visitor sketch of one hour, which
-- the collector merges new visitors into.
-- name: ListHourVisitors :many
SELECT service_id, sketch FROM traffic_visitors WHERE at = +?;

-- TrimTrafficPaths keeps the 100 most requested paths of each service in
-- an hour that is over.
-- name: TrimTrafficPaths :exec
DELETE FROM traffic_paths
WHERE traffic_paths.at = +sqlc.arg(hour) AND (traffic_paths.service_id, traffic_paths.method, traffic_paths.path) IN (
    SELECT r.service_id, r.method, r.path FROM (
        SELECT p.service_id, p.method, p.path,
               row_number() OVER (PARTITION BY p.service_id ORDER BY p.requests DESC) AS n
        FROM traffic_paths AS p WHERE p.at = +sqlc.arg(hour)
    ) AS r WHERE r.n > 100
);

-- name: PruneTrafficStats :exec
DELETE FROM traffic_stats WHERE res = +? AND at < +?;

-- name: PruneTrafficVisitors :exec
DELETE FROM traffic_visitors WHERE at < +?;

-- name: PruneTrafficPaths :exec
DELETE FROM traffic_paths WHERE at < +?;

-- name: ListTrafficStats :many
SELECT at, requests, status_2xx, status_3xx, status_4xx, status_5xx, bytes, duration_ms, visitors
FROM traffic_stats WHERE service_id = +? AND res = +? AND at >= +? ORDER BY at;

-- TrafficTotals adds up a service's hour points since a time.
-- name: TrafficTotals :one
SELECT CAST(coalesce(sum(requests), 0) AS INTEGER) AS requests,
       CAST(coalesce(sum(status_2xx), 0) AS INTEGER) AS status_2xx,
       CAST(coalesce(sum(status_3xx), 0) AS INTEGER) AS status_3xx,
       CAST(coalesce(sum(status_4xx), 0) AS INTEGER) AS status_4xx,
       CAST(coalesce(sum(status_5xx), 0) AS INTEGER) AS status_5xx,
       CAST(coalesce(sum(bytes), 0) AS INTEGER) AS bytes,
       CAST(coalesce(sum(duration_ms), 0) AS INTEGER) AS duration_ms
FROM traffic_stats WHERE service_id = +? AND res = 3600 AND at >= +?;

-- name: ListTrafficVisitors :many
SELECT sketch FROM traffic_visitors WHERE service_id = +? AND at >= +?;

-- TopTrafficPaths is a service's most requested paths since a time.
-- name: TopTrafficPaths :many
SELECT method, path, CAST(sum(requests) AS INTEGER) AS requests
FROM traffic_paths WHERE service_id = +? AND at >= +?
GROUP BY method, path ORDER BY 3 DESC, path, method LIMIT +?;
