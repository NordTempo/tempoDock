/**
 * Whether an image is one Dock built from source. Its tag (buildTag in
 * internal/core/releases.go, "dock/p12_e31_s34:31") holds only internal
 * IDs and the release number, so lists leave it out: the release number
 * already says which build it is. Titles and Details keep the full
 * reference.
 */
export const dockBuilt = (ref: string) =>
  /(?:^|\/)dock\/p\d+_e\d+_s\d+:\d+$/.test(ref);
