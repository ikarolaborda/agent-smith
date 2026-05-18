import { FormEvent, KeyboardEvent, useRef, useState } from 'react';
import { Button, Form, InputGroup } from 'react-bootstrap';

interface Props {
  onSend: (text: string) => void;
  onStop: () => void;
  isStreaming: boolean;
  disabled?: boolean;
}

export function Composer({ onSend, onStop, isStreaming, disabled }: Props) {
  const [text, setText] = useState('');
  const ref = useRef<HTMLTextAreaElement>(null);

  function submit(e?: FormEvent) {
    e?.preventDefault();
    const v = text.trim();
    if (!v || isStreaming) return;
    setText('');
    onSend(v);
    requestAnimationFrame(() => ref.current?.focus());
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  }

  return (
    <Form className="composer" onSubmit={submit}>
      <InputGroup>
        <Form.Control
          as="textarea"
          ref={ref}
          rows={2}
          placeholder={disabled ? 'No provider available' : 'Send a message…  (Enter to send, Shift+Enter for newline)'}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          disabled={disabled || isStreaming}
        />
        {isStreaming ? (
          <Button variant="outline-danger" onClick={onStop}>
            <i className="bi bi-stop-circle me-1" /> Stop
          </Button>
        ) : (
          <Button type="submit" variant="primary" disabled={disabled || !text.trim()}>
            <i className="bi bi-send me-1" /> Send
          </Button>
        )}
      </InputGroup>
    </Form>
  );
}
