import type { ToolCall, ToolResult } from '../types';

interface Props {
  call: ToolCall;
  result?: ToolResult;
}

export function ToolCallCard({ call, result }: Props) {
  const isErr = result?.is_error === true;
  return (
    <div className={'tool-card' + (isErr ? ' error' : '')}>
      <div className="tool-head">
        <i className={'bi ' + (isErr ? 'bi-exclamation-triangle' : 'bi-wrench-adjustable')} />
        <span>tool: {call.name}</span>
        {!result && <span className="text-secondary ms-2">running…</span>}
      </div>
      {call.arguments && (
        <pre>{prettify(call.arguments)}</pre>
      )}
      {result && <pre>{result.content || (isErr ? '(no error message)' : '(no output)')}</pre>}
    </div>
  );
}

function prettify(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
