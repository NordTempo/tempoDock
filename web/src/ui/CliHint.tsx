import {
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";

/** CLI hints exist only while hovered or focused, and stay inside the viewport. */
export function CliHint({
  cli,
  className,
  children,
}: {
  cli: string;
  className: string;
  children: ReactNode;
}) {
  const [anchor, setAnchor] = useState<DOMRect | null>(null);
  const hint = useRef<HTMLSpanElement>(null);

  useLayoutEffect(() => {
    if (!anchor || !hint.current) return;
    const { clientWidth, clientHeight } = document.documentElement;
    // clientWidth excludes classic scrollbars, unlike CSS viewport units.
    hint.current.style.maxWidth = `${Math.max(0, Math.min(600, clientWidth - 16))}px`;
    const { width, height } = hint.current.getBoundingClientRect();
    hint.current.style.left = `${Math.max(8, Math.min(anchor.left, clientWidth - width - 8))}px`;
    hint.current.style.top = `${
      anchor.bottom + height + 12 <= clientHeight
        ? anchor.bottom + 4
        : Math.max(8, anchor.top - height - 4)
    }px`;
  }, [anchor, cli]);

  // Only an open hint listens; scrolling or resizing invalidates its anchor.
  useEffect(() => {
    if (!anchor) return;
    const close = () => setAnchor(null);
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") close();
    };
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
      window.removeEventListener("keydown", onKey);
    };
  }, [anchor]);

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: observes the child button, not a separate control
    <span
      className={className}
      onMouseEnter={(event) =>
        setAnchor(event.currentTarget.getBoundingClientRect())
      }
      onMouseLeave={(event) => {
        if (!event.currentTarget.contains(document.activeElement))
          setAnchor(null);
      }}
      onFocus={(event) =>
        setAnchor(event.currentTarget.getBoundingClientRect())
      }
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) setAnchor(null);
      }}
    >
      {children}
      {anchor && (
        <span
          ref={hint}
          className="cli pointer-events-none fixed z-50 w-max [overflow-wrap:anywhere]"
          style={{ left: 8, top: 8 }}
        >
          {cli}
        </span>
      )}
    </span>
  );
}
