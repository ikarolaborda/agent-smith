import { Form } from 'react-bootstrap';

interface ModelOption {
  id: string;
  provider: string;
  model: string;
}

interface Props {
  models: ModelOption[];
  value: string;
  onChange: (id: string) => void;
  disabled?: boolean;
}

export function ProviderSelector({ models, value, onChange, disabled }: Props) {
  return (
    <Form.Select
      size="sm"
      style={{ width: 260 }}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      aria-label="Model"
    >
      {models.length === 0 && <option value="">No models available</option>}
      {models.map((m) => (
        <option key={m.id} value={m.id}>
          {m.provider} — {m.model}
        </option>
      ))}
    </Form.Select>
  );
}
