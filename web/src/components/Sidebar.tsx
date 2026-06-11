import { Button, ListGroup } from 'react-bootstrap';
import type { Conversation } from '../types';
import { LogoMark } from './LogoMark';

interface Props {
  conversations: Conversation[];
  activeId: string | null;
  onSelect: (id: string) => void;
  onNew: () => void;
  onDelete: (id: string) => void;
}

export function Sidebar({ conversations, activeId, onSelect, onNew, onDelete }: Props) {
  return (
    <aside className="sidebar">
      <div className="sidebar-header">
        <Button variant="light" className="w-100 d-flex align-items-center justify-content-center gap-2" onClick={onNew}>
          <i className="bi bi-plus-lg" /> New chat
        </Button>
      </div>
      <ListGroup className="sidebar-list">
        {conversations.length === 0 && (
          <div className="text-secondary px-3 py-2 small">No conversations yet.</div>
        )}
        {conversations.map((c) => (
          <ListGroup.Item key={c.id} action active={c.id === activeId} onClick={() => onSelect(c.id)}>
            <span className="conv-title">{c.title}</span>
            <button
              className="delete-btn"
              title="Delete"
              onClick={(e) => {
                e.stopPropagation();
                onDelete(c.id);
              }}
            >
              <i className="bi bi-trash" />
            </button>
          </ListGroup.Item>
        ))}
      </ListGroup>
      <div className="sidebar-brand px-3 py-2 small text-secondary border-top" style={{ borderColor: '#111827' }}>
        <LogoMark size={20} className="sidebar-brand-mark" />
        <span>agent-smith</span>
      </div>
    </aside>
  );
}
