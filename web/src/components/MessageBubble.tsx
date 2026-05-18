import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import type { Message } from '../types';
import { ToolCallCard } from './ToolCallCard';

/*
 * detect: false stops rehype-highlight from guessing a language for unlabeled
 * inline code like `foo`, which otherwise produces visually-noisy highlight
 * classes. Only fenced blocks with an explicit language are highlighted.
 * ignoreMissing skips fenced blocks whose declared language is not registered.
 */
const rehypePlugins: Parameters<typeof ReactMarkdown>[0]['rehypePlugins'] = [
  [rehypeHighlight, { detect: false, ignoreMissing: true }],
];
const remarkPlugins = [remarkGfm];

/*
 * Markdown tables render as display:table, which sizes to their content and
 * ignores the flex parent's min-width:0. Wrapping in a scroll container
 * preserves normal column-layout while keeping wide tables inside the bubble.
 */
const markdownComponents: Parameters<typeof ReactMarkdown>[0]['components'] = {
  table: ({ node: _node, ...props }) => (
    <div className="table-scroll">
      <table {...props} />
    </div>
  ),
};

interface Props {
  message: Message;
  onRemember?: (m: Message) => void;
  onCorrect?: (m: Message) => void;
}

const roleLabel: Record<Message['role'], string> = {
  user: 'You',
  assistant: 'AI',
  system: 'SYS',
  tool: 'TL',
};

export function MessageBubble({ message, onRemember, onCorrect }: Props) {
  if (message.role === 'tool') return null;

  const showCursor = message.streaming && message.role === 'assistant';
  const showActions = !message.streaming && (message.role === 'user' || message.role === 'assistant');

  return (
    <div className={'message-row ' + message.role}>
      <div className="avatar">{roleLabel[message.role]}</div>
      <div className="bubble">
        {message.content && (
          <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
            {message.content + (showCursor ? ' ' : '')}
          </ReactMarkdown>
        )}
        {showCursor && !message.content && <div className="streaming-cursor" />}
        {message.tool_calls?.map((tc) => (
          <ToolCallCard key={tc.id} call={tc} result={message.tool_results?.find((r) => r.tool_call_id === tc.id)} />
        ))}
        {showActions && (
          <div className="message-actions">
            {message.role === 'user' && onRemember && (
              <button type="button" className="action-btn" onClick={() => onRemember(message)} title="Remember this as a project fact">
                <i className="bi bi-bookmark-plus" /> Remember
              </button>
            )}
            {message.role === 'assistant' && onCorrect && (
              <button type="button" className="action-btn action-btn-danger" onClick={() => onCorrect(message)} title="Tell the agent this answer was wrong">
                <i className="bi bi-flag" /> This was wrong
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
