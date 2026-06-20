import { useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import type { Message } from '../types';
import { ToolCallCard } from './ToolCallCard';

/*
 * CodeBlock wraps a fenced code block with a copy button (Claude/ChatGPT style).
 * The copied text is read from the rendered <pre>'s textContent, which yields the
 * plain source without rehype-highlight's nested <span> markup. Inline code is
 * never wrapped because only block code is emitted inside a <pre>.
 */
function CodeBlock({ node: _node, ...props }: { node?: unknown } & React.ComponentPropsWithoutRef<'pre'>) {
  const ref = useRef<HTMLPreElement>(null);
  const [copied, setCopied] = useState(false);

  async function copy() {
    const text = ref.current?.textContent ?? '';
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      /* Fallback for browsers/contexts without the async clipboard API. */
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand('copy');
      } catch {
        /* give up silently; nothing else we can safely do */
      }
      document.body.removeChild(ta);
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  }

  return (
    <div className="code-block">
      <button type="button" className="code-copy" onClick={copy} aria-label="Copy code to clipboard">
        <i className={copied ? 'bi bi-check2' : 'bi bi-clipboard'} /> {copied ? 'Copied' : 'Copy'}
      </button>
      <pre ref={ref} {...props} />
    </div>
  );
}

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
  pre: CodeBlock,
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
        {(message.refine_rounds?.length || message.refine_summary) && (
          <div className="refine-panel">
            <div className="refine-rounds">
              {message.refine_rounds?.map((r) => (
                <div key={r.iter} className={'refine-round ' + r.status}>
                  <span className="refine-round-head">
                    <i className={r.usable ? 'bi bi-check-circle' : 'bi bi-arrow-repeat'} /> Round {r.iter} — {r.usable ? 'usable' : 'not usable'}
                    <span className="refine-round-time"> ({Math.round(r.duration_ms / 1000)}s)</span>
                  </span>
                  {r.reasons && <span className="refine-round-reasons">{r.reasons}</span>}
                  {r.failure_modes && r.failure_modes.length > 0 && (
                    <span className="refine-round-modes">{r.failure_modes.join(', ')}</span>
                  )}
                </div>
              ))}
            </div>
            {message.refine_summary && (
              <div className={'refine-verdict refine-verdict-' + message.refine_summary.status}>
                {message.refine_summary.status === 'usable' && (
                  <><i className="bi bi-shield-check" /> Evaluated &amp; usable — the most-evaluated output is shown below.</>
                )}
                {message.refine_summary.status === 'least_fabricated' && (
                  <><i className="bi bi-exclamation-triangle" /> Refinement did not reach a usable result after {message.refine_summary.rounds} round(s). The least-fabricated attempt is shown below — <strong>NOT a confirmed result</strong>.</>
                )}
                {message.refine_summary.status === 'error' && (
                  <><i className="bi bi-x-octagon" /> Refinement could not run: {message.refine_summary.reason}</>
                )}
              </div>
            )}
          </div>
        )}
        {message.content && (
          <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
            {message.content + (showCursor ? ' ' : '')}
          </ReactMarkdown>
        )}
        {message.images && message.images.length > 0 && (
          <div className="bubble-attachments">
            {message.images.map((img, i) => (
              <img key={i} src={img.url} alt={`attachment ${i + 1}`} className="bubble-image" />
            ))}
          </div>
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
