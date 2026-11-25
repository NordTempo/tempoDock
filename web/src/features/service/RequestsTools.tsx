import { createContext, type ReactNode, useContext } from "react";
import { createPortal } from "react-dom";

/**
 * The spot at the right end of the Requests tab's view strip (Traffic,
 * Visitors, Traces). The layout route provides it; a view puts its
 * controls there with <RequestsTools>.
 */
export const RequestsToolsSlot = createContext<HTMLElement | null>(null);

/** Renders a Requests view's controls on the view strip's row. */
export function RequestsTools({ children }: { children: ReactNode }) {
  const slot = useContext(RequestsToolsSlot);
  return slot && createPortal(children, slot);
}
