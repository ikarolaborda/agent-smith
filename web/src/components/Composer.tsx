import { ClipboardEvent, FormEvent, KeyboardEvent, useRef, useState } from 'react';
import { Button, Form, InputGroup } from 'react-bootstrap';
import type { ImageAttachment } from '../types';

interface Props {
  onSend: (text: string, images: ImageAttachment[]) => void;
  onStop: () => void;
  isStreaming: boolean;
  disabled?: boolean;
  /* supportsVision gates image paste: when false, pasted images are ignored with a hint. */
  supportsVision?: boolean;
}

function readAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error ?? new Error('read failed'));
    r.readAsDataURL(file);
  });
}

export function Composer({ onSend, onStop, isStreaming, disabled, supportsVision }: Props) {
  const [text, setText] = useState('');
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const [hint, setHint] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);

  function submit(e?: FormEvent) {
    e?.preventDefault();
    const v = text.trim();
    if ((!v && images.length === 0) || isStreaming) return;
    setText('');
    setImages([]);
    setHint(null);
    onSend(v, images);
    requestAnimationFrame(() => ref.current?.focus());
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  }

  async function onPaste(e: ClipboardEvent<HTMLTextAreaElement>) {
    const imageItems = Array.from(e.clipboardData.items).filter((it) => it.type.startsWith('image/'));
    if (imageItems.length === 0) return;
    /* An image is on the clipboard; take over so it is not pasted as noise text. */
    e.preventDefault();
    if (!supportsVision) {
      setHint('The selected model does not support images.');
      return;
    }
    const files = imageItems.map((it) => it.getAsFile()).filter((f): f is File => f !== null);
    try {
      const urls = await Promise.all(files.map(readAsDataURL));
      setImages((prev) => [...prev, ...urls.map((url) => ({ url }))]);
      setHint(null);
    } catch {
      setHint('Could not read the pasted image.');
    }
  }

  function removeImage(idx: number) {
    setImages((prev) => prev.filter((_, i) => i !== idx));
  }

  const canSend = (!!text.trim() || images.length > 0) && !disabled;

  return (
    <Form className="composer" onSubmit={submit}>
      {images.length > 0 && (
        <div className="composer-attachments">
          {images.map((img, i) => (
            <div className="composer-thumb" key={i}>
              <img src={img.url} alt={`pasted attachment ${i + 1}`} />
              <button type="button" className="composer-thumb-remove" onClick={() => removeImage(i)} aria-label="Remove image">
                ×
              </button>
            </div>
          ))}
        </div>
      )}
      <InputGroup>
        <Form.Control
          as="textarea"
          ref={ref}
          rows={2}
          placeholder={
            disabled
              ? 'No provider available'
              : supportsVision
                ? 'Send a message…  (Enter to send, Shift+Enter for newline, paste an image to attach)'
                : 'Send a message…  (Enter to send, Shift+Enter for newline)'
          }
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          disabled={disabled || isStreaming}
        />
        {isStreaming ? (
          <Button variant="outline-danger" onClick={onStop}>
            <i className="bi bi-stop-circle me-1" /> Stop
          </Button>
        ) : (
          <Button type="submit" variant="primary" disabled={!canSend}>
            <i className="bi bi-send me-1" /> Send
          </Button>
        )}
      </InputGroup>
      {hint && <div className="composer-hint">{hint}</div>}
    </Form>
  );
}
