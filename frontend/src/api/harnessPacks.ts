import type { HarnessImage } from "./generated/types.gen";

/** The part of a catalog entry that a Project stores. */
export function projectHarnessImage(pack: HarnessImage): HarnessImage {
  return { name: pack.name, imageReference: pack.imageReference };
}

type ImageParts = { repository: string; tag: string; digest: string };

// Mirrors domain.splitImageRef: a registry port is not a tag.
function imageParts(reference: string): ImageParts {
  let rest = reference.trim();
  let digest = "";
  const at = rest.lastIndexOf("@");
  if (at >= 0) {
    digest = rest.slice(at + 1);
    rest = rest.slice(0, at);
  }
  let tag = "";
  const colon = rest.lastIndexOf(":");
  if (colon >= 0 && !rest.slice(colon + 1).includes("/")) {
    tag = rest.slice(colon + 1);
    rest = rest.slice(0, colon);
  }
  return { repository: rest, tag, digest };
}

// Mirrors domain.releaseTag: v0.4.1, or a prerelease such as v0.5.0-rc.1.
const releaseTag = /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;

/** The release tag of an image reference, such as v0.4.0, if it has one. */
export function imageRelease(reference: string): string | undefined {
  const { tag, digest } = imageParts(reference);
  return digest === "" && releaseTag.test(tag) ? tag : undefined;
}

/**
 * The installation catalog entry that a pack is a copy of: the same harness
 * name and either the same image, or the same official repository at another
 * release tag. Mirrors domain.CatalogPack (ADR 0041). A digest-pinned or
 * custom-tagged image matches only exactly.
 */
export function catalogPack(
  pack: HarnessImage,
  catalog: readonly HarnessImage[],
): HarnessImage | undefined {
  const parts = imageParts(pack.imageReference);
  return catalog.find((item) => {
    if (item.name !== pack.name) return false;
    if (item.imageReference === pack.imageReference) return true;
    const entry = imageParts(item.imageReference);
    return (
      parts.digest === "" &&
      entry.digest === "" &&
      releaseTag.test(parts.tag) &&
      entry.repository === parts.repository
    );
  });
}

/**
 * Whether a Project's pack can be asked for a structured session before its
 * Capsule exists. Only an installation catalog entry declares interaction
 * modes, and an official pack saved under an older release is that entry.
 * Any other pack, such as a custom image, gets its profiles from the
 * repository, so it stays possible until the daemon says otherwise.
 */
export function packMayRunStructured(
  pack: HarnessImage,
  catalog: readonly HarnessImage[],
): boolean {
  const entry = catalogPack(pack, catalog);
  return entry?.interactionModes === undefined || entry.interactionModes.includes("structured");
}

/** Whether a Project's pack can open a terminal session, by the same rule. */
export function packMayRunNative(
  pack: HarnessImage,
  catalog: readonly HarnessImage[],
): boolean {
  const entry = catalogPack(pack, catalog);
  return entry?.interactionModes === undefined || entry.interactionModes.includes("native");
}

/**
 * The older release a Capsule's image was built from, when new Capsules of
 * the same harness now use a different release. Existing Capsules keep the
 * image they were created with, so this is only shown, never changed.
 */
export function olderPackRelease(
  harness: string | undefined,
  imageReference: string | undefined,
  catalog: readonly HarnessImage[],
): { release: string; current: string } | undefined {
  if (!harness || !imageReference) return undefined;
  const entry = catalogPack({ name: harness, imageReference }, catalog);
  const release = imageRelease(imageReference);
  const current = entry && imageRelease(entry.imageReference);
  if (!release || !current || release === current) return undefined;
  return { release, current };
}
