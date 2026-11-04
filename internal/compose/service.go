package compose

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// mappedKeys are the service keys convertService maps (or warns about
// itself, value by value).
var mappedKeys = []string{
	"image", "build", "environment", "env_file", "ports", "expose", "volumes", "depends_on",
	"healthcheck", "command", "entrypoint", "mem_limit", "cpus", "deploy", "restart",
}

// unsupportedKeys are Compose service keys Dock can't apply, with what
// happens instead ("" for the generic sentence).
var unsupportedKeys = map[string]string{
	"privileged":   "Privileged mode isn't supported, so the container runs unprivileged.",
	"cap_add":      "Added capabilities aren't supported, so the container runs with Docker's default capabilities.",
	"cap_drop":     "Dropped capabilities aren't applied, so the container runs with Docker's default capabilities.",
	"devices":      "Host devices can't be passed to a Dock service, so devices isn't applied.",
	"user":         "user isn't applied, so the container runs as the image's user. Set USER in the Dockerfile to change it.",
	"working_dir":  "working_dir isn't applied. Set WORKDIR in the Dockerfile, or cd in the start command.",
	"tmpfs":        "tmpfs mounts aren't supported, so files written there go to the container's own filesystem and are lost on the next deploy.",
	"scale":        "Dock runs one instance of a service, so scale isn't applied.",
	"volumes_from": "volumes_from isn't supported: Dock volumes belong to one service. Give this service its own volume.",
	"ulimits":      "ulimits aren't applied, so the container runs with the server's default limits. If it needs more open files, raise default-ulimits in the server's Docker daemon settings.",
	"init":         "init isn't applied, so the service's command runs as PID 1 without an init process. If it leaves zombie processes or ignores stop signals, add tini to the image as its entrypoint.",
	// 30s is core's stopTimeout.
	"stop_grace_period": "stop_grace_period isn't applied: Dock gives a container 30s to stop before it kills it.",
	"shm_size":          "shm_size isn't applied, so /dev/shm has Docker's default 64 MB. Services that need more (browsers, some databases) may fail; write to a volume instead.",
	"extra_hosts":       "extra_hosts isn't applied, so those hostnames don't resolve in the container. Use the service names of the environment, or full hostnames in variables.",
	"pid":               "", "ipc": "", "security_opt": "", "sysctls": "",
	"dns": "", "dns_search": "", "dns_opt": "", "platform": "",
	"stop_signal": "", "read_only": "", "group_add": "", "runtime": "",
	"cgroup": "", "cgroup_parent": "", "cpu_count": "", "cpu_percent": "", "cpu_period": "",
	"cpu_quota": "", "cpu_rt_period": "", "cpu_rt_runtime": "", "cpu_shares": "", "cpuset": "",
	"blkio_config": "", "credential_spec": "", "device_cgroup_rules": "", "isolation": "",
	"mac_address": "", "mem_reservation": "", "mem_swappiness": "", "memswap_limit": "",
	"oom_kill_disable": "", "oom_score_adj": "", "pids_limit": "", "storage_opt": "",
	"userns_mode": "", "uts": "", "gpus": "", "develop": "", "post_start": "", "pre_stop": "",
	"annotations": "", "attach": "", "label_file": "", "use_api_socket": "", "models": "", "provider": "",
}

// ignoredKeys are Compose service keys Dock ignores because it does the
// job itself, with the reason; %[1]s stands for the service's Dock name.
var ignoredKeys = map[string]string{
	"container_name": "Dock names containers itself, so container_name is ignored. Other services in the environment reach this one at %[1]s.",
	"hostname":       "Dock sets the hostname, so hostname is ignored. Other services in the environment reach this one at %[1]s.",
	"domainname":     "domainname is ignored. Other services in the environment reach this one at %[1]s.",
	"networks":       "Dock gives every environment one network, and services reach each other by service name, so networks is ignored.",
	"links":          "links are ignored: services in an environment reach each other by service name.",
	"external_links": "external_links are ignored: services in an environment reach each other by service name.",
	"logging":        "Dock keeps every service's logs itself (`dock logs`), so the logging settings are ignored.",
	"pull_policy":    "Dock decides when to pull images, so pull_policy is ignored.",
	"stdin_open":     "Dock services don't get a terminal, so stdin_open is ignored.",
	"tty":            "Dock services don't get a terminal, so tty is ignored.",
}

// convertService maps one service's settings.
func (c *converter) convertService(s *svc) {
	d := s.def
	c.serviceKeys(s)
	template := c.source(s)
	c.environment(s)
	c.ports(s, template)
	c.portVariable(s)
	if template == apitypes.TemplateNone {
		c.mounts(s)
		c.healthcheck(s)
		c.command(s)
	} else {
		c.templateSettings(s)
	}
	c.dependsOn(s)
	c.resources(s)
	if d.Restart.set {
		switch r := c.str(s, "restart", d.Restart); {
		case r == "no":
			c.warn(s, "restart", "Dock restarts services that stop, so restart: no isn't applied. If this is a one-off task (a migration), run it in the start command of the service that needs it instead.")
		case strings.HasPrefix(r, "on-failure:"):
			c.warn(s, "restart", "Dock restarts a service every time it stops, so the limit in restart: %s isn't applied.", r)
		}
	}
}

// serviceKeys warns about every key that isn't mapped.
func (c *converter) serviceKeys(s *svc) {
	for _, k := range s.def.keys {
		if slices.Contains(mappedKeys, k) || strings.HasPrefix(k, "x-") {
			continue
		}
		if msg, ok := ignoredKeys[k]; ok {
			c.warn(s, k, "%s", strings.ReplaceAll(msg, "%[1]s", s.name))
			continue
		}
		msg, ok := unsupportedKeys[k]
		switch {
		case k == "labels":
			c.labels(s)
		case k == "secrets":
			c.warn(s, k, "Compose secrets (files under /run/secrets) aren't supported, so %s %s mounted. Set each value as a secret variable with `dock env set -s %s NAME=...`, and have the service read it from its environment instead of a file.",
				joinNames(s.def.Secrets), isntArent(len(s.def.Secrets)), s.name)
		case k == "configs":
			c.warn(s, k, "Compose configs (files mounted into the container) aren't supported, so %s %s mounted. Build the files into the image with COPY in a Dockerfile instead.",
				joinNames(s.def.Configs), isntArent(len(s.def.Configs)))
		case k == "profiles":
			c.warn(s, k, "Compose starts this service only with the profile %s (`docker compose --profile %s up`). Dock has no profiles, so it is imported and always runs; archive it with `dock service archive %s` if you don't need it.",
				strings.Join(s.def.Profiles, " or "), firstOr(s.def.Profiles, "<name>"), s.name)
		case k == "extends":
			c.warn(s, k, "extends isn't supported, so the settings this service inherits from %s aren't converted. Copy them into the service and import again.", extendsTarget(s.def.Extends))
		case k == "network_mode":
			mode := c.str(s, k, s.def.NetworkMode)
			c.warn(s, k, "network_mode %s isn't supported: every Dock service runs on its environment's network and reaches the others by service name. Set the service's port to the one it listens on.", mode)
		case ok && msg != "":
			c.warn(s, k, "%s", msg)
		case ok:
			c.warn(s, k, "%s isn't supported by Dock, so it is not applied.", k)
		default:
			c.warn(s, k, "%s isn't a Compose key Dock knows, so it is ignored. Check it for a typo.", k)
		}
	}
}

// source maps image and build, and recognises the database templates. It
// returns the service's template.
func (c *converter) source(s *svc) apitypes.Template {
	d := s.def
	image := ""
	if d.Image.set {
		image = c.str(s, "image", d.Image)
	}
	if d.Build != nil {
		if image != "" {
			c.warn(s, "image", "The service has both build and image; Dock builds it from source, so the image name %s isn't used.", image)
		}
		c.build(s)
		return apitypes.TemplateNone
	}
	if image == "" {
		c.warn(s, "image", "The service has no image and no build, so it has nothing to run. Set its image with `dock service update --image` after importing.")
		return apitypes.TemplateNone
	}
	ref := parseImage(image)
	switch ref.repo {
	case "postgres":
		s.out.Template = apitypes.TemplatePostgres
		c.warn(s, "image", "%s becomes Dock's postgres template, which brings its own volume, port, health check and backups, so the service's ports, volumes, health check and command aren't used. Data in the Compose volumes isn't moved: the new database starts empty.", image)
		if ref.major() != "17" {
			c.warn(s, "image", "The postgres template runs PostgreSQL 17, and the Compose file uses %s. To move data from the old database, dump it with pg_dump and restore it into the new one.", image)
		}
	case "redis":
		s.out.Template = apitypes.TemplateRedis
		c.warn(s, "image", "%s becomes Dock's redis template, which brings its own volume, port, health check and backups, so the service's ports, volumes, health check and command aren't used.", image)
		c.warn(s, "image", "The redis template requires a password (REDIS_PASSWORD, generated). ${{%[1]s.REDIS_URL}} includes it; services that connect by host and port need ${{%[1]s.REDIS_PASSWORD}} as their password.", s.name)
	default:
		s.out.Image = image
		c.databaseImage(s, ref)
	}
	return s.out.Template
}

// pgCompatible are PostgreSQL images with extensions, by repository name:
// they stay image services, since the template runs plain PostgreSQL.
var pgCompatible = []string{"pgvecto-rs", "pgvector", "postgis", "timescaledb", "timescaledb-ha", "vchord-postgres", "postgresql", "postgres"}

// otherDatabases are database images Dock has no template for.
var otherDatabases = []string{"mysql", "mariadb", "mongo", "mongodb", "clickhouse-server"}

// databaseImage warns that a database image that isn't a template runs as
// a plain image service, without Dock's backups.
func (c *converter) databaseImage(s *svc, ref imageRef) {
	switch name := ref.name(); {
	case slices.Contains(pgCompatible, name):
		c.warn(s, "image", "%s is PostgreSQL with extensions, not Dock's postgres template, so it stays an image service with its own volume: Dock doesn't back it up and the data panel doesn't open it. Back it up yourself with pg_dump, or switch to the postgres template if the app doesn't need the extensions.", s.out.Image)
	case slices.Contains(otherDatabases, name):
		c.warn(s, "image", "Dock has no template for %s, so it runs as an image service with its own volume, and Dock doesn't back it up. Back it up yourself.", s.out.Image)
	}
}

// build makes a source service: no image, a Dockerfile path inside the
// context, and a Build entry saying where to deploy it from.
func (c *converter) build(s *svc) {
	b := s.def.Build
	ctx := "."
	if b.Context.set {
		ctx = c.str(s, "build.context", b.Context)
	}
	dockerfile := "Dockerfile"
	if b.Dockerfile.set {
		dockerfile = c.str(s, "build.dockerfile", b.Dockerfile)
	}
	if dockerfile != "Dockerfile" {
		if clean := path.Clean(dockerfile); path.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
			c.warn(s, "build.dockerfile", "The Dockerfile %s is outside the build context; Dock reads it from inside the context, so move it there and set it with `dock service update --dockerfile`.", dockerfile)
		} else {
			s.out.DockerfilePath = clean
		}
	}
	if strings.Contains(ctx, "://") || strings.HasPrefix(ctx, "git@") {
		c.warn(s, "build.context", "The build context %s is a git repository. Connect the service to it with `dock git connect`, or deploy a checkout with `dock deploy`.", ctx)
	}
	s.build = &Build{Service: s.name, Context: ctx, Dockerfile: dockerfile}
	for _, k := range b.keys {
		switch {
		case k == "context", k == "dockerfile", strings.HasPrefix(k, "x-"):
		case k == "args":
			c.warn(s, "build.args", "Build arguments aren't set separately: Dock passes the service's variables to the build as build arguments, so set any the build needs as variables.")
		case k == "target":
			c.warn(s, "build.target", "Build targets aren't supported, so Dock builds the Dockerfile's last stage. Move the stage you want to the end.")
		default:
			c.warn(s, "build."+k, "build.%s isn't supported by Dock, so it is not applied.", k)
		}
	}
}

// templateSettings handles a template service's settings: the template
// replaces its command, volumes and health check. A Redis password in the
// command is remembered so other services' copies become references.
func (c *converter) templateSettings(s *svc) {
	if s.out.Template != apitypes.TemplateRedis {
		return
	}
	words := s.def.Command.list
	if !s.def.Command.isList {
		words = shellWords(s.def.Command.text)
	}
	for i, w := range words {
		w, _ = interpolate(w, c.lookup)
		if v, ok := strings.CutPrefix(w, "--requirepass="); ok {
			s.redisPassword = v
		} else if w == "--requirepass" && i+1 < len(words) {
			s.redisPassword, _ = interpolate(words[i+1], c.lookup)
		}
	}
}

// candidate is a port that could be the service's port.
type candidate struct {
	portSpec
	fromPorts bool
}

// nonHTTPPorts are well-known ports that don't speak HTTP.
var nonHTTPPorts = []int{
	21, 22, 25, 53, 110, 143, 389, 465, 587, 636, 993, 995, 1433, 1521, 1883, 2181, 3306, 4222,
	5432, 5671, 5672, 6379, 6380, 8883, 9042, 9092, 11211, 27017, 61616,
}

// nonHTTPImages are images (by repository name) that don't speak HTTP.
var nonHTTPImages = []string{
	"mysql", "mariadb", "mongo", "mongodb", "rabbitmq", "memcached", "postgres", "postgresql", "postgis",
	"redis", "redis-stack-server", "valkey", "keydb", "mosquitto", "eclipse-mosquitto", "nats", //nolint:misspell // image names
	"zookeeper", "kafka", "cp-kafka", "cassandra",
}

// ports picks the service's port from ports and expose, and gives a
// service that publishes an HTTP port a generated domain.
func (c *converter) ports(s *svc, template apitypes.Template) {
	var cands []candidate
	for _, p := range s.def.Ports {
		spec, err := c.port(s, p)
		if err != nil {
			c.warn(s, "ports", "%s, so it is skipped.", upperFirst(err.Error()))
			continue
		}
		cands = append(cands, candidate{spec, true})
	}
	for _, e := range s.def.Expose {
		spec, err := parseExpose(c.str(s, "expose", e))
		if err != nil {
			c.warn(s, "expose", "%s, so it is skipped.", upperFirst(err.Error()))
			continue
		}
		cands = append(cands, candidate{spec, false})
	}
	if template != apitypes.TemplateNone {
		return
	}
	// The port is the first TCP one, preferring ports that aren't
	// well-known non-HTTP ones (Pi-hole publishes DNS before its web UI).
	var chosen *candidate
	for i := range cands {
		p := &cands[i]
		key := "ports"
		if !p.fromPorts {
			key = "expose"
		}
		if p.protocol != "tcp" {
			c.warn(s, key, "Port %s is %s, and Dock routes only HTTP from outside, so it isn't published. Other services in the environment can still reach it at %s:%d.", p.text, strings.ToUpper(p.protocol), s.name, p.target)
			continue
		}
		if p.targetEnd != 0 {
			c.warn(s, key, "Port range %s isn't supported; only port %d is considered.", p.text, p.target)
		}
		if chosen == nil || slices.Contains(nonHTTPPorts, chosen.target) && !slices.Contains(nonHTTPPorts, p.target) {
			chosen = p
		}
	}
	if chosen == nil {
		return
	}
	s.out.Port = chosen.target
	var unused []string
	for _, p := range cands {
		if t := strconv.Itoa(p.target); p.protocol == "tcp" && p.target != chosen.target && !slices.Contains(unused, t) {
			unused = append(unused, t)
		}
	}
	switch len(unused) {
	case 0:
	case 1:
		c.warn(s, "ports", "A Dock service receives traffic on one port, so %d is used and %s isn't. Other services in the environment can still reach it at %s:%s.", chosen.target, unused[0], s.name, unused[0])
	default:
		c.warn(s, "ports", "A Dock service receives traffic on one port, so %d is used and %s aren't. Other services in the environment can still reach them at %s:<port>.", chosen.target, strings.Join(unused, ", "), s.name)
	}
	if !chosen.fromPorts || !chosen.published {
		return
	}
	switch {
	case isLoopback(chosen.hostIP):
		c.warn(s, "ports", "Port %s is published only on the host's loopback (%s) in Compose, so it isn't public there either, and the service gets no domain. Other services reach it at %s:%d; to make it public, add a domain with `dock domain add <hostname> -s %s`.", chosen.text, chosen.hostIP, s.name, chosen.target, s.name)
	case slices.Contains(nonHTTPPorts, chosen.target) || slices.Contains(nonHTTPImages, parseImage(s.out.Image).name()):
		c.warn(s, "ports", "Port %s is published in Compose, but Dock routes only HTTP publicly, so it isn't reachable from outside. Other services in the environment reach it at %s:%d.", chosen.text, s.name, chosen.target)
	default:
		s.out.Domains = []apitypes.DomainExport{{Generated: true}}
		c.warn(s, "ports", "Dock doesn't publish host ports, so %s isn't opened on the server. HTTP reaches port %d through a generated domain when the server has a base domain; add your own with `dock domain add <hostname> -s %s`.", chosen.text, chosen.target, s.name)
	}
}

// port parses one ports entry, in either form.
func (c *converter) port(s *svc, p port) (portSpec, error) {
	if p.long == nil {
		return parsePort(c.str(s, "ports", p.short))
	}
	l := p.long
	spec := portSpec{protocol: "tcp", published: l.Published.set, hostIP: c.str(s, "ports", l.HostIP)}
	target := c.str(s, "ports", l.Target)
	spec.text = target
	if spec.published {
		spec.text = c.str(s, "ports", l.Published) + ":" + target
	}
	if l.Protocol.set {
		spec.protocol = strings.ToLower(c.str(s, "ports", l.Protocol))
		spec.text += "/" + spec.protocol
	}
	for _, k := range l.keys {
		switch k {
		case "target", "published", "protocol", "host_ip", "mode", "name", "app_protocol":
		default:
			c.warn(s, "ports", "%s isn't a port key Dock knows, so it is ignored.", k)
		}
	}
	var err error
	spec.target, spec.targetEnd, err = parsePortRange(target)
	if err != nil {
		return spec, fmt.Errorf("port %q: %w", spec.text, err)
	}
	return spec, nil
}

// isLoopback reports whether a host address is a loopback one.
func isLoopback(ip string) bool {
	return ip == "localhost" || ip == "::1" || strings.HasPrefix(ip, "127.")
}

// mounts maps named volumes, and anonymous ones to named volumes after
// their path; bind mounts and tmpfs are warned about and not mounted.
func (c *converter) mounts(s *svc) {
	type named struct{ name, target string }
	var vols []named
	for _, m := range s.def.Volumes {
		var typ, source, target string
		readOnly := false
		if m.long == nil {
			text := c.str(s, "volumes", m.short)
			parts := strings.Split(text, ":")
			switch len(parts) {
			case 1:
				target = parts[0]
			default:
				source, target = parts[0], parts[1]
				if len(parts) > 2 {
					readOnly = slices.Contains(strings.Split(parts[2], ","), "ro")
				}
			}
			switch {
			case source == "":
				typ = "anonymous"
			case strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~"):
				typ = "bind"
			default:
				typ = "volume"
			}
		} else {
			l := m.long
			typ, source, target = c.str(s, "volumes", l.Type), c.str(s, "volumes", l.Source), c.str(s, "volumes", l.Target)
			readOnly = l.ReadOnly.set && isTrue(l.ReadOnly.text)
			if typ == "volume" && source == "" {
				typ = "anonymous"
			}
			for _, k := range l.keys {
				switch k {
				case "type", "source", "target", "read_only", "volume", "bind", "tmpfs", "consistency":
				default:
					c.warn(s, "volumes", "%s isn't a volume key Dock knows, so it is ignored.", k)
				}
			}
		}
		switch typ {
		case "volume":
		case "bind":
			c.bindMount(s, source, target, readOnly)
			continue
		case "tmpfs":
			c.warn(s, "volumes", "The tmpfs mount at %s isn't supported, so files written there go to the container's own filesystem and are lost on the next deploy.", target)
			continue
		case "anonymous":
			// Dock never makes anonymous volumes (they leak, one per
			// container): the files get a named volume of their own.
			source = path.Base(path.Clean(target))
			c.warn(s, "volumes", "The anonymous volume at %s becomes a named volume, %s, so its files are kept across deploys.", target, source)
		default:
			c.warn(s, "volumes", "The %s mount at %s isn't supported, so it isn't mounted.", typ, target)
			continue
		}
		if !path.IsAbs(target) || path.Clean(target) == "/" {
			c.warn(s, "volumes", "Volume %s is mounted at %q, and Dock needs an absolute path other than /, so it isn't mounted.", source, target)
			continue
		}
		if readOnly {
			c.warn(s, "volumes", "Volume %s is read-only in Compose; Dock mounts it read-write.", source)
		}
		vols = append(vols, named{source, path.Clean(target)})
	}
	sources := make([]string, 0, len(vols))
	for _, v := range vols {
		if !slices.Contains(sources, v.name) {
			sources = append(sources, v.name)
		}
	}
	names := assignNames(sources, "data", nil)
	var used, paths []string
	for _, v := range vols {
		name := names[v.name]
		switch {
		case slices.Contains(used, name):
			c.warn(s, "volumes", "Volume %s is mounted more than once; Dock mounts it once, at %s.", v.name, s.volumePath(name))
			continue
		case slices.Contains(paths, v.target):
			c.warn(s, "volumes", "Two volumes are mounted at %s; only the first is kept.", v.target)
			continue
		}
		if name != v.name {
			c.warn(s, "volumes", "Volume %q is named %q in Dock: volume names are 1-32 lowercase letters, digits or dashes.", v.name, name)
		}
		used, paths = append(used, name), append(paths, v.target)
		s.mounts = append(s.mounts, v.name)
		s.out.Volumes = append(s.out.Volumes, apitypes.VolumeExport{Name: name, MountPath: v.target})
	}
	slices.SortFunc(s.out.Volumes, func(a, b apitypes.VolumeExport) int { return strings.Compare(a.Name, b.Name) })
}

// bindMount warns that a bind mount isn't mounted, with what to do for
// the kind of files it holds: the host's time zone or Docker socket, a
// config file, or a data directory whose files would be lost.
func (c *converter) bindMount(s *svc, source, target string, readOnly bool) {
	mount := source + ":" + target
	switch base := path.Base(target); {
	case target == "/etc/localtime" || target == "/etc/timezone":
		c.warn(s, "volumes", "The bind mount %s isn't mounted, so the container keeps the image's time zone (usually UTC). Set a TZ variable with `dock env set -s %s TZ=Europe/Oslo` if the image reads it.", mount, s.name)
	case strings.HasSuffix(source, "docker.sock"):
		c.warn(s, "volumes", "The bind mount %s isn't mounted: Dock doesn't give services the server's Docker socket. Tools that manage containers through it don't work on Dock.", mount)
	case readOnly || strings.Contains(strings.TrimPrefix(base, "."), "."):
		c.warn(s, "volumes", "The bind mount %s isn't mounted: Dock mounts only named volumes, not files from the server. Build the files into the image with COPY in a Dockerfile instead.", mount)
	default:
		c.warn(s, "volumes", "The bind mount %s isn't mounted: Dock mounts only named volumes, so files written to %s are lost on the next deploy. Add a volume there with `dock volume add <name> %s -s %s`, and copy existing files into it.", mount, target, target, s.name)
	}
}

// volumePath is where the service mounts a volume, or "".
func (s *svc) volumePath(name string) string {
	for _, v := range s.out.Volumes {
		if v.Name == name {
			return v.MountPath
		}
	}
	return ""
}

// dependsOn maps depends_on to Dock names.
func (c *converter) dependsOn(s *svc) {
	for _, dep := range s.def.DependsOn {
		other := c.service(dep.service)
		if other == nil {
			c.warn(s, "depends_on", "The service depends on %s, which isn't in the Compose file, so that is ignored.", dep.service)
			continue
		}
		if dep.condition == "service_completed_successfully" {
			c.warn(s, "depends_on", "Dock has no one-shot services, so waiting for %s to complete isn't supported; it only starts first. Run one-off tasks in the start command instead.", other.name)
		}
		if !slices.Contains(s.out.DependsOn, other.name) {
			s.out.DependsOn = append(s.out.DependsOn, other.name)
		}
	}
	slices.Sort(s.out.DependsOn)
}

// defaultInterval and defaultRetries are Compose's health check defaults.
const (
	defaultInterval = 30 * time.Second
	defaultRetries  = 3
	maxTimeout      = 3600
)

// healthcheck maps an HTTP health check to the health check path, and
// start_period to the timeout.
func (c *converter) healthcheck(s *svc) {
	h := s.def.Healthcheck
	if h == nil {
		return
	}
	for _, k := range h.keys {
		switch k {
		case "test", "interval", "timeout", "retries", "start_period", "start_interval", "disable":
		default:
			if !strings.HasPrefix(k, "x-") {
				c.warn(s, "healthcheck", "%s isn't a health check key Dock knows, so it is ignored.", k)
			}
		}
	}
	if h.Disable.set && isTrue(h.Disable.text) || !h.Test.set {
		return
	}
	words := h.Test.list
	shell := ""
	if !h.Test.isList {
		shell = h.Test.text
	} else if len(words) > 0 {
		switch strings.ToUpper(words[0]) {
		case "NONE":
			return
		case "CMD-SHELL":
			shell = strings.Join(words[1:], " ")
			words = nil
		case "CMD":
			words = words[1:]
		}
	}
	shell, _ = interpolate(shell, c.lookup)
	for i, w := range words {
		words[i], _ = interpolate(w, c.lookup)
	}
	if shell != "" {
		words = shellWords(shell)
	}
	if len(words) == 3 && slices.Contains([]string{"sh", "/bin/sh", "bash", "/bin/bash"}, words[0]) && words[1] == "-c" {
		shell = words[2]
		words = shellWords(shell)
	}
	desc := shell
	if desc == "" {
		desc = shellJoin(words)
	}
	checkPath, port, ok := httpCheck(words)
	if !ok {
		c.warn(s, "healthcheck", "The health check (%s) isn't used: Dock checks an HTTP path on the service's port. Set one with `dock service update --health-check-path` if the service has one.", desc)
		return
	}
	s.out.HealthCheckPath = checkPath
	switch {
	case port == 0:
	case s.out.Port == 0:
		s.out.Port = port
	case port != s.out.Port:
		c.warn(s, "healthcheck", "The health check uses port %d, and Dock checks the service's port, %d.", port, s.out.Port)
	}
	if !h.StartPeriod.set {
		return
	}
	start, err := time.ParseDuration(c.str(s, "healthcheck.start_period", h.StartPeriod))
	if err != nil {
		c.warn(s, "healthcheck", "start_period %q isn't a duration like 30s or 1m30s, so it isn't used.", h.StartPeriod.text)
		return
	}
	interval := defaultInterval
	if h.Interval.set {
		if d, err := time.ParseDuration(c.str(s, "healthcheck.interval", h.Interval)); err == nil {
			interval = d
		}
	}
	retries := defaultRetries
	if h.Retries.set {
		if n, err := strconv.Atoi(c.str(s, "healthcheck.retries", h.Retries)); err == nil && n > 0 {
			retries = n
		}
	}
	total := start + interval*time.Duration(retries)
	s.out.HealthCheckTimeoutSeconds = min(maxTimeout, int((total+time.Second-1)/time.Second))
}

// httpCheck recognises an HTTP GET by curl or wget of a local URL and
// returns its path and port (0: not given, or not a number).
func httpCheck(words []string) (checkPath string, port int, ok bool) {
	if len(words) < 2 {
		return "", 0, false
	}
	tool := path.Base(words[0])
	if tool != "curl" && tool != "wget" {
		return "", 0, false
	}
	// valueFlags take the next word as their value.
	valueFlags := map[string][]string{
		"curl": {"-o", "--output", "-w", "--write-out", "-H", "--header", "-m", "--max-time", "--connect-timeout",
			"-u", "--user", "--retry", "--retry-delay", "-A", "--user-agent", "-e", "--referer", "-x", "--proxy", "--resolve"},
		"wget": {"-O", "-o", "-T", "-t", "--tries", "--timeout", "-U", "--user-agent", "--header", "-e"},
	}
	var target string
	for i := 1; i < len(words); i++ {
		w := words[i]
		flag, _, _ := strings.Cut(w, "=")
		switch {
		case tool == "curl" && (w == "-X" || w == "--request"):
			if i+1 < len(words) && !strings.EqualFold(words[i+1], "GET") {
				return "", 0, false
			}
			i++
		case tool == "curl" && (strings.HasPrefix(w, "-d") || strings.HasPrefix(w, "--data") || w == "-F" || w == "--form" || w == "-T"),
			tool == "wget" && (flag == "--post-data" || flag == "--post-file" || flag == "--method" && !strings.EqualFold(w, "--method=GET")):
			return "", 0, false
		case slices.Contains(valueFlags[tool], w):
			i++
		case strings.HasPrefix(w, "-"):
		case target == "":
			target = w
		}
	}
	if target == "" {
		return "", 0, false
	}
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	u, ok := splitURL(target)
	if !ok || u.scheme != "http" {
		return "", 0, false
	}
	switch u.host {
	case "localhost", "127.0.0.1", "0.0.0.0", "[::1]":
	default:
		return "", 0, false
	}
	if u.port != "" {
		port, _ = strconv.Atoi(u.port)
	} else {
		port = 80
	}
	checkPath, _, _ = strings.Cut(u.rest, "#")
	if !strings.HasPrefix(checkPath, "/") {
		checkPath = "/" + checkPath
	}
	return checkPath, port, true
}

// command maps command and entrypoint to the start command, which Dock
// runs with `sh -c` after the image's own entrypoint.
func (c *converter) command(s *svc) {
	cmd := c.commandLine(s, "command", s.def.Command)
	if s.def.Entrypoint.set {
		entry := c.commandLine(s, "entrypoint", s.def.Entrypoint)
		cmd = strings.TrimSpace(entry + " " + cmd)
		c.warn(s, "entrypoint", "Dock keeps the image's own entrypoint and runs the start command after it, so this entrypoint runs inside it instead of replacing it. Check that the image's entrypoint runs its arguments as a command.")
	}
	s.out.StartCommand = cmd
}

// commandLine renders a command or entrypoint as one shell line.
func (c *converter) commandLine(s *svc, key string, cmd command) string {
	if !cmd.set {
		return ""
	}
	if !cmd.isList {
		return c.str(s, key, scalar{text: cmd.text, set: true})
	}
	words := make([]string, len(cmd.list))
	for i, w := range cmd.list {
		words[i] = c.str(s, key, scalar{text: w, set: true})
	}
	return shellJoin(words)
}

// resources maps memory and CPU limits and warns about the rest of deploy.
func (c *converter) resources(s *svc) {
	d := s.def
	memory, cpus := d.MemLimit, d.CPUs
	memKey, cpuKey := "mem_limit", "cpus"
	if dep := d.Deploy; dep != nil {
		for _, k := range dep.keys {
			key := "deploy." + k
			switch k {
			case "resources":
			case "replicas":
				if n := c.str(s, key, dep.Replicas); n != "1" {
					c.warn(s, key, "Dock runs one instance of a service, so replicas: %s isn't applied.", n)
				}
			case "mode":
				if m := c.str(s, key, dep.Mode); m != "replicated" {
					c.warn(s, key, "Dock runs one instance of a service, so mode: %s isn't applied.", m)
				}
			case "restart_policy":
				c.warn(s, key, "Dock restarts services that stop, so deploy.restart_policy is ignored.")
			case "placement":
				c.warn(s, key, "Dock places the environment on a server itself, so deploy.placement is ignored.")
			case "update_config", "rollback_config":
				c.warn(s, key, "Dock deploys with a health check before traffic moves and rolls back with `dock rollback`, so deploy.%s is ignored.", k)
			case "labels", "endpoint_mode":
				c.warn(s, key, "deploy.%s is ignored.", k)
			default:
				if !strings.HasPrefix(k, "x-") {
					c.warn(s, key, "%s isn't a deploy key Dock knows, so it is ignored.", k)
				}
			}
		}
		if r := dep.Resources; r != nil {
			for _, k := range r.keys {
				switch k {
				case "limits":
				case "reservations":
					if r.Reservations != nil && len(r.Reservations.Devices) > 0 {
						c.warn(s, "deploy.resources.reservations.devices", "Devices (GPUs) can't be passed to a Dock service, so the service runs without them. Use an image that runs on the CPU.")
					}
					if r.Reservations == nil || slices.ContainsFunc(r.Reservations.keys, func(k string) bool { return k != "devices" }) {
						c.warn(s, "deploy.resources.reservations", "Dock sets limits, not reservations, so the reserved memory and CPUs aren't set aside. Set limits with `dock service update --memory` and `--cpus` if the service needs them.")
					}
				default:
					c.warn(s, "deploy.resources."+k, "deploy.resources.%s isn't supported by Dock, so it is not applied.", k)
				}
			}
			if l := r.Limits; l != nil {
				for _, k := range l.keys {
					if k != "memory" && k != "cpus" {
						c.warn(s, "deploy.resources.limits."+k, "deploy.resources.limits.%s isn't supported by Dock, so it is not applied.", k)
					}
				}
				if l.Memory.set {
					memory, memKey = l.Memory, "deploy.resources.limits.memory"
				}
				if l.CPUs.set {
					cpus, cpuKey = l.CPUs, "deploy.resources.limits.cpus"
				}
			}
		}
	}
	if memory.set {
		if mb, err := parseMemory(c.str(s, memKey, memory)); err != nil {
			c.warn(s, memKey, "%s, so no memory limit is set.", upperFirst(err.Error()))
		} else {
			s.out.MemoryMB = mb
		}
	}
	if cpus.set {
		text := c.str(s, cpuKey, cpus)
		if n, err := strconv.ParseFloat(text, 64); err != nil || n <= 0 {
			c.warn(s, cpuKey, "CPU limit %q isn't a number like 0.5, so no CPU limit is set.", text)
		} else {
			s.out.CPUs = n
		}
	}
}

// upperFirst capitalises a sentence's first letter.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// hostRuleRe matches a Traefik Host(...) matcher; quotedRe the hostnames
// in it.
var (
	hostRuleRe = regexp.MustCompile("Host\\(([^)]*)\\)")
	quotedRe   = regexp.MustCompile("[`\"']([^`\"']+)[`\"']")
)

// labels warns that container labels are ignored. A reverse proxy's
// labels (Traefik, Caddy) get a warning naming the hostnames they route,
// so the user can add them as domains.
func (c *converter) labels(s *svc) {
	all := labelsOf(s.def.Labels)
	var proxies, hosts []string
	for _, k := range slices.Sorted(maps.Keys(all)) {
		v, _ := interpolate(all[k], c.lookup)
		switch {
		case strings.HasPrefix(k, "traefik."):
			if !slices.Contains(proxies, "Traefik") {
				proxies = append(proxies, "Traefik")
			}
			if strings.HasSuffix(k, ".rule") {
				for _, m := range hostRuleRe.FindAllStringSubmatch(v, -1) {
					for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
						hosts = append(hosts, q[1])
					}
				}
			}
		case k == "caddy" || strings.HasPrefix(k, "caddy.") || strings.HasPrefix(k, "caddy_"):
			if !slices.Contains(proxies, "Caddy") {
				proxies = append(proxies, "Caddy")
			}
			if !strings.Contains(k, ".") && !strings.Contains(v, "{{") {
				for _, h := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
					if _, after, ok := strings.Cut(h, "://"); ok {
						h = after
					}
					hosts = append(hosts, strings.TrimSuffix(h, "/"))
				}
			}
		}
	}
	if len(proxies) == 0 {
		c.warn(s, "labels", "Container labels are ignored; Dock sets its own.")
		return
	}
	slices.Sort(hosts)
	hosts = slices.Compact(hosts)
	add := fmt.Sprintf("Add the service's hostname with `dock domain add <hostname> -s %s`.", s.name)
	switch len(hosts) {
	case 0:
	case 1:
		add = fmt.Sprintf("Add %s with `dock domain add %s -s %s`.", hosts[0], hosts[0], s.name)
	default:
		add = fmt.Sprintf("Add %s with `dock domain add <hostname> -s %s`, one at a time.", strings.Join(hosts, ", "), s.name)
	}
	c.warn(s, "labels", "%s labels are ignored: Dock routes HTTP to the service's port through its domains, without a proxy container. %s",
		strings.Join(proxies, " and "), add)
}

// joinNames lists the names a service's secrets or configs entries refer to.
func joinNames(entries []any) string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = sourceOf(e)
	}
	return strings.Join(names, ", ")
}

// isntArent agrees "isn't" with a count.
func isntArent(n int) string {
	if n == 1 {
		return "isn't"
	}
	return "aren't"
}

// firstOr is the first element of list, or fallback.
func firstOr(list []string, fallback string) string {
	if len(list) == 0 {
		return fallback
	}
	return list[0]
}

// extendsTarget says what an extends entry names: `extends: web`, or a map
// with a service and maybe a file.
func extendsTarget(v any) string {
	m, ok := v.(map[any]any)
	if !ok {
		return fmt.Sprintf("service %v", v)
	}
	if f, ok := m["file"]; ok {
		return fmt.Sprintf("service %v in %v", m["service"], f)
	}
	return fmt.Sprintf("service %v", m["service"])
}
