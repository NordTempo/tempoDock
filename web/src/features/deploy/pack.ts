// The source archive `dock deploy` uploads, packed in the browser: a gzip
// tar of a directory with exactly internal/pack's rules.
//
//   - A root .dockerignore decides, with Docker's matcher
//     (moby/patternmatcher, ported below); .gitignore files are then not
//     read. Otherwise .gitignore files at the root and in subdirectories
//     apply, with internal/pack's git rules.
//   - .git (at any depth, a directory or a submodule's file) is left out.
//     The Dockerfile (dock.toml's [build] dockerfile, else the service's),
//     .dockerignore and dock.toml are always kept.
//   - Entries go in lexical order, a directory before its contents.
//
// What a browser can't match: it hands over files and directories only,
// so symlinks arrive as the files they point to, and it hides permission
// bits, so files are 0644 except ones starting with "#!" (scripts), which
// are 0755 to stay executable. A picked folder (<input webkitdirectory>)
// lists no empty directories.
//
// The module has no imports, so internal/pack's tests can run it in Node
// and compare its archives with pack.Write's.

/**
 * One entry of a directory being packed. List reads a directory's
 * entries when the walk gets there, so ignored directories are never read.
 */
export type Entry =
  | { kind: "file"; name: string; file: () => Promise<File> }
  | { kind: "dir"; name: string; list: List };
export type List = () => Promise<Entry[]>;

/** Archive bytes; ArrayBuffer-backed, as CompressionStream takes them. */
type Bytes = Uint8Array<ArrayBuffer>;

export type PackStats = {
  /** Regular files packed. */
  files: number;
  /** Their uncompressed size. */
  bytes: number;
  /** What shaped the archive: "dock.toml", ".dockerignore". */
  notes: string[];
};

export type PackOptions = {
  /** The service's Dockerfile path; dock.toml's [build] dockerfile wins. */
  dockerfile?: string;
  /** Called after each file with the running totals. */
  onProgress?: (stats: PackStats) => void;
};

/**
 * Packs the directory that `root` lists into a gzip tar stream. Nothing is
 * read before the stream is pulled, and files are read one chunk at a time.
 * `stats` fills in as it goes.
 */
export function pack(
  root: List,
  opts: PackOptions = {},
): { stream: ReadableStream<Bytes>; stats: PackStats } {
  const stats: PackStats = { files: 0, bytes: 0, notes: [] };
  const chunks = tarChunks(root, opts, stats);
  const tar = new ReadableStream<Bytes>({
    async pull(controller) {
      const next = await chunks.next();
      if (next.done) controller.close();
      else controller.enqueue(next.value);
    },
    async cancel() {
      await chunks.return(undefined);
    },
  });
  return { stream: tar.pipeThrough(new CompressionStream("gzip")), stats };
}

// ---- The walk (internal/pack.Write) ----

type Decision = "include" | "skip" | "descend";

type Filter = {
  decide(rel: string, isDir: boolean): Decision;
  /** An included directory's entries, before they are visited. */
  enter(rel: string, entries: Entry[]): Promise<void>;
};

async function* tarChunks(
  root: List,
  opts: PackOptions,
  stats: PackStats,
): AsyncGenerator<Bytes> {
  const top = sorted(await root());
  const text = async (name: string) => {
    const e = top.find((x) => x.name === name);
    return e?.kind === "file" ? (await e.file()).text() : null;
  };
  const toml = await text("dock.toml");
  if (toml !== null) stats.notes.push("dock.toml");
  const dockerfile =
    (toml !== null && tomlDockerfile(toml)) || opts.dockerfile || "Dockerfile";
  const keep = [clean(dockerfile), ".dockerignore", "dock.toml"];

  const ignore = await text(".dockerignore");
  let filter: Filter;
  if (ignore !== null) {
    stats.notes.push(".dockerignore");
    filter = dockerignore(readIgnoreFile(ignore), keep);
  } else {
    filter = gitignore(keep);
  }

  const out = new Batch();
  // A directory's modification time isn't exposed; use the packing time.
  const now = Math.floor(Date.now() / 1000);

  async function* walk(entries: Entry[], base: string): AsyncGenerator<Bytes> {
    for (const e of entries) {
      if (e.name === ".git") continue; // the repo, or a submodule's .git file
      const rel = base ? `${base}/${e.name}` : e.name;
      const dec = filter.decide(rel, e.kind === "dir");
      if (dec === "skip") continue;
      if (e.kind === "file") {
        yield* addFile(out, rel, await e.file(), stats);
        opts.onProgress?.(stats);
        continue;
      }
      const children = sorted(await e.list());
      if (dec === "include") {
        await filter.enter(rel, children);
        yield* out.add(header(`${rel}/`, 0o755, now, "5", 0));
      }
      yield* walk(children, rel);
    }
  }

  await filter.enter("", top);
  yield* walk(top, "");
  yield* out.add(new Uint8Array(1024)); // two zero blocks end the archive
  if (out.size) yield out.take();
}

/** Byte order for ASCII names, like os.ReadDir's sort. */
const sorted = (entries: Entry[]) =>
  entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));

/** Reads one file into the archive, checking it didn't change meanwhile. */
async function* addFile(
  out: Batch,
  rel: string,
  file: File,
  stats: PackStats,
): AsyncGenerator<Bytes> {
  const size = file.size;
  const mode = (await file.slice(0, 2).text()) === "#!" ? 0o755 : 0o644;
  yield* out.add(
    header(rel, mode, Math.floor(file.lastModified / 1000), "0", size),
  );
  let n = 0;
  try {
    const reader = file.stream().getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      n += value.length;
      if (n > size) break;
      yield* out.add(value);
    }
  } catch {
    throw new Error(`${rel}: could not read it (did it change while packing?)`);
  }
  if (n !== size) {
    throw new Error(`${rel}: its size changed while packing; try again`);
  }
  const pad = (512 - (size % 512)) % 512;
  if (pad) yield* out.add(new Uint8Array(pad));
  stats.files++;
  stats.bytes += size;
}

/** Collects small writes into 64 KiB chunks, as pack.Write's bufio does. */
class Batch {
  private buf = new Uint8Array(1 << 16);
  size = 0;

  *add(b: Bytes): Generator<Bytes> {
    if (this.size + b.length > this.buf.length && this.size) yield this.take();
    if (b.length >= this.buf.length) {
      yield b;
      return;
    }
    this.buf.set(b, this.size);
    this.size += b.length;
  }

  take() {
    const full = this.buf.subarray(0, this.size);
    this.buf = new Uint8Array(1 << 16);
    this.size = 0;
    return full;
  }
}

/** dock.toml's [build] dockerfile, if set. Enough TOML for that one key. */
export function tomlDockerfile(toml: string): string | undefined {
  let table = "";
  for (const raw of toml.split("\n")) {
    const line = raw.trim();
    const t = /^\[\s*([\w.-]+)\s*\](\s*#.*)?$/.exec(line);
    if (t) {
      table = t[1] ?? "";
      continue;
    }
    const kv = /^([\w.-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')\s*(#.*)?$/.exec(line);
    if (!kv) continue;
    const key = table ? `${table}.${kv[1]}` : kv[1];
    if (key === "build.dockerfile") return kv[2] ?? kv[3];
  }
  return undefined;
}

// ---- Paths ----

/** Go's path.Clean. */
export function clean(p: string): string {
  if (p === "") return ".";
  const rooted = p.startsWith("/");
  const out: string[] = [];
  for (const seg of p.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") {
      if (out.length && out[out.length - 1] !== "..") out.pop();
      else if (!rooted) out.push("..");
      continue;
    }
    out.push(seg);
  }
  const s = out.join("/");
  return rooted ? `/${s}` : s || ".";
}

/** Go's path.Dir for a clean relative path. */
const dirname = (p: string) => {
  const i = p.lastIndexOf("/");
  return i < 0 ? "." : p.slice(0, i);
};

// ---- .dockerignore (moby/patternmatcher and its ignorefile) ----

/** ignorefile.ReadAll: one cleaned pattern per line, "!" kept. */
export function readIgnoreFile(text: string): string[] {
  const out: string[] = [];
  text.split("\n").forEach((raw, i) => {
    let line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
    if (i === 0) line = line.replace(/^\ufeff/, "");
    if (line.startsWith("#")) return;
    line = line.trim();
    if (!line) return;
    const invert = line[0] === "!";
    if (invert) line = line.slice(1).trim();
    if (line) {
      line = clean(line);
      if (line.length > 1 && line[0] === "/") line = line.slice(1);
    }
    out.push(invert ? `!${line}` : line);
  });
  return out;
}

type Pattern = {
  text: string;
  exclusion: boolean;
  match: (path: string) => boolean;
};

/**
 * The root .dockerignore with Docker's matcher, so the build sees what
 * `docker build` would. Files in keep are sent even if ignored.
 */
function dockerignore(lines: string[], keep: string[]): Filter {
  const patterns: Pattern[] = [];
  for (let p of [...lines, ...keep.map((k) => `!${k}`)]) {
    p = p.trim();
    if (!p) continue;
    p = clean(p);
    let exclusion = false;
    if (p[0] === "!") {
      if (p.length === 1)
        throw new Error('.dockerignore: illegal exclusion pattern: "!"');
      exclusion = true;
      p = p.slice(1);
    }
    if (!validGlob(p))
      throw new Error(`.dockerignore: syntax error in pattern ${p}`);
    patterns.push({ text: p, exclusion, match: compilePattern(p) });
  }

  // PatternMatcher.MatchesOrParentMatches.
  const matches = (file: string) => {
    let matched = false;
    const parent = dirname(file);
    const parentDirs = parent.split("/");
    for (const p of patterns) {
      if (p.exclusion !== matched) continue;
      let m = p.match(file);
      if (!m && parent !== ".") {
        for (let i = 0; i < parentDirs.length && !m; i++) {
          m = p.match(parentDirs.slice(0, i + 1).join("/"));
        }
      }
      if (m) matched = !p.exclusion;
    }
    return matched;
  };

  return {
    decide(rel, isDir) {
      if (!matches(rel)) return "include";
      if (!isDir) return "skip";
      // Walk into an ignored directory only if an exclusion pattern could
      // re-include something inside it (the Docker CLI's shortcut).
      const within = patterns.some(
        (p) => p.exclusion && `${p.text}/`.startsWith(`${rel}/`),
      );
      return within ? "descend" : "skip";
    },
    enter: async () => {},
  };
}

/**
 * Pattern.compile and Pattern.match: exact, prefix ("dir/**"), suffix
 * ("**\/name") or a regular expression.
 */
function compilePattern(pattern: string): (path: string) => boolean {
  let re = "^";
  let type: "exact" | "prefix" | "suffix" | "regexp" = "exact";
  const chars = [...pattern];
  for (let i = 0, iter = 0; i < chars.length; iter++) {
    const ch = chars[i++] ?? "";
    if (ch === "*") {
      if (chars[i] === "*") {
        i++;
        if (chars[i] === "/") i++;
        if (i >= chars.length) {
          if (type === "exact") type = "prefix";
          else {
            re += ".*";
            type = "regexp";
          }
        } else {
          re += "(.*/)?";
          type = "regexp";
        }
        if (iter === 0) type = "suffix";
      } else {
        re += "[^/]*";
        type = "regexp";
      }
    } else if (ch === "?") {
      re += "[^/]";
      type = "regexp";
    } else if (".+()|{}$".includes(ch)) {
      re += `\\${ch}`;
    } else if (ch === "\\") {
      if (i < chars.length) {
        re += `\\${chars[i++]}`;
        type = "regexp";
      } else re += "\\";
    } else if (ch === "[" || ch === "]") {
      re += ch;
      type = "regexp";
    } else {
      re += ch;
    }
  }
  switch (type) {
    case "exact":
      return (path) => path === pattern;
    case "prefix": {
      const prefix = pattern.slice(0, -2);
      return (path) => path.startsWith(prefix);
    }
    case "suffix": {
      const suffix = pattern.slice(2);
      return (path) =>
        path.endsWith(suffix) ||
        (suffix[0] === "/" && path === suffix.slice(1));
    }
    case "regexp": {
      let rx: RegExp;
      try {
        rx = new RegExp(`${re}$`);
      } catch {
        throw new Error(`.dockerignore: syntax error in pattern ${pattern}`);
      }
      return (path) => rx.test(path);
    }
  }
}

/** Whether Go's path.Match accepts the pattern: well-formed [classes] and escapes. */
function validGlob(p: string): boolean {
  return globRegExp(p, false) !== null;
}

// ---- .gitignore (internal/pack's git rules) ----

type Rule = {
  base: string;
  /** Pattern segments; "**" matches any number of directories. */
  segs: (RegExp | "**")[];
  anchored: boolean;
  dirOnly: boolean;
  negate: boolean;
};

/**
 * .gitignore files at the root and in every directory: deeper files take
 * precedence, the last matching pattern wins, and nothing inside an ignored
 * directory can be re-included (it is never walked).
 */
function gitignore(keep: string[]): Filter {
  const rules: Rule[] = [];
  return {
    async enter(rel, entries) {
      const e = entries.find((x) => x.name === ".gitignore");
      if (e?.kind !== "file") return;
      const text = await (await e.file()).text();
      for (const line of text.split("\n")) {
        const r = parseRule(rel, line.replace(/[\r\n]+$/, ""));
        if (r) rules.push(r);
      }
    },
    decide(rel, isDir) {
      if (keep.includes(rel)) return "include";
      let ignored = false;
      for (const r of rules) {
        if (matchRule(r, rel, isDir)) ignored = !r.negate;
      }
      return ignored ? "skip" : "include";
    },
  };
}

/** One .gitignore line found in directory base, or null for none. */
export function parseRule(base: string, line: string): Rule | null {
  line = line.replace(/^\ufeff/, "");
  // Trailing spaces are ignored unless escaped with a backslash.
  while (line.endsWith(" ") && !line.endsWith("\\ ")) line = line.slice(0, -1);
  if (line === "" || line[0] === "#") return null;
  const r: Rule = {
    base,
    segs: [],
    anchored: false,
    dirOnly: false,
    negate: false,
  };
  if (line[0] === "!") {
    r.negate = true;
    line = line.slice(1);
  }
  if (line.endsWith("/")) {
    r.dirOnly = true;
    line = line.replace(/\/+$/, "");
  }
  if (line.includes("/")) {
    r.anchored = true;
    if (line.startsWith("/")) line = line.slice(1);
  }
  if (line === "") return null;
  for (const s of line.split("/")) {
    if (s === "**") {
      r.segs.push("**");
      continue;
    }
    // git negates a bracket expression with "!", path.Match with "^".
    const rx = globRegExp(s.replaceAll("[!", "[^"), true);
    if (!rx) return null; // malformed pattern; git ignores these too
    r.segs.push(rx);
  }
  return r;
}

function matchRule(r: Rule, rel: string, isDir: boolean) {
  if (r.dirOnly && !isDir) return false;
  if (r.base) {
    if (!rel.startsWith(`${r.base}/`)) return false;
    rel = rel.slice(r.base.length + 1);
  }
  // A pattern without a slash matches the name at any depth.
  if (!r.anchored) {
    return matchSegs(r.segs, [rel.slice(rel.lastIndexOf("/") + 1)]);
  }
  return matchSegs(r.segs, rel.split("/"));
}

/** Path segments against pattern segments; "**" matches any number of directories. */
function matchSegs(pat: (RegExp | "**")[], name: string[]): boolean {
  for (let p = 0, n = 0; ; p++, n++) {
    const seg = pat[p];
    if (seg === undefined) return n === name.length;
    if (seg === "**") {
      if (p === pat.length - 1) return name.length - n > 0; // "dir/**": everything inside
      for (let i = n; i <= name.length; i++) {
        if (matchSegs(pat.slice(p + 1), name.slice(i))) return true;
      }
      return false;
    }
    const part = name[n];
    if (part === undefined || !seg.test(part)) return false;
  }
}

/**
 * A Go path.Match pattern as a regular expression: * and ? stop at "/",
 * [classes] with ^ and ranges, \ escapes. Null when Go would report
 * ErrBadPattern. Anchored when `whole`; only the syntax is checked otherwise.
 */
function globRegExp(p: string, whole: boolean): RegExp | null {
  const lit = (c: string) => c.replace(/[\\^$.*+?()[\]{}|/-]/g, "\\$&");
  const chars = [...p];
  let re = "";
  for (let i = 0; i < chars.length; ) {
    const c = chars[i++] ?? "";
    if (c === "*") re += "[^/]*";
    else if (c === "?") re += "[^/]";
    else if (c === "\\") {
      const next = chars[i++];
      if (next === undefined) return null;
      re += lit(next);
    } else if (c === "[") {
      let cls = "";
      if (chars[i] === "^") {
        cls = "^";
        i++;
      }
      let ranges = 0;
      for (;;) {
        if (chars[i] === "]" && ranges > 0) {
          i++;
          break;
        }
        const lo = classChar(chars, i);
        if (!lo) return null;
        i = lo.next;
        let hi = lo;
        if (chars[i] === "-") {
          const h = classChar(chars, i + 1);
          if (!h) return null;
          hi = h;
          i = h.next;
        }
        ranges++;
        // Go matches nothing for an inverted range; a RegExp throws.
        if (lo.c <= hi.c)
          cls += lo.c === hi.c ? lit(lo.c) : `${lit(lo.c)}-${lit(hi.c)}`;
      }
      re += cls === "^" ? "[^]" : cls === "" ? "[]" : `[${cls}]`;
    } else re += lit(c);
  }
  return whole ? new RegExp(`^${re}$`) : /(?:)/;
}

/** One character of a [class], maybe escaped; null at the end or a bare "]" or "-". */
function classChar(
  chars: string[],
  i: number,
): { c: string; next: number } | null {
  let c = chars[i];
  if (c === undefined || c === "-" || c === "]") return null;
  if (c === "\\") {
    c = chars[i + 1];
    if (c === undefined) return null;
    return { c, next: i + 2 };
  }
  return { c, next: i + 1 };
}

// ---- tar (USTAR, with PAX records for long or non-ASCII names) ----

const enc = new TextEncoder();

/** A file ("0") or directory ("5") header, preceded by a PAX header if needed. */
function header(
  name: string,
  mode: number,
  mtime: number,
  type: "0" | "5",
  size: number,
): Bytes {
  const ascii = enc.encode(name).length === name.length;
  const split = ascii ? ustarSplit(name) : null;
  if (split) return block(split[1], split[0], mode, mtime, type, size);
  // PAX: the real path in a record; the headers carry stand-ins.
  const record = paxRecord("path", name);
  const safe = name.replace(/[^\x20-\x7e]/g, "_");
  const pad = (512 - (record.length % 512)) % 512;
  const out = new Uint8Array(512 + record.length + pad + 512);
  out.set(
    block(
      `PaxHeaders/${safe.slice(-80)}`,
      "",
      0o644,
      mtime,
      "x",
      record.length,
    ),
  );
  out.set(record, 512);
  out.set(
    block(safe.slice(0, 100), "", mode, mtime, type, size),
    512 + record.length + pad,
  );
  return out;
}

/** [prefix, name] for USTAR's 155 + 100 byte fields, or null. */
function ustarSplit(name: string): [string, string] | null {
  if (name.length <= 100) return ["", name];
  const max = Math.min(name.length - 1, 155);
  for (let i = max; i > 0; i--) {
    if (
      name[i] === "/" &&
      name.length - i - 1 <= 100 &&
      name.length - i - 1 > 0
    ) {
      return [name.slice(0, i), name.slice(i + 1)];
    }
  }
  return null;
}

/** "<len> key=value\n", where len counts the whole record. */
function paxRecord(key: string, value: string): Bytes {
  const body = enc.encode(` ${key}=${value}\n`);
  let n = body.length + 1;
  while (String(n).length + body.length !== n)
    n = String(n).length + body.length;
  const out = new Uint8Array(n);
  out.set(enc.encode(String(n)));
  out.set(body, String(n).length);
  return out;
}

function block(
  name: string,
  prefix: string,
  mode: number,
  mtime: number,
  type: string,
  size: number,
): Bytes {
  if (size > 0o77777777777)
    throw new Error(`${prefix}/${name}: larger than 8 GB`);
  const b = new Uint8Array(512);
  const put = (at: number, s: string) => b.set(enc.encode(s), at);
  const octal = (at: number, width: number, n: number) =>
    put(at, n.toString(8).padStart(width - 1, "0"));
  put(0, name);
  octal(100, 8, mode);
  octal(108, 8, 0); // uid
  octal(116, 8, 0); // gid
  octal(124, 12, size);
  octal(136, 12, Math.max(0, mtime));
  put(148, "        "); // checksum, counted as spaces
  put(156, type);
  put(257, "ustar\u000000");
  put(345, prefix);
  let sum = 0;
  for (const x of b) sum += x;
  put(148, `${sum.toString(8).padStart(6, "0")}\u0000 `);
  return b;
}
