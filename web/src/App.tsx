import { useEffect, useMemo, useRef, useState } from 'react';
import { Sidebar } from './components/Sidebar';
import { MessageBubble } from './components/MessageBubble';
import { Composer } from './components/Composer';
import { ProviderSelector } from './components/ProviderSelector';
import { ClusterBadge } from './components/ClusterBadge';
import { WorkspaceBar } from './components/WorkspaceBar';
import { ModelExplorer } from './components/ModelExplorer';
import { CorrectionDialog } from './components/CorrectionDialog';
import { ResearchDashboard } from './components/ResearchDashboard';
import { authenticatedFetch, getBearerToken, setBearerToken } from './auth';
import {
  deriveTitle,
  loadConversations,
  makeMessageId,
  newConversation,
  saveConversations,
} from './store';
import { streamChatCompletion } from './sse';
import { generateTitle } from './title';
import type { WireContentPart, WireMessage } from './sse';
import { getOrCreateProfileId } from './profile';
import { correction, remember } from './memory';
import type { Conversation, ImageAttachment, Message, ModelsResponse, ProvidersResponse, ToolCall } from './types';

export function App() {
  const [conversations, setConversations] = useState<Conversation[]>(() => loadConversations());
  const [activeId, setActiveId] = useState<string | null>(null);
  const [models, setModels] = useState<ModelsResponse['data']>([]);
  const [showModels, setShowModels] = useState(false);
  const [defaultProvider, setDefaultProvider] = useState<string>('');
  const [isStreaming, setIsStreaming] = useState(false);
  const [transientError, setTransientError] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);
	const [view, setView] = useState<'chat' | 'research'>('chat');
	const [researchEnabled, setResearchEnabled] = useState(false);
	const [authRequired, setAuthRequired] = useState(false);
	const [authDraft, setAuthDraft] = useState('');
	const [authEpoch, setAuthEpoch] = useState(0);
  const [correctionTarget, setCorrectionTarget] = useState<{ question: string; wrong: string } | null>(null);
  const profileId = useMemo(() => getOrCreateProfileId(), []);
  const abortRef = useRef<AbortController | null>(null);
  const messageListRef = useRef<HTMLDivElement>(null);
  const latestConversationsRef = useRef(conversations);
  const saveTimerRef = useRef<number | null>(null);

  latestConversationsRef.current = conversations;

  /*
   * Persist conversations to localStorage with a short debounce. During
   * streaming the conversations array reference changes once per token, which
   * would otherwise trigger hundreds of full JSON.stringify+setItem calls per
   * response. The timer callback reads from a ref so it never persists a
   * stale snapshot.
   */
  useEffect(() => {
    if (saveTimerRef.current !== null) {
      window.clearTimeout(saveTimerRef.current);
    }
    saveTimerRef.current = window.setTimeout(() => {
      saveConversations(latestConversationsRef.current);
      saveTimerRef.current = null;
    }, 400);
    return () => {
      if (saveTimerRef.current !== null) {
        window.clearTimeout(saveTimerRef.current);
        saveTimerRef.current = null;
      }
    };
  }, [conversations]);

  /* Flush the final state on unmount so a tab close mid-stream is not lost. */
  useEffect(() => {
    return () => {
      saveConversations(latestConversationsRef.current);
    };
  }, []);

  /*
   * When streaming finishes, write the final tail of the conversation
   * immediately instead of waiting for the debounce window. Avoids losing the
   * last few tokens if the user navigates away right after the response ends.
   */
  useEffect(() => {
    if (!isStreaming) {
      if (saveTimerRef.current !== null) {
        window.clearTimeout(saveTimerRef.current);
        saveTimerRef.current = null;
      }
      saveConversations(latestConversationsRef.current);
    }
  }, [isStreaming]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [mRes, pRes] = await Promise.all([
          authenticatedFetch('/v1/models'),
          authenticatedFetch('/v1/providers'),
        ]);
		if (mRes.status === 401 || pRes.status === 401) {
			setAuthRequired(true);
			return;
		}
		if (!mRes.ok || !pRes.ok) throw new Error('model discovery failed');
		const [mBody, pBody] = await Promise.all([
			mRes.json() as Promise<ModelsResponse>, pRes.json() as Promise<ProvidersResponse>,
		]);
        if (cancelled) return;
        const chatOnly = (mBody.data ?? []).filter((m) => m.kind !== 'embedding');
        setModels(chatOnly);
		setDefaultProvider(pBody.default ?? '');
		setAuthRequired(false);
		/*
			Research availability is a server capability. Gate the Research tab (and
			its polling) on it so a build running without --research-mode never hits
			the /v1/research endpoints that would 404. Best-effort: any failure leaves
			research disabled rather than surfacing an error.
		*/
		try {
			const sRes = await authenticatedFetch('/v1/system');
			if (!cancelled && sRes.ok) {
				const sBody = await sRes.json();
				setResearchEnabled(!!sBody?.capabilities?.research_persistence);
			}
		} catch {
			/* research stays disabled */
		}
      } catch {
        if (!cancelled) setTransientError('Failed to load models from /v1/models');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [authEpoch]);

  const active = useMemo(
    () => conversations.find((c) => c.id === activeId) ?? null,
    [conversations, activeId],
  );

  useEffect(() => {
    messageListRef.current?.scrollTo({ top: messageListRef.current.scrollHeight, behavior: 'smooth' });
  }, [active?.messages.length, active?.messages[active.messages.length - 1]?.content]);

  function ensureActive(): Conversation {
    if (active) return active;
    const provider = defaultProvider || models[0]?.provider || '';
    const model = models.find((m) => m.provider === provider)?.id || models[0]?.id || '';
    const c = newConversation(provider, model);
    setConversations((prev) => [c, ...prev]);
    setActiveId(c.id);
    return c;
  }

  function updateConversation(id: string, fn: (c: Conversation) => Conversation) {
    setConversations((prev) => prev.map((c) => (c.id === id ? fn(c) : c)));
  }

  async function handleSend(text: string, images: ImageAttachment[] = []) {
    if (text.startsWith('/remember ')) {
      const body = text.slice('/remember '.length).trim();
      if (!body) {
        setTransientError('Usage: /remember &lt;text&gt;');
        return;
      }
      try {
        await remember(profileId, 'project_fact', body);
        showToast('Remembered.');
      } catch (e) {
        setTransientError((e as Error).message);
      }
      return;
    }

    const conv = ensureActive();
    const isFirstMessage = conv.messages.length === 0;
    const userMsg: Message = {
      id: makeMessageId(),
      role: 'user',
      content: text,
      images: images.length > 0 ? images : undefined,
    };
    const assistantMsg: Message = { id: makeMessageId(), role: 'assistant', content: '', streaming: true };

    updateConversation(conv.id, (c) => ({
      ...c,
      title: isFirstMessage ? deriveTitle([userMsg]) : c.title,
      messages: [...c.messages, userMsg, assistantMsg],
      updatedAt: Date.now(),
    }));

    /*
     * On the first message, deriveTitle above gives an instant placeholder
     * (the truncated prompt). In the background, ask the model to distill a
     * concise title and patch it in when it arrives. Best-effort: generateTitle
     * resolves to null on any failure, in which case the placeholder stays.
     */
    if (isFirstMessage && text.trim()) {
      const placeholder = deriveTitle([userMsg]);
      generateTitle(conv.model, text).then((distilled) => {
        if (!distilled) return;
        updateConversation(conv.id, (c) =>
          /* Only replace the auto-placeholder, never a title the user edited. */
          c.title === placeholder ? { ...c, title: distilled } : c,
        );
      });
    }

    const wireMessages: WireMessage[] = [...conv.messages, userMsg].map((m) => {
      if (m.images && m.images.length > 0) {
        const parts: WireContentPart[] = [];
        if (m.content) parts.push({ type: 'text', text: m.content });
        for (const img of m.images) parts.push({ type: 'image_url', image_url: { url: img.url } });
        return { role: m.role, content: parts };
      }
      return { role: m.role, content: m.content };
    });
    const ctl = new AbortController();
    abortRef.current = ctl;
    setIsStreaming(true);
    setTransientError(null);

    /* per-tool_call_id staging buffers */
    const toolCalls = new Map<string, ToolCall>();

    const patchAssistant = (fn: (m: Message) => Message) => {
      updateConversation(conv.id, (c) => ({
        ...c,
        messages: c.messages.map((m) => (m.id === assistantMsg.id ? fn(m) : m)),
        updatedAt: Date.now(),
      }));
    };

    const webSearch = conv.webSearch ?? defaultWebSearchFor(conv.model);
    const refine = conv.refine ?? false;
    try {
      await streamChatCompletion(
        { model: conv.model, messages: wireMessages, stream: true, profile_id: profileId, web_search: webSearch, agentic: conv.agentic, refine },
        {
          onRefineRound: (r) => {
            patchAssistant((m) => ({ ...m, refine_rounds: [...(m.refine_rounds ?? []), r] }));
          },
          onRefineSummary: (s) => {
            patchAssistant((m) => ({ ...m, refine_summary: s }));
          },
          onDelta: (delta) => {
            if (delta.content) {
              patchAssistant((m) => ({ ...m, content: m.content + (delta.content ?? '') }));
            }
            if (delta.tool_calls?.length) {
              for (const tc of delta.tool_calls) {
                if (!tc.id) continue;
                const existing = toolCalls.get(tc.id) ?? { id: tc.id, name: '', arguments: '' };
                if (tc.function?.name) existing.name = tc.function.name;
                if (tc.function?.arguments) existing.arguments = tc.function.arguments;
                toolCalls.set(tc.id, existing);
              }
              const calls = Array.from(toolCalls.values());
              patchAssistant((m) => ({ ...m, tool_calls: calls }));
            }
          },
          onToolResult: (r) => {
            patchAssistant((m) => ({
              ...m,
              tool_results: [
                ...(m.tool_results ?? []).filter((x) => x.tool_call_id !== r.tool_call_id),
                r,
              ],
            }));
          },
          onError: (msg) => {
            setTransientError(msg);
          },
          onDone: () => {
            patchAssistant((m) => ({ ...m, streaming: false }));
          },
        },
        ctl.signal,
      );
    } finally {
      setIsStreaming(false);
      abortRef.current = null;
    }
  }

  function handleStop() {
    abortRef.current?.abort();
  }

  function showToast(msg: string) {
    setToast(msg);
    window.setTimeout(() => setToast((t) => (t === msg ? null : t)), 2500);
  }

  async function handleRememberMessage(m: Message) {
    try {
      await remember(profileId, 'project_fact', m.content);
      showToast('Remembered.');
    } catch (e) {
      setTransientError((e as Error).message);
    }
  }

  function handleStartCorrection(assistant: Message) {
    if (!active) return;
    const idx = active.messages.findIndex((m) => m.id === assistant.id);
    const previousUser = active.messages
      .slice(0, idx)
      .reverse()
      .find((m) => m.role === 'user');
    setCorrectionTarget({
      question: previousUser?.content ?? '',
      wrong: assistant.content,
    });
  }

  async function submitCorrection(correct: string) {
    if (!correctionTarget) return;
    try {
      await correction(profileId, correctionTarget.question, correctionTarget.wrong, correct);
      showToast('Correction stored.');
    } catch (e) {
      setTransientError((e as Error).message);
    } finally {
      setCorrectionTarget(null);
    }
  }

  function handleNew() {
    const provider = defaultProvider || models[0]?.provider || '';
    const model = models.find((m) => m.provider === provider)?.id || models[0]?.id || '';
    const c = newConversation(provider, model);
    setConversations((prev) => [c, ...prev]);
    setActiveId(c.id);
  }

  function handleDelete(id: string) {
    setConversations((prev) => prev.filter((c) => c.id !== id));
    if (activeId === id) setActiveId(null);
  }

  function defaultWebSearchFor(modelID: string): boolean {
    return modelID.startsWith('ollama/');
  }

  function handleToggleWebSearch(next: boolean) {
    if (!active) return;
    updateConversation(active.id, (c) => ({ ...c, webSearch: next }));
  }

  function handleToggleAgentic(next: boolean) {
    if (!active) return;
    updateConversation(active.id, (c) => ({ ...c, agentic: next }));
  }

  function handleToggleRefine(next: boolean) {
    if (!active) return;
    updateConversation(active.id, (c) => ({ ...c, refine: next }));
  }

  function handleModelChange(id: string) {
    if (!active) {
      const m = models.find((x) => x.id === id);
      if (m) {
        const c = newConversation(m.provider, m.id);
        setConversations((prev) => [c, ...prev]);
        setActiveId(c.id);
      }
      return;
    }
    updateConversation(active.id, (c) => {
      const m = models.find((x) => x.id === id);
      return { ...c, model: id, provider: m?.provider ?? c.provider };
    });
  }

  const noProviders = models.length === 0;
  const selectedModelId = active?.model ?? (models[0]?.id ?? '');
  const supportsVision = !!models.find((m) => m.id === selectedModelId)?.supports_vision;

	if (authRequired) {
		return (
			<div className="auth-gate">
				<form className="auth-card" onSubmit={(event) => { event.preventDefault(); setBearerToken(authDraft); setAuthEpoch((value) => value + 1); }}>
					<div className="auth-icon"><i className="bi bi-shield-lock" /></div>
					<h1>Research mode is locked</h1>
					<p>Enter the operator-issued bearer token. It is kept only in this tab's session storage.</p>
					<label htmlFor="research-token">Bearer token</label>
					<input id="research-token" className="form-control" type="password" value={authDraft} onChange={(event) => setAuthDraft(event.target.value)} minLength={32} autoComplete="off" required autoFocus />
					<button type="submit" className="btn btn-primary w-100 mt-3">Unlock control plane</button>
				</form>
			</div>
		);
	}

  return (
    <div className="app-shell">
      <Sidebar
        conversations={conversations}
        activeId={activeId}
        onSelect={setActiveId}
        onNew={handleNew}
        onDelete={handleDelete}
      />
      <main className="main-pane">
        <header className="top-bar">
		  <div className="title">{view === 'research' ? 'Research control plane' : (active?.title ?? 'agent-smith')}</div>
		  <div className="view-switch" role="group" aria-label="Application view">
			<button type="button" className={view === 'chat' ? 'active' : ''} onClick={() => setView('chat')}><i className="bi bi-chat-dots" /> Chat</button>
			{researchEnabled && (
				<button type="button" className={view === 'research' ? 'active' : ''} onClick={() => setView('research')}><i className="bi bi-shield-check" /> Research</button>
			)}
		  </div>
		  {view === 'chat' && <>
          <ProviderSelector
            models={models}
            value={active?.model ?? (models[0]?.id ?? '')}
            onChange={handleModelChange}
            disabled={noProviders}
          />
          <label className="web-toggle" title="Search the web before answering (third-party, untrusted)">
            <input
              type="checkbox"
              checked={active?.webSearch ?? defaultWebSearchFor(active?.model ?? '')}
              onChange={(e) => handleToggleWebSearch(e.target.checked)}
              disabled={!active}
            />
            <span>Ground with web</span>
          </label>
          <label className="web-toggle" title="Agentic-RAG: the model plans and runs its own retrieval (rag_search/graph_expand) and cites sources, instead of one-shot augmentation. Best with a tool-capable reasoning model (OpenAI/Anthropic).">
            <input
              type="checkbox"
              checked={active?.agentic ?? false}
              onChange={(e) => handleToggleAgentic(e.target.checked)}
              disabled={!active}
            />
            <span>Agentic retrieval</span>
          </label>
          <label className="web-toggle" title="Judge-in-the-loop: regenerate and re-evaluate until the answer is grounded/usable. Evaluation-first; may take minutes; an honest negative is not upgraded to a confirmed result.">
            <input
              type="checkbox"
              checked={active?.refine ?? false}
              onChange={(e) => handleToggleRefine(e.target.checked)}
              disabled={!active}
            />
            <span>Refine &amp; evaluate</span>
          </label>
          <button type="button" className="btn btn-sm btn-outline-secondary" onClick={() => setShowModels(true)} title="Explore models and see detected system resources">
            <i className="bi bi-cpu" /> Models
          </button>
          <WorkspaceBar />
          <ClusterBadge />
		  </>}
		  {getBearerToken() && <button type="button" className="btn btn-sm btn-outline-secondary" title="Lock research APIs" onClick={() => { setBearerToken(''); setAuthRequired(true); }}><i className="bi bi-lock" /></button>}
        </header>
        {transientError && (
          <div className="alert alert-warning mb-0 rounded-0" role="alert">
            {transientError}
          </div>
        )}
		{view === 'research' && researchEnabled ? (
		  <ResearchDashboard onUnauthorized={() => setAuthRequired(true)} onError={setTransientError} />
		) : <>
		<div
          ref={messageListRef}
          className="message-list"
          role="log"
          aria-live="polite"
          aria-relevant="additions"
          aria-atomic="false"
        >
          {(!active || active.messages.length === 0) && (
            <div className="empty-state">
              <h2>How can I help?</h2>
              <p>Start a new conversation by sending a message below.</p>
              {noProviders && <p className="text-danger">No providers configured. Check your YAML config and API keys.</p>}
            </div>
          )}
          {active?.messages.map((m) => (
            <MessageBubble
              key={m.id}
              message={m}
              onRemember={handleRememberMessage}
              onCorrect={handleStartCorrection}
            />
          ))}
        </div>
        <Composer onSend={handleSend} onStop={handleStop} isStreaming={isStreaming} disabled={noProviders} supportsVision={supportsVision} />
		</>}
        {toast && (
          <div className="toast-message" role="status" aria-live="polite">
            {toast}
          </div>
        )}
      </main>
      {correctionTarget && (
        <CorrectionDialog
          question={correctionTarget.question}
          wrongAnswer={correctionTarget.wrong}
          onSubmit={submitCorrection}
          onCancel={() => setCorrectionTarget(null)}
        />
      )}
      <ModelExplorer show={showModels} onClose={() => setShowModels(false)} />
    </div>
  );
}
