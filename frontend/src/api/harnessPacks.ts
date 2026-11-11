import type { HarnessImage } from "./generated/types.gen";

/** The part of a catalog entry that a Project stores. */
export function projectHarnessImage(pack: HarnessImage): HarnessImage {
  return { name: pack.name, imageReference: pack.imageReference };
}

/**
 * Whether a Project's pack can be asked for a structured session before its
 * Capsule exists. Only an installation catalog entry with the same name and
 * image declares interaction modes. Any other pack, such as a custom image,
 * gets its profiles from the repository, so it stays possible until the
 * daemon says otherwise.
 */
export function packMayRunStructured(
  pack: HarnessImage,
  catalog: readonly HarnessImage[],
): boolean {
  const entry = catalog.find(
    (item) => item.name === pack.name && item.imageReference === pack.imageReference,
  );
  return entry?.interactionModes === undefined || entry.interactionModes.includes("structured");
}

/** Whether a Project's pack can open a terminal session, by the same rule. */
export function packMayRunNative(
  pack: HarnessImage,
  catalog: readonly HarnessImage[],
): boolean {
  const entry = catalog.find(
    (item) => item.name === pack.name && item.imageReference === pack.imageReference,
  );
  return entry?.interactionModes === undefined || entry.interactionModes.includes("native");
}
