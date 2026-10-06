// Image attachments for user messages: validation, reading files into
// base64 and turning them into content blocks.

import type { ContentBlock, Message } from "../types";

export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
export const MAX_IMAGE_BYTES = 5 << 20;
// Base64 grows data by 4/3; the server reads at most 16 MiB per run request.
export const MAX_TOTAL_IMAGE_BYTES = 10 << 20;

export interface ImageAttachment {
  name: string;
  mimeType: string;
  base64: string;
  size: number;
}

interface FileInfo {
  name: string;
  type: string;
  size: number;
}

function mib(n: number): string {
  return `${(n / (1 << 20)).toFixed(n % (1 << 20) === 0 ? 0 : 1)} MB`;
}

// imageError explains why file cannot be attached next to existing, or
// returns "" when it can.
export function imageError(file: FileInfo, existing: ImageAttachment[]): string {
  const name = file.name || "image";
  if (!IMAGE_TYPES.includes(file.type)) return `${name}: only PNG, JPEG, GIF and WebP images can be attached`;
  if (file.size > MAX_IMAGE_BYTES) return `${name} is ${mib(file.size)}; images are limited to ${mib(MAX_IMAGE_BYTES)}`;
  const total = existing.reduce((n, img) => n + img.size, 0) + file.size;
  if (total > MAX_TOTAL_IMAGE_BYTES) return `${name}: attachments would total ${mib(total)}; the limit is ${mib(MAX_TOTAL_IMAGE_BYTES)}`;
  return "";
}

// base64FromDataURL strips the "data:<mime>;base64," prefix.
export function base64FromDataURL(url: string): string {
  const i = url.indexOf(",");
  return i >= 0 ? url.slice(i + 1) : url;
}

export function readImage(file: File): Promise<ImageAttachment> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      resolve({ name: file.name || "pasted image", mimeType: file.type, base64: base64FromDataURL(String(reader.result)), size: file.size });
    reader.onerror = () => reject(reader.error ?? new Error(`could not read ${file.name}`));
    reader.readAsDataURL(file);
  });
}

// userContent is the content of a user message: the text, then the images.
export function userContent(text: string, images: ImageAttachment[] = []): ContentBlock[] {
  const blocks: ContentBlock[] = [];
  if (text) blocks.push({ type: "text", text });
  for (const img of images) blocks.push({ type: "image", mime_type: img.mimeType, base64: img.base64 });
  return blocks;
}

export function imagesOf(m?: Message): ContentBlock[] {
  return (m?.content ?? []).filter((b) => b.type === "image" && (b.base64 || b.url));
}

export function imageSrc(b: ContentBlock): string {
  return b.base64 ? `data:${b.mime_type ?? "image/png"};base64,${b.base64}` : (b.url ?? "");
}

// pendingText is the placeholder shown for a message while it is sent.
export function pendingText(text: string, images: number): string {
  if (images === 0) return text;
  const note = `[${images} image${images === 1 ? "" : "s"}]`;
  return text ? `${text}\n${note}` : note;
}
