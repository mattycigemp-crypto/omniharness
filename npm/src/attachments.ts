/** How an attached file is presented to the model. Images ride the modality bridge as data URLs. */
export type AttachKind = 'file' | 'image' | 'video';

export interface AttachmentInput {
  name: string;
  size: number;
  kind: AttachKind;
}

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|bmp|svg|avif)$/i;
const VIDEO_EXT = /\.(mp4|webm|mov|mkv|avi)$/i;

export function kindFromName(name: string): AttachKind {
  if (IMAGE_EXT.test(name)) return 'image';
  if (VIDEO_EXT.test(name)) return 'video';
  return 'file';
}

/** Format the attachments block prepended to a prompt so the agent knows what was attached. */
export function attachmentBlock(attachments: readonly AttachmentInput[]): string {
  if (attachments.length === 0) return '';
  const rows = attachments.map((a) => `- ${a.name} (${a.kind}, ${a.size} bytes)`).join('\n');
  return `The user attached these files to this request:\n${rows}\nImages were sent as vision input; read_file the others from the workspace or user path as needed.\n---\n`;
}
