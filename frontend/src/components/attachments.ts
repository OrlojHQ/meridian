// Composer attachments. The server decides what is stored and where; these
// checks exist so a bad file fails before the request, with the same limits.

export const MAX_ATTACHMENT_BYTES = 4 * 1024 * 1024;
export const MAX_ATTACHMENTS = 8;
export const MAX_ATTACHMENT_TOTAL_BYTES = 8 * 1024 * 1024;

const FILE_EXTENSIONS = new Set([
  ".txt", ".md", ".markdown", ".json", ".jsonl", ".csv", ".tsv", ".pdf", ".log",
  ".yaml", ".yml", ".xml", ".html", ".css", ".patch", ".diff", ".zip", ".gz",
  ".tgz", ".tar", ".toml", ".ini", ".cfg", ".conf", ".py", ".go", ".ts", ".tsx",
  ".js", ".jsx", ".rs", ".rb", ".java", ".c", ".h", ".cc", ".cpp", ".hpp",
  ".sql", ".svg", ".sh",
]);

export type PendingAttachment = {
  id: string;
  name: string;
  mediaType: string;
  kind: "image" | "file";
  size: number;
  data: string;
  preview?: string;
};

export type AttachmentPayload = {
  name: string;
  mediaType: string;
  data: string;
};

export function attachmentPayload(items: readonly PendingAttachment[]): AttachmentPayload[] {
  return items.map(({ name, mediaType, data }) => ({ name, mediaType, data }));
}

export function revokeAttachments(items: readonly PendingAttachment[]) {
  for (const item of items) {
    if (item.preview) URL.revokeObjectURL(item.preview);
  }
}

export async function readAttachment(
  file: File,
  images: boolean,
): Promise<{ ok: true; attachment: PendingAttachment } | { ok: false; error: string }> {
  if (file.size <= 0) return { ok: false, error: "This file is empty." };
  if (file.size > MAX_ATTACHMENT_BYTES) {
    return { ok: false, error: "This file is larger than 4 MB." };
  }
  const name = safeAttachmentName(file.name);
  if (!name) return { ok: false, error: "This file needs a simpler name." };
  const header = new Uint8Array(await file.slice(0, 16).arrayBuffer());
  const sniffed = sniffImage(header);
  let kind: PendingAttachment["kind"];
  let mediaType: string;
  if (sniffed) {
    if (!images) return { ok: false, error: "This model can't take images." };
    if (!imageNameMatches(name, sniffed)) {
      return { ok: false, error: "This image's name doesn't match its contents." };
    }
    kind = "image";
    mediaType = sniffed;
  } else if (!FILE_EXTENSIONS.has(extensionOf(name))) {
    return { ok: false, error: "This file type can't be attached." };
  } else {
    kind = "file";
    mediaType = file.type || "application/octet-stream";
  }
  const data = await fileToBase64(file);
  return {
    ok: true,
    attachment: {
      id: newAttachmentId(),
      name,
      mediaType,
      kind,
      size: file.size,
      data,
      preview: kind === "image" ? URL.createObjectURL(file) : undefined,
    },
  };
}

export async function takeAttachments(
  current: readonly PendingAttachment[],
  files: readonly File[],
  images: boolean,
): Promise<{ items: PendingAttachment[]; error?: string }> {
  const items = [...current];
  let total = items.reduce((sum, item) => sum + item.size, 0);
  let error: string | undefined;
  for (const file of files) {
    if (items.length >= MAX_ATTACHMENTS) {
      error = "You can attach up to 8 files.";
      break;
    }
    const result = await readAttachment(file, images);
    if (!result.ok) {
      error = result.error;
      break;
    }
    if (total + result.attachment.size > MAX_ATTACHMENT_TOTAL_BYTES) {
      if (result.attachment.preview) URL.revokeObjectURL(result.attachment.preview);
      error = "These attachments are larger than 8 MB together.";
      break;
    }
    items.push(result.attachment);
    total += result.attachment.size;
  }
  return { items, error };
}

export function filesFromList(list: FileList | readonly File[] | null | undefined): File[] {
  if (!list) return [];
  return Array.from(list).filter((file) => file.size >= 0);
}

export function safeAttachmentName(value: string): string {
  const base = value.replaceAll("\\", "/").split("/").pop()?.trim() ?? "";
  if (!base || base === "." || base === "..") return "";
  let name = "";
  for (const char of base) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f || code > 0x7e) name += "_";
    else if (/[A-Za-z0-9._-]/.test(char)) name += char;
    else name += "_";
  }
  name = name.replace(/^[._-]+|[._-]+$/g, "");
  if (!name || name.length > 80 || !extensionOf(name) || name.startsWith(".")) return "";
  return name;
}

function extensionOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot <= 0 ? "" : name.slice(dot).toLowerCase();
}

function imageNameMatches(name: string, media: string): boolean {
  const ext = extensionOf(name);
  if (media === "image/png") return ext === ".png";
  if (media === "image/jpeg") return ext === ".jpg" || ext === ".jpeg";
  if (media === "image/gif") return ext === ".gif";
  if (media === "image/webp") return ext === ".webp";
  return false;
}

export function sniffImage(data: Uint8Array): string | undefined {
  if (data.length >= 8 && String.fromCharCode(...data.slice(0, 8)) === "\x89PNG\r\n\x1a\n") {
    return "image/png";
  }
  if (data.length >= 3 && data[0] === 0xff && data[1] === 0xd8 && data[2] === 0xff) return "image/jpeg";
  if (data.length >= 6) {
    const gif = String.fromCharCode(...data.slice(0, 6));
    if (gif === "GIF87a" || gif === "GIF89a") return "image/gif";
  }
  if (
    data.length >= 12 &&
    String.fromCharCode(...data.slice(0, 4)) === "RIFF" &&
    String.fromCharCode(...data.slice(8, 12)) === "WEBP"
  ) {
    return "image/webp";
  }
  return undefined;
}

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const value = typeof reader.result === "string" ? reader.result : "";
      const comma = value.indexOf(",");
      resolve(comma >= 0 ? value.slice(comma + 1) : value);
    };
    reader.onerror = () => reject(reader.error ?? new Error("read failed"));
    reader.readAsDataURL(file);
  });
}

function newAttachmentId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return `attachment-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

const FILE_ACCEPT = [...FILE_EXTENSIONS].join(",");
const IMAGE_ACCEPT = "image/png,image/jpeg,image/gif,image/webp,.png,.jpg,.jpeg,.gif,.webp";

export function attachmentAccept(images: boolean): string {
  return images ? `${IMAGE_ACCEPT},${FILE_ACCEPT}` : FILE_ACCEPT;
}
