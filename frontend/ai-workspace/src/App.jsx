import React, { useEffect, useMemo, useRef, useState } from "react";
import { getLocale, translate } from "./i18n";

const workspaceSelectionStorageKey = "klovys99.ai-workspace.selection";

function getBackendBaseURL() {
  const configured = import.meta.env.VITE_KLOVYS_BASE_URL?.trim();
  if (configured) {
    return configured.replace(/\/+$/, "");
  }
  return "";
}

function readStoredWorkspaceSelection() {
  if (typeof window === "undefined") {
    return {};
  }
  try {
    const raw = window.localStorage.getItem(workspaceSelectionStorageKey);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(raw);
    return typeof parsed === "object" && parsed ? parsed : {};
  } catch {
    return {};
  }
}

function writeStoredWorkspaceSelection(selection) {
  if (typeof window === "undefined") {
    return;
  }
  window.localStorage.setItem(workspaceSelectionStorageKey, JSON.stringify({
    providerID: selection.providerID || "",
    methodID: selection.methodID || "",
    model: selection.model || "",
  }));
}

function getAvailableMethod(provider) {
  if (!provider) {
    return null;
  }
  if (provider.default_method) {
    const defaultMethod = provider.methods?.find((method) => method.id === provider.default_method);
    if (defaultMethod) {
      return defaultMethod;
    }
  }
  return provider?.methods?.find((method) => method.available) || provider?.methods?.[0] || null;
}

function buildBackendURL(baseURL, path) {
  return `${baseURL}${path}`;
}

function buildConfigState(fields) {
  const next = {};
  for (const field of fields || []) {
    next[field.key] = "";
  }
  return next;
}

function buildConfigStateWithValues(fields, currentValues = {}) {
  const next = {};
  for (const field of fields || []) {
    next[field.key] = currentValues[field.key] || "";
  }
  return next;
}

function countWords(value) {
  return value.trim() ? value.trim().split(/\s+/).length : 0;
}

function conversationTitle(conversation, fallback) {
  return conversation?.title?.trim() || fallback;
}

async function readJSON(response) {
  const raw = await response.text();
  if (!raw) {
    return {};
  }
  try {
    return JSON.parse(raw);
  } catch {
    return { message: raw };
  }
}

function ProviderStatusDot({ available }) {
  return <span className={`settings-provider-dot ${available ? "settings-provider-dot-live" : ""}`} aria-hidden="true" />;
}

function ProviderCard({ provider, selectedMethod, config, onSelectProvider, onConfigChange, onSave, saving, text }) {
  const expanded = provider.id === selectedMethod?.providerID || provider.id === selectedMethod?.selectedProviderID;
  const currentMethod = provider.id === selectedMethod?.selectedProviderID
    ? selectedMethod?.method
    : getAvailableMethod(provider);
  const hasRequiredValues = (currentMethod?.fields || [])
    .filter((field) => field.required)
    .every((field) => (config[field.key] || "").trim());

  return (
    <section className={`settings-provider-card ${expanded ? "settings-provider-card-open" : ""}`}>
      <button className="settings-provider-summary" type="button" onClick={() => onSelectProvider(provider.id)}>
        <div className="settings-provider-brand">
          <div className="settings-provider-icon">{provider.label.slice(0, 1)}</div>
          <div>
            <strong>{provider.label}</strong>
            <span>{provider.available ? text.providerConnected : text.providerNotConnected}</span>
          </div>
        </div>
        <div className="settings-provider-trailing">
          <ProviderStatusDot available={provider.available} />
          <span className="settings-provider-chevron">{expanded ? "⌃" : "⌄"}</span>
        </div>
      </button>

      {expanded ? (
        <div className="settings-provider-body">
          <div className="settings-provider-copy">
            <p>{text.providerHelp}</p>
          </div>

          {(currentMethod?.fields || []).map((field) => (
            <label key={field.key} className="workspace-field" htmlFor={`${provider.id}-${field.key}`}>
              <span className="workspace-field-label">
                {field.label}
                <span className="workspace-field-required">{text.fieldRequired}</span>
              </span>
              {field.input_type === "textarea" ? (
                <textarea
                  id={`${provider.id}-${field.key}`}
                  className="workspace-textarea workspace-textarea-small"
                  value={config[field.key] || ""}
                  onChange={(event) => onConfigChange(field.key, event.target.value)}
                  placeholder={field.placeholder || ""}
                />
              ) : (
                <input
                  id={`${provider.id}-${field.key}`}
                  className="workspace-input"
                  type={field.input_type === "password" ? "password" : "text"}
                  value={config[field.key] || ""}
                  onChange={(event) => onConfigChange(field.key, event.target.value)}
                  placeholder={field.placeholder || ""}
                />
              )}
            </label>
          ))}

          <button
            className="settings-provider-save-button"
            type="button"
            onClick={() => onSave(provider.id, currentMethod?.id)}
            disabled={!hasRequiredValues || saving}
          >
            {saving ? text.saving : text.saveKey}
          </button>
        </div>
      ) : null}
    </section>
  );
}

function ChatBubble({ role, content, label }) {
  return (
    <article className={`chat-bubble-row chat-bubble-row-${role}`}>
      <div className={`chat-bubble chat-bubble-${role}`}>
        <span className="chat-bubble-label">{label}</span>
        <p>{content}</p>
      </div>
    </article>
  );
}

export function App() {
  const [locale] = useState(getLocale());
  const text = translate(locale);
  const backendBaseURL = getBackendBaseURL();
  const dashboardURL = `${backendBaseURL}/dashboard`;
  const [providers, setProviders] = useState([]);
  const [providerID, setProviderID] = useState("");
  const [methodID, setMethodID] = useState("");
  const [model, setModel] = useState("");
  const [config, setConfig] = useState({});
  const [prompt, setPrompt] = useState("");
  const [anonymizedPrompt, setAnonymizedPrompt] = useState("");
  const [messages, setMessages] = useState([]);
  const [conversations, setConversations] = useState([]);
  const [activeConversationID, setActiveConversationID] = useState("");
  const [status, setStatus] = useState(text.idleStatus);
  const [requestError, setRequestError] = useState("");
  const [loadingProviders, setLoadingProviders] = useState(true);
  const [anonymizing, setAnonymizing] = useState(false);
  const [sending, setSending] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [savingProviderID, setSavingProviderID] = useState("");
  const anonymizeRequestRef = useRef(0);
  const threadRef = useRef(null);
  const storedSelectionRef = useRef(readStoredWorkspaceSelection());

  useEffect(() => {
    document.title = `klovys99 ${text.title}`;
  }, [text.title]);

  async function loadProviders(options = {}) {
    const {
      preserveSelection = false,
      nextProviderID = providerID,
      nextMethodID = methodID,
      nextModel = model,
      keepConfig = false,
    } = options;
    setLoadingProviders(true);
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/providers"));
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || "Failed to load providers");
      }
      setRequestError("");
      const nextProviders = payload.providers || [];
      setProviders(nextProviders);
      if (nextProviders.length === 0) {
        setProviderID("");
        setMethodID("");
        setModel("");
        setConfig({});
        return;
      }
      const preferredProviderID = preserveSelection
        ? nextProviderID
        : storedSelectionRef.current.providerID;
      const targetProvider = preserveSelection
        ? nextProviders.find((provider) => provider.id === nextProviderID) || nextProviders.find((provider) => provider.available) || nextProviders[0]
        : nextProviders.find((provider) => provider.id === preferredProviderID)
          || nextProviders.find((provider) => provider.available)
          || nextProviders[0];
      const preferredMethodID = preserveSelection
        ? nextMethodID
        : storedSelectionRef.current.methodID;
      const targetMethod = preserveSelection
        ? targetProvider?.methods?.find((method) => method.id === nextMethodID) || getAvailableMethod(targetProvider)
        : targetProvider?.methods?.find((method) => method.id === preferredMethodID) || getAvailableMethod(targetProvider);
      const preferredModel = preserveSelection
        ? nextModel
        : storedSelectionRef.current.model;
      const targetModel = preserveSelection && targetProvider?.models?.includes(nextModel)
        ? nextModel
        : !preserveSelection && targetProvider?.models?.includes(preferredModel)
          ? preferredModel
        : targetProvider?.default_model || targetProvider?.models?.[0] || "";
      const shouldKeepConfig = keepConfig && targetProvider?.id === nextProviderID && targetMethod?.id === nextMethodID;

      setProviderID(targetProvider?.id || "");
      setMethodID(targetMethod?.id || "");
      setModel(targetModel);
      setConfig((current) => (
        shouldKeepConfig
          ? buildConfigStateWithValues(targetMethod?.fields, current)
          : buildConfigState(targetMethod?.fields)
      ));
    } catch (error) {
      setRequestError(error.message);
    } finally {
      setLoadingProviders(false);
    }
  }

  const selectedProvider = useMemo(
    () => providers.find((provider) => provider.id === providerID) || null,
    [providers, providerID],
  );
  const connectedProviders = useMemo(
    () => providers.filter((provider) => provider.available),
    [providers],
  );
  const selectedMethod = useMemo(
    () => selectedProvider?.methods?.find((method) => method.id === methodID) || getAvailableMethod(selectedProvider),
    [selectedProvider, methodID],
  );
  const connectedProviderID = connectedProviders.some((provider) => provider.id === providerID)
    ? providerID
    : connectedProviders[0]?.id || "";

  useEffect(() => {
    if (!selectedProvider) {
      return;
    }
    const nextMethod = selectedProvider.methods?.find((method) => method.id === methodID) || getAvailableMethod(selectedProvider);
    setMethodID(nextMethod?.id || "");
    setModel(selectedProvider.default_model || selectedProvider.models?.[0] || "");
    setConfig(buildConfigState(nextMethod?.fields));
    setRequestError("");
  }, [providerID]);

  useEffect(() => {
    setConfig(buildConfigState(selectedMethod?.fields));
    setRequestError("");
  }, [methodID]);

  useEffect(() => {
    if (settingsOpen) {
      return;
    }
    if (connectedProviders.length === 0) {
      return;
    }
    if (connectedProviders.some((provider) => provider.id === providerID)) {
      return;
    }
    setProviderID(connectedProviders[0].id);
  }, [connectedProviders, providerID, settingsOpen]);

  useEffect(() => {
    writeStoredWorkspaceSelection({ providerID, methodID, model });
    storedSelectionRef.current = { providerID, methodID, model };
  }, [providerID, methodID, model]);

  function applyConversationSelection(conversation) {
    setActiveConversationID(conversation.id || "");
    setMessages(conversation.messages || []);
    if (conversation.provider) {
      setProviderID(conversation.provider);
    }
    if (conversation.method) {
      setMethodID(conversation.method);
    }
    if (conversation.model) {
      setModel(conversation.model);
    }
  }

  async function fetchConversationList() {
    const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/conversations"));
    const payload = await readJSON(response);
    if (!response.ok) {
      throw new Error(payload.error || "Failed to load conversations");
    }
    return payload.conversations || [];
  }

  async function loadConversation(id) {
    const response = await fetch(buildBackendURL(backendBaseURL, `/api/ai-workspace/conversations/${encodeURIComponent(id)}`));
    const payload = await readJSON(response);
    if (!response.ok) {
      throw new Error(payload.error || "Failed to load conversation");
    }
    applyConversationSelection(payload);
    return payload;
  }

  async function createConversation(options = {}) {
    const { focus = true } = options;
    const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/conversations"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
    });
    const payload = await readJSON(response);
    if (!response.ok) {
      throw new Error(payload.error || "Failed to create conversation");
    }
    if (focus) {
      applyConversationSelection(payload);
      setPrompt("");
      setAnonymizedPrompt("");
      setStatus(text.idleStatus);
      setRequestError("");
    }
    return payload;
  }

  async function syncConversations(nextConversationID = "") {
    const nextConversations = await fetchConversationList();
    setConversations(nextConversations);
    const targetID = nextConversationID || activeConversationID || nextConversations[0]?.id || "";
    if (!targetID) {
      const created = await createConversation();
      setConversations((current) => [created, ...current]);
      return created;
    }
    return loadConversation(targetID);
  }

  useEffect(() => {
    let cancelled = false;

    async function initialize() {
      try {
        await loadProviders();
        if (cancelled) {
          return;
        }
        const nextConversations = await fetchConversationList();
        if (cancelled) {
          return;
        }
        setConversations(nextConversations);
        if (nextConversations.length > 0) {
          await loadConversation(nextConversations[0].id);
          return;
        }
        const created = await createConversation({ focus: false });
        if (cancelled) {
          return;
        }
        setConversations([created]);
        applyConversationSelection(created);
      } catch (error) {
        if (!cancelled) {
          setRequestError(error.message);
        }
      }
    }

    initialize();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const trimmedPrompt = prompt.trim();
    const requestID = anonymizeRequestRef.current + 1;
    anonymizeRequestRef.current = requestID;

    if (!trimmedPrompt) {
      setAnonymizedPrompt("");
      setAnonymizing(false);
      setStatus(text.idleStatus);
      return undefined;
    }

    setStatus(text.previewStatus);
    const timeoutID = window.setTimeout(async () => {
      setAnonymizing(true);
      try {
        const response = await fetch(buildBackendURL(backendBaseURL, "/api/anonymization/test"), {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ text: prompt }),
        });
        const payload = await readJSON(response);
        if (!response.ok) {
          throw new Error(payload.error || text.anonymizationError);
        }
        if (anonymizeRequestRef.current !== requestID) {
          return;
        }
        setAnonymizedPrompt(payload.anonymized_text || "");
        setRequestError("");
        setStatus(text.anonymizedStatus);
      } catch (error) {
        if (anonymizeRequestRef.current !== requestID) {
          return;
        }
        setRequestError(error.message);
        setStatus(text.previewErrorStatus);
      } finally {
        if (anonymizeRequestRef.current === requestID) {
          setAnonymizing(false);
        }
      }
    }, 500);

    return () => {
      window.clearTimeout(timeoutID);
    };
  }, [prompt, backendBaseURL, text.idleStatus, text.previewErrorStatus, text.previewStatus, text.anonymizedStatus, text.anonymizationError]);

  useEffect(() => {
    if (!threadRef.current) {
      return;
    }
    const frameID = window.requestAnimationFrame(() => {
      threadRef.current.scrollTo({
        top: threadRef.current.scrollHeight,
        behavior: "smooth",
      });
    });
    return () => {
      window.cancelAnimationFrame(frameID);
    };
  }, [messages.length, activeConversationID]);

  function updateProvider(nextProviderID) {
    setProviderID(nextProviderID);
  }

  function updateConfig(key, value) {
    setConfig((current) => ({ ...current, [key]: value }));
  }

  async function saveProviderCredentials(targetProviderID, targetMethodID) {
    setSavingProviderID(targetProviderID);
    setRequestError("");
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, `/api/ai-workspace/providers/${encodeURIComponent(targetProviderID)}/credentials`), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ method: targetMethodID, config }),
      });
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      await loadProviders({
        preserveSelection: true,
        nextProviderID: targetProviderID,
        nextMethodID: targetMethodID,
        nextModel: model,
        keepConfig: true,
      });
    } catch (error) {
      setRequestError(error.message);
    } finally {
      setSavingProviderID("");
    }
  }

  async function startNewConversation() {
    try {
      const created = await createConversation();
      const nextConversations = await fetchConversationList();
      setConversations(nextConversations);
      applyConversationSelection(created);
    } catch (error) {
      setRequestError(error.message);
    }
  }

  async function handleSelectConversation(conversationID) {
    try {
      setRequestError("");
      await loadConversation(conversationID);
    } catch (error) {
      setRequestError(error.message);
    }
  }

  async function handleSend() {
    if (!anonymizedPrompt) {
      return;
    }
    const hasStoredProvider = providers.some((provider) => (
      provider.available || provider.methods?.some((method) => method.available)
    ));
    const hasSelectedLocalCredentials = (selectedMethod?.fields || [])
      .filter((field) => field.required)
      .every((field) => (config[field.key] || "").trim());
    if ((!hasStoredProvider && !hasSelectedLocalCredentials) || (!selectedProvider?.available && !hasSelectedLocalCredentials)) {
      setSettingsOpen(true);
      return;
    }
    setSending(true);
    setRequestError("");
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/complete"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          conversation_id: activeConversationID,
          provider: providerID,
          method: methodID,
          model,
          anonymized_prompt: anonymizedPrompt,
          config,
        }),
      });
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      const nextConversationID = payload.conversation_id || activeConversationID;
      setPrompt("");
      setAnonymizedPrompt("");
      setStatus(text.responseStatus);
      await loadProviders({
        preserveSelection: true,
        nextProviderID: providerID,
        nextMethodID: methodID,
        nextModel: model,
        keepConfig: true,
      });
      await syncConversations(nextConversationID);
    } catch (error) {
      setRequestError(error.message);
    } finally {
      setSending(false);
    }
  }

  const configFields = selectedMethod?.fields || [];
  const completedConfigFields = configFields.filter((field) => (config[field.key] || "").trim()).length;
  const hasSelectedLocalCredentials = configFields
    .filter((field) => field.required)
    .every((field) => (config[field.key] || "").trim());
  const hasConfiguredProvider = providers.some((provider) => (
    provider.available || provider.methods?.some((method) => method.available)
  )) || hasSelectedLocalCredentials;
  const canSend = Boolean(anonymizedPrompt && !sending && !anonymizing);

  return (
    <div className="chat-app-shell">
      <aside className="chat-sidebar">
        <div className="chat-sidebar-header">
          <a className="brand" href={dashboardURL} aria-label="klovys99 dashboard">
            <img src="/dashboard/assets/klovys99-logo.png" alt="klovys99" className="brand-logo" />
          </a>
        </div>

        <button className="chat-new-conversation" type="button" onClick={startNewConversation}>
          + {text.newConversation}
        </button>

        <div className="chat-history">
          <span className="chat-history-label">{text.historyLabel}</span>
          {conversations.map((conversation) => (
            <button
              key={conversation.id}
              className={`chat-history-item ${conversation.id === activeConversationID ? "chat-history-item-active" : ""}`}
              type="button"
              onClick={() => handleSelectConversation(conversation.id)}
            >
              {conversationTitle(conversation, text.newConversationLabel)}
            </button>
          ))}
        </div>

        <div className="chat-sidebar-footer">
          <button className="chat-settings-button" type="button" onClick={() => setSettingsOpen(true)}>
            {text.settings}
          </button>
        </div>
      </aside>

      <main className="chat-main">
        <section ref={threadRef} className="chat-thread">
          {messages.length === 0 ? (
            <div className="chat-empty-state">
              <h1>{text.welcomeTitle}</h1>
            </div>
          ) : (
            <div className="chat-message-list">
              {messages.map((message, index) => (
                <ChatBubble
                  key={`${message.created_at || index}-${index}`}
                  role={message.role}
                  content={message.content}
                  label={message.role === "user" ? text.sentLabel : `${selectedProvider?.label || "AI"} ${text.receivedLabel}`}
                />
              ))}
            </div>
          )}
        </section>

        <section className="chat-composer-wrap">
          <div className="chat-anonymized-preview">
            <pre className={`chat-anonymized-preview-body ${anonymizedPrompt ? "" : "chat-anonymized-preview-body-empty"}`}>
              {anonymizedPrompt || (anonymizing ? text.previewPending : text.noAnonymizedPrompt)}
            </pre>
          </div>

          <div className="chat-composer">
            <textarea
              id="prompt"
              className="chat-composer-input"
              value={prompt}
              onChange={(event) => {
                setPrompt(event.target.value);
                setRequestError("");
              }}
              placeholder={text.promptPlaceholder}
            />
            <button className="chat-send-button" type="button" onClick={handleSend} disabled={!canSend}>
              ↑
            </button>

            <div className="chat-composer-footer">
              <div className="chat-composer-right">
                {!hasConfiguredProvider ? (
                  <button className="chat-config-pill" type="button" onClick={() => setSettingsOpen(true)}>
                    {text.configureAccount}
                  </button>
                ) : null}
                {connectedProviders.length > 0 ? (
                  <label className="chat-provider-select-wrap" htmlFor="provider">
                    <select
                      id="provider"
                      className="chat-provider-select"
                      value={connectedProviderID}
                      onChange={(event) => updateProvider(event.target.value)}
                    >
                      {connectedProviders.map((provider) => (
                        <option key={provider.id} value={provider.id}>
                          {provider.label} · {text.providerConnected}
                        </option>
                      ))}
                    </select>
                  </label>
                ) : null}
                <label className="chat-model-select-wrap" htmlFor="model">
                  <select
                    id="model"
                    className="chat-model-select"
                    value={model}
                    onChange={(event) => setModel(event.target.value)}
                    disabled={!selectedProvider || !hasConfiguredProvider}
                  >
                    {(selectedProvider?.models || []).map((providerModel) => (
                      <option key={providerModel} value={providerModel}>{providerModel}</option>
                    ))}
                  </select>
                </label>
              </div>
            </div>

            {requestError ? (
              <div className="workspace-alert workspace-alert-danger">
                <strong>{text.requestError}</strong>
                <p>{requestError}</p>
              </div>
            ) : null}
          </div>
        </section>
      </main>

      {settingsOpen ? (
        <div className="settings-modal-backdrop" role="presentation" onClick={() => setSettingsOpen(false)}>
          <section className="settings-modal" role="dialog" aria-modal="true" aria-labelledby="settingsTitle" onClick={(event) => event.stopPropagation()}>
            <header className="settings-modal-header">
              <h2 id="settingsTitle">{text.settingsTitle}</h2>
              <button className="settings-close-button" type="button" onClick={() => setSettingsOpen(false)}>×</button>
            </header>

            <div className="settings-modal-body">
              <div className="settings-modal-content">
                <div className="settings-provider-list">
                  {providers.map((provider) => (
                    <ProviderCard
                      key={provider.id}
                      provider={provider}
                      selectedMethod={{ method: selectedMethod, selectedProviderID: providerID }}
                      config={provider.id === providerID ? config : {}}
                      onSelectProvider={updateProvider}
                      onConfigChange={updateConfig}
                      onSave={saveProviderCredentials}
                      saving={savingProviderID === provider.id}
                      text={text}
                    />
                  ))}
                </div>

                <div className="settings-summary-bar">
                  <div className="metric-card">
                    <span>{text.activeProvider}</span>
                    <strong>{selectedProvider?.label || text.providerUnavailable}</strong>
                  </div>
                  <div className="metric-card">
                    <span>{text.statusLabel}</span>
                    <strong>{completedConfigFields}/{configFields.length}</strong>
                  </div>
                  <div className="metric-card">
                    <span>{text.promptWords}</span>
                    <strong>{countWords(prompt)}</strong>
                  </div>
                </div>
              </div>
            </div>
          </section>
        </div>
      ) : null}
    </div>
  );
}
