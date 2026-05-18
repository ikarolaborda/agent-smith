import { useState } from 'react';
import { Button, Form, Modal } from 'react-bootstrap';

interface Props {
  question: string;
  wrongAnswer: string;
  onSubmit: (correctAnswer: string) => void;
  onCancel: () => void;
}

export function CorrectionDialog({ question, wrongAnswer, onSubmit, onCancel }: Props) {
  const [text, setText] = useState('');
  return (
    <Modal show onHide={onCancel} centered>
      <Modal.Header closeButton>
        <Modal.Title>Tell the agent the correct answer</Modal.Title>
      </Modal.Header>
      <Modal.Body>
        <div className="small text-secondary mb-2">Question</div>
        <div className="correction-quote">{question || '(no preceding user message)'}</div>
        <div className="small text-secondary mt-3 mb-2">What the agent said</div>
        <div className="correction-quote correction-wrong">{wrongAnswer.slice(0, 400)}{wrongAnswer.length > 400 ? '…' : ''}</div>
        <Form.Group className="mt-3">
          <Form.Label className="small text-secondary">Correct answer (will be remembered)</Form.Label>
          <Form.Control
            as="textarea"
            rows={4}
            value={text}
            onChange={(e) => setText(e.target.value)}
            autoFocus
            placeholder="What should the agent have said?"
          />
        </Form.Group>
      </Modal.Body>
      <Modal.Footer>
        <Button variant="secondary" onClick={onCancel}>Cancel</Button>
        <Button variant="primary" disabled={!text.trim()} onClick={() => onSubmit(text.trim())}>Store correction</Button>
      </Modal.Footer>
    </Modal>
  );
}
