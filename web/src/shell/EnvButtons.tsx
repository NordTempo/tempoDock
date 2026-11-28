import type { ReactNode } from "react";
import type { Environment } from "../data/types";
import { Icon } from "../ui/icons";
import { Menu, menuItem } from "../ui/kit";
import type { Tone } from "../ui/status";
import { envSegment, envSwitchBox, ToneDot } from "./context";

/**
 * A project's environments as a row of buttons, the picked one marked
 * (aria-current) and a dot on one with a problem. Previews, which can be
 * many, sit in one menu at the end. The project header switches pages
 * with it (as a nav); the New service dialog picks where the service
 * goes (as a fieldset of its form). `children` ends the row, like the
 * header's "+".
 */
export function EnvButtons({
  environments,
  picked,
  onPick,
  tones,
  nav = false,
  children,
}: {
  environments: readonly Environment[];
  /** The marked environment; none when undefined. */
  picked: string | undefined;
  onPick: (name: string) => void;
  tones?: ReadonlyMap<string, Tone>;
  /** A navigation landmark rather than a group of a form. */
  nav?: boolean;
  children?: ReactNode;
}) {
  const regular = environments.filter((e) => !e.preview);
  const previews = environments.filter((e) => e.preview);
  const current = previews.find((e) => e.name === picked);
  const buttons = (
    <>
      {regular.map((e) => (
        <button
          key={e.id}
          type="button"
          aria-current={e.name === picked}
          onClick={() => onPick(e.name)}
          className={`${envSegment} not-first:border-l not-first:border-edge`}
        >
          {e.name}
          <ToneDot tone={tones?.get(e.name)} />
        </button>
      ))}
      {previews.length > 0 && (
        <Menu
          label="Previews"
          className={regular.length > 0 ? "border-l border-edge" : ""}
          button={
            <span aria-current={!!current} className={envSegment}>
              {current ? current.name : "previews"}
              <span className="text-faint">{previews.length}</span>
              <Icon name="chevronDown" size={12} />
            </span>
          }
        >
          {(close) =>
            previews.map((e) => (
              <button
                key={e.id}
                type="button"
                role="menuitem"
                className={menuItem}
                onClick={() => {
                  close();
                  onPick(e.name);
                }}
              >
                {e.name}
                <ToneDot tone={tones?.get(e.name)} />
              </button>
            ))
          }
        </Menu>
      )}
      {children}
    </>
  );
  return nav ? (
    <nav aria-label="Environment" className={envSwitchBox}>
      {buttons}
    </nav>
  ) : (
    <fieldset aria-label="Environment" className={`${envSwitchBox} min-w-0`}>
      {buttons}
    </fieldset>
  );
}
