import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useIsAdmin } from "../../data/queries";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { Icon } from "../../ui/icons";
import {
  Act,
  Button,
  ErrorText,
  Page,
  PageBar,
  Panel,
  StateText,
} from "../../ui/kit";
import { publishState, statusQueries, useCreateStatusPage } from "./data";

/**
 * The organization's public status pages (ADR 0073): each lists
 * components built from services and uptime checks, served at its address
 * once published. Every member sees them; admins make and change them.
 */
export function StatusPagesPage() {
  const pages = useSuspenseQuery(statusQueries.list()).data;
  const admin = useIsAdmin();
  const org = useOrg();
  const [adding, setAdding] = useState(false);
  useShortcuts(
    admin
      ? [{ key: "n", label: "new status page", run: () => setAdding(true) }]
      : [],
  );
  return (
    <>
      <PageBar
        title="Status pages"
        actions={
          admin &&
          !adding && (
            <Button primary onClick={() => setAdding(true)}>
              New status page <kbd>N</kbd>
            </Button>
          )
        }
      />
      <Page>
        {adding && <NewPage onClose={() => setAdding(false)} />}
        {pages.length === 0 ? (
          !adding && (
            <p className="text-mut">
              No status pages. A status page tells the public how your services
              are doing, from their uptime checks.
            </p>
          )
        ) : (
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th className="pr-7">Title</th>
                  <th>State</th>
                  <th>Components</th>
                  <th>Address</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {pages.map((p) => (
                  <tr key={p.slug} className="row-link">
                    <td className="py-2 font-mono">
                      <Link
                        to="/$org/status-pages/$page"
                        params={{ org, page: p.slug }}
                        className="row-main font-medium hover:underline"
                      >
                        {p.slug}
                      </Link>
                    </td>
                    <td className="pr-7">{p.title}</td>
                    <td>
                      <StateText state={publishState(p.published)} />
                    </td>
                    <td className="tabular-nums">{p.components}</td>
                    <td className="w-full font-mono text-sec">
                      {p.published && p.url ? (
                        <a href={p.url} target="_blank" rel="noreferrer">
                          {p.url.replace(/^https?:\/\//, "")}
                        </a>
                      ) : (
                        <span className="text-faint">
                          {p.url.replace(/^https?:\/\//, "") || "none"}
                        </span>
                      )}
                    </td>
                    <td className="row-go" aria-hidden>
                      <Icon name="chevronRight" size={14} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>
        )}
      </Page>
    </>
  );
}

/** Makes a page: its name (its address) and title. */
function NewPage({ onClose }: { onClose: () => void }) {
  const create = useCreateStatusPage();
  const navigate = useNavigate();
  const org = useOrg();
  const [slug, setSlug] = useState("");
  const [title, setTitle] = useState("");
  return (
    <form
      aria-label="New status page"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate(
          { slug: slug.trim(), title: title.trim() || undefined },
          {
            onSuccess: (p) =>
              navigate({
                to: "/$org/status-pages/$page",
                params: { org, page: p.slug },
              }),
          },
        );
      }}
    >
      <input
        className="field w-48 font-mono"
        placeholder="name, e.g. acme"
        aria-label="Name"
        value={slug}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) => setSlug(e.target.value.toLowerCase())}
      />
      <input
        className="field w-64"
        placeholder="Title (default: the name)"
        aria-label="Title"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
      />
      <Button onClick={onClose}>Cancel</Button>
      <Act
        primary
        type="submit"
        cli={`dock status-page create ${slug || "<name>"}${title ? ` --title "${title}"` : ""}`}
        disabled={!slug.trim() || create.isPending}
      >
        Create
      </Act>
      <span className="basis-full text-xs text-mut">
        The name is the page's address: status-{slug || "<name>"}.&lt;base
        domain&gt;. It starts unpublished.
      </span>
      <ErrorText error={create.error} />
    </form>
  );
}
