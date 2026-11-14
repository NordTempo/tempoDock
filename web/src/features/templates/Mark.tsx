/**
 * An app's monochrome mark: its Simple Icons path on a 24 unit view box,
 * or its initial when it has none.
 */
export function Mark({
  path,
  title,
  large,
}: {
  path?: string;
  title: string;
  large?: boolean;
}) {
  const box = large
    ? "size-8 rounded-[7px] text-[15px]"
    : "size-[22px] rounded-[5px] text-[11.5px]";
  return (
    <span
      aria-hidden
      className={`inline-flex flex-none items-center justify-center bg-sel font-semibold text-fg ${box}`}
    >
      {path ? (
        <svg
          viewBox="0 0 24 24"
          className={large ? "size-5" : "size-3.5"}
          fill="currentColor"
        >
          <title>{title}</title>
          <path d={path} />
        </svg>
      ) : (
        title.charAt(0).toUpperCase()
      )}
    </span>
  );
}
