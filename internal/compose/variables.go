package compose

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// variable is a service variable being converted.
type variable struct {
	// value is the interpolated value; raw is the value as written.
	value, raw string
	// bare is a variable with no value: `KEY` taken from a shell that .env
	// doesn't cover.
	bare bool
	// missing are the ${VAR}s in it that .env doesn't set.
	missing []string
	// ref is a value rewritten to a pure reference: never a secret.
	ref bool
}

// unresolved reports whether the variable needs a value from the user.
func (v *variable) unresolved() bool { return !v.ref && (v.bare || len(v.missing) > 0) }

// refOnlyRe matches a value that is one Dock reference and nothing else.
var refOnlyRe = regexp.MustCompile(`^\$\{\{[a-z0-9-]+\.[A-Za-z_][A-Za-z0-9_]*\}\}$`)

// ref writes a Dock reference to a service's variable.
func ref(service, key string) string { return "${{" + service + "." + key + "}}" }

// environment collects a service's variables from env_file and
// environment (which wins, as in Compose), drops the ones Dock can't use,
// and takes a template's credentials out of its variables.
func (c *converter) environment(s *svc) {
	fromFile := map[string]bool{}
	for _, f := range s.def.EnvFile {
		p := c.str(s, "env_file", f.path)
		content, ok := c.files[path.Clean(p)]
		if !ok {
			if !slices.Contains(c.missing, path.Clean(p)) {
				c.missing = append(c.missing, path.Clean(p))
			}
			if f.optional {
				c.warn(s, "env_file", "The env file %s is optional in Compose (required: false) and wasn't sent, so its variables aren't set. If it exists, import again with the file next to the Compose file, or set them with `dock env set -s %s`.", p, s.name)
			} else {
				c.warn(s, "env_file", "The env file %s wasn't sent with the Compose file, so its variables aren't set. Import again with the file next to the Compose file, or set them with `dock env set -s %s`.", p, s.name)
			}
			continue
		}
		lines, bad := parseEnvFile(content, c.lookup)
		for _, n := range bad {
			c.warn(s, "env_file", "Line %d of %s isn't a KEY=VALUE variable, so it is skipped.", n, p)
		}
		for _, l := range lines {
			fromFile[l.key] = true
			if l.bare {
				s.vars[l.key] = c.fromShell(l.key)
			} else {
				s.vars[l.key] = &variable{value: l.value, raw: l.raw, missing: l.missing}
			}
		}
	}
	for _, e := range s.def.Environment {
		if !e.value.set {
			if !fromFile[e.key] {
				s.vars[e.key] = c.fromShell(e.key)
			}
			continue
		}
		v := &variable{raw: e.value.text}
		v.value, v.missing = interpolate(e.value.text, c.lookup)
		s.vars[e.key] = v
	}
	for _, key := range slices.Sorted(maps.Keys(s.vars)) {
		v := s.vars[key]
		if key == "PORT" {
			s.portVar = v
			delete(s.vars, key)
			continue
		}
		if msg := validKey(key); msg != "" {
			c.warn(s, key, "%s %s", key, msg)
			delete(s.vars, key)
			continue
		}
		if strings.HasPrefix(v.value, "/run/secrets/") {
			c.warn(s, key, "%s points at a Compose secret file under /run/secrets, which Dock doesn't create. If the image also reads the value itself from a variable, set that with `dock env set -s %s` instead.", key, s.name)
		}
		if strings.Contains(v.value, "${{") {
			c.warn(s, key, "%s contains ${{, which Dock reads as a reference to another service's variable (${{service.KEY}}).", key)
		}
	}
	c.templateVariables(s)
}

// requiredRe matches ${VAR:?message} and ${VAR?message}.
var requiredRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:?\?([^}]+)\}`)

// requiredNote quotes the error messages a value's ${VAR:?message} forms
// give, as a sentence with a leading space, or "".
func requiredNote(raw string) string {
	var msgs []string
	for _, m := range requiredRe.FindAllStringSubmatch(raw, -1) {
		msgs = append(msgs, strings.TrimSpace(m[1]))
	}
	if len(msgs) == 0 {
		return ""
	}
	return fmt.Sprintf(" The Compose file says: %q.", strings.Join(msgs, "; "))
}

// fromShell resolves a variable Compose takes from the shell: from .env,
// else it has no value.
func (c *converter) fromShell(key string) *variable {
	if v, ok := c.dotenv[key]; ok {
		return &variable{value: v, raw: v}
	}
	return &variable{bare: true}
}

// pgCreds are a postgres template service's credentials as the Compose
// file sets them (with the postgres image's defaults), to recognise them
// in other services.
type pgCreds struct {
	user, password, db string
	// rawPassword is POSTGRES_PASSWORD as written, so `${DB_PASSWORD}`
	// matches even when .env doesn't set it.
	rawPassword string
}

// templateVariables drops the variables a template sets itself: its
// generated password, PGDATA, and credentials equal to its defaults.
func (c *converter) templateVariables(s *svc) {
	get := func(key string) (string, bool) {
		if v, ok := s.vars[key]; ok && !v.unresolved() {
			return v.value, true
		}
		return "", false
	}
	switch s.out.Template {
	case apitypes.TemplatePostgres:
		creds := &pgCreds{user: "postgres"}
		if v, ok := get("POSTGRES_USER"); ok && v != "" {
			creds.user = v
		}
		creds.db = creds.user
		if v, ok := get("POSTGRES_DB"); ok && v != "" {
			creds.db = v
		}
		if v, ok := s.vars["POSTGRES_PASSWORD"]; ok {
			creds.password, _ = get("POSTGRES_PASSWORD")
			if strings.Contains(v.raw, "$") {
				creds.rawPassword = v.raw
			}
			delete(s.vars, "POSTGRES_PASSWORD")
			c.warn(s, "POSTGRES_PASSWORD", "The postgres template generates its own password, so this value isn't used. Other services' variables that held it now reference %s.", ref(s.name, "POSTGRES_PASSWORD"))
		}
		if _, ok := s.vars["PGDATA"]; ok {
			delete(s.vars, "PGDATA")
			c.warn(s, "PGDATA", "The postgres template keeps its data in its own volume, so PGDATA isn't used.")
		}
		if v, ok := get("POSTGRES_USER"); ok && v == "postgres" {
			delete(s.vars, "POSTGRES_USER")
		}
		if v, ok := get("POSTGRES_DB"); ok && v == "app" {
			delete(s.vars, "POSTGRES_DB")
		}
		s.pg = creds
	case apitypes.TemplateRedis:
		if v, ok := s.vars["REDIS_PASSWORD"]; ok {
			s.password = v.value
			delete(s.vars, "REDIS_PASSWORD")
			c.warn(s, "REDIS_PASSWORD", "The redis template generates its own password, so this value isn't used. Other services' variables that held it now reference %s, unless another service had the same one.", ref(s.name, "REDIS_PASSWORD"))
		}
	case apitypes.TemplateMongoDB:
		if _, ok := s.vars["MONGO_INITDB_ROOT_PASSWORD"]; ok {
			s.password, _ = get("MONGO_INITDB_ROOT_PASSWORD")
			delete(s.vars, "MONGO_INITDB_ROOT_PASSWORD")
			c.warn(s, "MONGO_INITDB_ROOT_PASSWORD", "The mongodb template generates its own password, so this value isn't used. Other services' variables that held it now reference %s, unless another service had the same one.", ref(s.name, "MONGO_INITDB_ROOT_PASSWORD"))
		}
		// MONGODB_URI holds the user as it is, unescaped: keep one only
		// when it is known now and safe in a URL as written (MongoDB wants
		// $ : / ? # [ ] @ and % escaped).
		if _, set := s.vars["MONGO_INITDB_ROOT_USERNAME"]; set {
			switch v, ok := get("MONGO_INITDB_ROOT_USERNAME"); {
			case ok && v == "root":
				delete(s.vars, "MONGO_INITDB_ROOT_USERNAME")
			case !ok:
				delete(s.vars, "MONGO_INITDB_ROOT_USERNAME")
				c.warn(s, "MONGO_INITDB_ROOT_USERNAME", "The user has no value in the Compose file or .env, and MONGODB_URI holds it as it is, so the template's root user is used instead.")
			case !mongoUserRe.MatchString(v):
				delete(s.vars, "MONGO_INITDB_ROOT_USERNAME")
				c.warn(s, "MONGO_INITDB_ROOT_USERNAME", "The user %q has characters a mongodb:// URL can't hold as they are, so the template's root user is used instead.", v)
			}
		}
	}
}

// mongoUserRe is a MongoDB user that a connection string holds as it is.
var mongoUserRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// portVariable uses a PORT variable as the service's port when nothing
// else set one: Dock sets PORT from the port setting.
func (c *converter) portVariable(s *svc) {
	if s.portVar == nil {
		return
	}
	if n, err := parsePortNumber(s.portVar.value); err == nil && s.out.Port == 0 && s.out.Template == apitypes.TemplateNone {
		s.out.Port = n
		c.warn(s, "PORT", "Dock sets PORT from the service's port setting, so PORT=%d becomes the service's port.", n)
		return
	}
	c.warn(s, "PORT", "PORT %s", validKey("PORT"))
}

// references rewrites values that point at other services into Dock
// references, then warns about the variables that still need a value.
func (c *converter) references() {
	for _, s := range c.services {
		if s.out.Template != apitypes.TemplateNone {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(s.vars)) {
			v := s.vars[key]
			out, note := c.rewrite(key, v)
			if note == "" {
				continue
			}
			v.value, v.missing, v.bare = out, nil, false
			v.ref = refOnlyRe.MatchString(out)
			c.warn(s, key, "%s %s", key, note)
		}
	}
	for _, s := range c.services {
		for _, key := range slices.Sorted(maps.Keys(s.vars)) {
			v := s.vars[key]
			set := fmt.Sprintf("Set it before the first deploy with `dock env set -s %s %s=...`.", s.name, key)
			switch {
			case v.ref:
			case v.bare:
				c.warn(s, key, "%s has no value in the Compose file or .env, so it is a secret without a value. %s", key, set)
			case len(v.missing) > 0:
				names := make([]string, len(v.missing))
				for i, n := range v.missing {
					names[i] = "${" + n + "}"
				}
				verb := "isn't"
				if len(names) > 1 {
					verb = "aren't"
				}
				c.warn(s, key, "%s uses %s, which %s set in .env, so it is a secret without a value.%s %s", key, strings.Join(names, ", "), verb, requiredNote(v.raw), set)
			}
		}
	}
}

// rewrite turns a value that points at another service into a reference.
// note says what changed ("" when nothing did); it follows the key in a
// warning.
func (c *converter) rewrite(key string, v *variable) (string, string) {
	upper := strings.ToUpper(key)
	if v.value == "" && v.raw == "" {
		return "", ""
	}
	if u, ok := splitURL(v.value); ok {
		return c.rewriteURL(v.value, u)
	}
	mentions := func(words ...string) bool {
		return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(upper, w) })
	}
	// MONGODB_PASSWORD says DB too: Mongo and Redis keys are theirs.
	if mentions("DB", "PG", "POSTGRES", "DATABASE") && !mentions("MONGO", "REDIS") {
		field := ""
		switch {
		case mentions("PASS"):
			field = "POSTGRES_PASSWORD"
		case mentions("USER"):
			field = "POSTGRES_USER"
		case mentions("NAME") || strings.HasSuffix(upper, "DB") || strings.HasSuffix(upper, "DATABASE"):
			field = "POSTGRES_DB"
		}
		var match []*svc
		for _, t := range c.services {
			if t.pg == nil {
				continue
			}
			ok := false
			switch field {
			case "POSTGRES_PASSWORD":
				ok = t.pg.password != "" && v.value == t.pg.password ||
					t.pg.rawPassword != "" && v.raw == t.pg.rawPassword
			case "POSTGRES_USER":
				ok = v.value == t.pg.user
			case "POSTGRES_DB":
				ok = v.value == t.pg.db
			}
			if ok {
				match = append(match, t)
			}
		}
		if len(match) == 1 {
			r := ref(match[0].name, field)
			return r, fmt.Sprintf("matches %s's %s in the Compose file, so it now references %s.", match[0].compose, field, r)
		}
	}
	if mentions("REDIS", "MONGO") && mentions("PASS") {
		// A REDIS_ key matches only a Redis template, a MONGO_ key only a
		// MongoDB one; as for Postgres, only one service may match, or
		// the value stays as it is.
		var match []*svc
		for _, t := range c.services {
			pw, ok := templatePasswords[t.out.Template]
			if ok && mentions(pw.engine) && t.password != "" && v.value == t.password {
				match = append(match, t)
			}
		}
		if len(match) == 1 {
			r := ref(match[0].name, templatePasswords[match[0].out.Template].field)
			return r, fmt.Sprintf("matches %s's password in the Compose file, so it now references %s.", match[0].compose, r)
		}
		if len(match) > 1 {
			// Kept as written, which no generated password matches: say
			// which references to choose from.
			refs := make([]string, len(match))
			for i, t := range match {
				refs[i] = ref(t.name, templatePasswords[t.out.Template].field)
			}
			return v.value, fmt.Sprintf("matches the password of %d services in the Compose file, which each get their own, so it keeps the old value and won't log in; set it to the one it uses: %s.", len(match), strings.Join(refs, " or "))
		}
	}
	if strings.HasSuffix(upper, "HOST") {
		if t := c.renamed(v.value); t != nil {
			return t.name, fmt.Sprintf("names %s, which is named %s in Dock, so it now says %s.", t.compose, t.name, t.name)
		}
	}
	return "", ""
}

// templatePasswords are the templates whose password other services'
// variables may hold: the variable it is, and the word a key names it by.
var templatePasswords = map[apitypes.Template]struct{ field, engine string }{
	apitypes.TemplateRedis:   {"REDIS_PASSWORD", "REDIS"},
	apitypes.TemplateMongoDB: {"MONGO_INITDB_ROOT_PASSWORD", "MONGO"},
}

// rewriteURL rewrites a URL whose host is a template service into its
// references, and the host of a renamed service into its Dock name.
func (c *converter) rewriteURL(value string, u urlParts) (string, string) {
	t := c.byHost(u.host)
	if t == nil {
		return "", ""
	}
	query := ""
	if i := strings.IndexAny(u.rest, "?#"); i >= 0 {
		query = u.rest[i:]
	}
	switch {
	case t.out.Template == apitypes.TemplatePostgres && (u.scheme == "postgres" || u.scheme == "postgresql"):
		r := ref(t.name, "DATABASE_URL") + query
		return r, fmt.Sprintf("points at %s, so it now uses the postgres template's DATABASE_URL: %s.", t.compose, r)
	case t.out.Template == apitypes.TemplatePostgres && strings.Contains(u.scheme, "postgres"):
		r := fmt.Sprintf("%s://%s:%s@%s:5432/%s%s", u.scheme, ref(t.name, "POSTGRES_USER"), ref(t.name, "POSTGRES_PASSWORD"),
			t.name, ref(t.name, "POSTGRES_DB"), query)
		return r, fmt.Sprintf("points at %s, so it now uses the postgres template's credentials: %s.", t.compose, r)
	case t.out.Template == apitypes.TemplateRedis && (u.scheme == "redis" || u.scheme == "rediss"):
		r := ref(t.name, "REDIS_URL") + u.rest
		return r, fmt.Sprintf("points at %s, so it now uses the redis template's REDIS_URL: %s.", t.compose, r)
	case t.out.Template == apitypes.TemplateMongoDB && u.scheme == "mongodb":
		// The app's database and other options stay; the root user is
		// in admin, whatever authSource the old user had.
		path, query, _ := strings.Cut(u.rest, "?")
		opts := []string{}
		for o := range strings.SplitSeq(query, "&") {
			if o != "" && !strings.HasPrefix(strings.ToLower(o), "authsource=") {
				opts = append(opts, o)
			}
		}
		query = strings.Join(append(opts, "authSource=admin"), "&")
		if path == "" {
			path = "/"
		}
		r := fmt.Sprintf("mongodb://%s:%s@%s:27017%s?%s", ref(t.name, "MONGO_INITDB_ROOT_USERNAME"), ref(t.name, "MONGO_INITDB_ROOT_PASSWORD"), t.name, path, query)
		return r, fmt.Sprintf("points at %s, so it now uses the mongodb template's credentials: %s.", t.compose, r)
	case t.name != t.compose && strings.EqualFold(u.host, t.compose):
		return replaceHost(value, t.name), fmt.Sprintf("points at %s, which is named %s in Dock, so the URL now uses that name.", t.compose, t.name)
	}
	return "", ""
}

// byHost finds the service a hostname names: its Compose name, else its
// Dock name (hostnames are case-insensitive).
func (c *converter) byHost(host string) *svc {
	for _, s := range c.services {
		if strings.EqualFold(host, s.compose) {
			return s
		}
	}
	for _, s := range c.services {
		if strings.EqualFold(host, s.name) {
			return s
		}
	}
	return nil
}

// renamed finds a service whose Dock name differs from its Compose name
// by its Compose name.
func (c *converter) renamed(name string) *svc {
	for _, s := range c.services {
		if s.name != s.compose && strings.EqualFold(name, s.compose) {
			return s
		}
	}
	return nil
}

// replaceHost replaces the host of a URL that splitURL accepted, leaving
// the rest as written.
func replaceHost(s, host string) string {
	start := strings.Index(s, "://") + 3
	end := len(s)
	if i := strings.IndexAny(s[start:], "/?#"); i >= 0 {
		end = start + i
	}
	authority := s[start:end]
	at := strings.LastIndex(authority, "@") + 1
	hostPort := authority[at:]
	port := ""
	if i := strings.LastIndex(hostPort, ":"); i >= 0 && !strings.HasSuffix(hostPort, "]") {
		if _, err := strconv.Atoi(hostPort[i+1:]); err == nil || hostPort[i+1:] == "" {
			port = hostPort[i:]
		}
	}
	return s[:start] + authority[:at] + host + port + s[end:]
}
