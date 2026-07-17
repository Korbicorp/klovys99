import React, { useEffect, useMemo, useRef, useState } from "react";
import { getLocale, translate } from "./i18n";

const workspaceSelectionStorageKey = "klovys99.ai-workspace.selection";
const acceptedAttachmentTypes = "application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet,application/vnd.ms-excel,text/plain,text/csv,application/csv,image/*";
const acceptedAttachmentExtensions = [".pdf", ".docx", ".xls", ".xlsx", ".txt", ".csv"];

function isSupportedAttachment(file) {
  if (!file) return false;
  if (file.type?.startsWith("image/")) return true;
  if (acceptedAttachmentTypes.split(",").includes(file.type)) return true;
  const filename = file.name?.toLowerCase() || "";
  return acceptedAttachmentExtensions.some((extension) => filename.endsWith(extension));
}

function hasDraggedFiles(dataTransfer) {
  return Array.from(dataTransfer?.types || []).includes("Files");
}

function normalizeTextAttachment(file) {
  const filename = file.name?.toLowerCase() || "";
  const mediaType = filename.endsWith(".csv") ? "text/csv" : filename.endsWith(".txt") ? "text/plain" : "";
  if (!mediaType || file.type === mediaType) return file;
  return new File([file], file.name, { type: mediaType, lastModified: file.lastModified });
}

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

function normalizeClaudeOAuthStatus(status = {}) {
  return {
    linked: Boolean(status.linked),
    pending: Boolean(status.pending),
    method: status.method || "oauth_token",
    authorizationURL: status.authorization_url || status.authorizationURL || "",
    requiresCode: Boolean(status.requires_code || status.requiresCode),
  };
}

function buildClaudeOAuthMethod(text, status) {
  return {
    id: "oauth_token",
    label: text.claudeOAuthMethodLabel,
    available: Boolean(status?.linked),
    fields: [],
  };
}

function mergeClaudeProvider(provider, status, text) {
  if (!provider || provider.id !== "claude") {
    return provider;
  }
  const oauthMethod = buildClaudeOAuthMethod(text, status);
  let hasOAuthMethod = false;
  const methods = (provider.methods || []).map((method) => {
    if (method.id !== "oauth_token") {
      return method;
    }
    hasOAuthMethod = true;
    return {
      ...method,
      ...oauthMethod,
      label: method.label || oauthMethod.label,
    };
  });
  if (!hasOAuthMethod) {
    methods.push(oauthMethod);
  }
  return {
    ...provider,
    available: provider.available || methods.some((method) => method.available),
    methods,
  };
}

function mergeProvidersWithClaudeOAuth(providers, status, text) {
  return (providers || []).map((provider) => mergeClaudeProvider(provider, status, text));
}

function conversationTitle(conversation, fallback) {
  return conversation?.title?.trim() || fallback;
}

function buildOptimisticMessage(role, content, options = {}) {
  return {
    id: options.id || `optimistic-${role}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    role,
    content,
    created_at: new Date().toISOString(),
    pending: Boolean(options.pending),
  };
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

const previewEntityTypes = [
  "EMAIL",
  "IP",
  "PHONE",
  "NIR",
  "ADDRESS",
  "IBAN",
  "CREDIT_CARD",
  "MAC_ADDRESS",
  "CRYPTO",
  "SECRET",
  "GENERIC_ID",
  "NUMERIC_ID",
  "REFERENCE_ID",
  "NAME",
  "LOCATION",
  "ORGANIZATION",
  "CONTEXT_IDENTIFIER",
  "OTHER_PII",
  "DATE",
  "BLOOD_TYPE",
  "DOCUMENT_ID",
  "VEHICLE_PLATE",
  "MEDICAL_PROVIDER",
  "SCHOOL",
  "EMPLOYER",
  "PET_IDENTIFIER",
];

const unknownTypeTheme = {
  background: "rgba(7, 108, 216, 0.15)",
  border: "rgba(7, 108, 216, 0.18)",
  text: "#084b9a",
};

function safeNumber(value) {
  const numeric = Number(value);
  return Number.isFinite(numeric) ? numeric : 0;
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll("\"", "&quot;")
    .replaceAll("'", "&#39;");
}

function buildTypeColorThemes(types) {
  return Object.fromEntries(
    types.map((type, index) => {
      const hue = Math.round((index * 137.508) % 360);
      const saturation = index % 2 === 0 ? 72 : 64;
      const backgroundLightness = index % 3 === 0 ? 89 : index % 3 === 1 ? 86 : 83;
      const borderLightness = backgroundLightness - 11;
      const textLightness = index % 2 === 0 ? 24 : 28;
      return [
        type,
        {
          background: `hsl(${hue} ${saturation}% ${backgroundLightness}%)`,
          border: `hsl(${hue} ${Math.max(saturation - 10, 46)}% ${borderLightness}%)`,
          text: `hsl(${hue} ${Math.min(saturation + 6, 82)}% ${textLightness}%)`,
        },
      ];
    }),
  );
}

const previewTypeColorThemes = buildTypeColorThemes(previewEntityTypes);

function themeForType(type) {
  return previewTypeColorThemes[type] || unknownTypeTheme;
}

function styleAttributeForType(type) {
  const theme = themeForType(type);
  return [
    `--type-highlight-bg: ${theme.background}`,
    `--type-highlight-border: ${theme.border}`,
    `--type-highlight-text: ${theme.text}`,
  ]
    .map((part) => escapeHtml(part))
    .join("; ");
}

function byteOffsetToStringIndex(text, byteOffset) {
  const encoder = new TextEncoder();
  let consumedBytes = 0;
  let stringIndex = 0;

  for (const char of String(text)) {
    if (consumedBytes >= byteOffset) {
      break;
    }
    consumedBytes += encoder.encode(char).length;
    stringIndex += char.length;
  }

  return stringIndex;
}

function normalizePreviewFindings(findings, sourceText = "") {
  if (!Array.isArray(findings)) {
    return [];
  }
  return findings
    .map((finding) => {
      const startByteOffset = safeNumber(finding.start);
      const endByteOffset = safeNumber(finding.end);
      return {
        type: String(finding.type || "UNKNOWN"),
        value: String(finding.value || ""),
        token: String(finding.token || ""),
        start: byteOffsetToStringIndex(sourceText, startByteOffset),
        end: byteOffsetToStringIndex(sourceText, endByteOffset),
      };
    })
    .filter((finding) => finding.end >= finding.start)
    .sort((left, right) => left.start - right.start || left.end - right.end);
}

function normalizePIIReplacements(replacements, sourceText = "") {
  if (!Array.isArray(replacements)) {
    return [];
  }
  return replacements
    .map((replacement) => {
      const startByteOffset = safeNumber(replacement.start);
      const endByteOffset = safeNumber(replacement.end);
      return {
        type: String(replacement.type || "UNKNOWN"),
        token: String(replacement.token || ""),
        value: String(replacement.value || ""),
        start: byteOffsetToStringIndex(sourceText, startByteOffset),
        end: byteOffsetToStringIndex(sourceText, endByteOffset),
      };
    })
    .filter((replacement) => replacement.token && replacement.end >= replacement.start)
    .sort((left, right) => left.start - right.start || left.end - right.end);
}

function renderTextWithFindings(sourceText, findings, mapFinding) {
  if (!findings.length) {
    return escapeHtml(sourceText).replaceAll("\n", "<br>");
  }

  let cursor = 0;
  let html = "";
  findings.forEach((finding) => {
    html += escapeHtml(sourceText.slice(cursor, finding.start)).replaceAll("\n", "<br>");
    const segment = mapFinding(finding);
    const styleAttribute = segment.style ? ` style="${segment.style}"` : "";
    html += `<span class="preview-highlight ${segment.className}"${styleAttribute}>${escapeHtml(segment.text)}</span>`;
    cursor = finding.end;
  });
  html += escapeHtml(sourceText.slice(cursor)).replaceAll("\n", "<br>");
  return html;
}

function renderHighlightedSource(sourceText, findings) {
  return renderTextWithFindings(sourceText, findings, (finding) => ({
    text: finding.value,
    className: "preview-highlight-enabled",
    style: styleAttributeForType(finding.type),
  }));
}

function renderHighlightedResult(sourceText, findings) {
  return renderTextWithFindings(sourceText, findings, (finding) => ({
    text: finding.token,
    className: "preview-highlight-enabled",
    style: styleAttributeForType(finding.type),
  }));
}

const piiMarkerPattern = /\uE000(\d+)\uE001/g;

function injectPIIMarkers(content, replacements) {
  if (!replacements.length) {
    return { content, markers: [] };
  }

  let cursor = 0;
  let output = "";
  const markers = [];

  replacements.forEach((replacement) => {
    output += content.slice(cursor, replacement.start);
    const markerIndex = markers.length;
    output += `\uE000${markerIndex}\uE001`;
    markers.push(replacement);
    cursor = replacement.end;
  });

  output += content.slice(cursor);
  return { content: output, markers };
}

function renderPIIToken(replacement, key, toggledTokens, onToggle) {
  const isTokenVisible = toggledTokens.has(replacement.token);
  const displayText = isTokenVisible ? replacement.token : replacement.value;
  const theme = themeForType(replacement.type);

  return (
    <button
      key={key}
      type="button"
      className={`chat-pii-toggle ${isTokenVisible ? "chat-pii-toggle-token" : ""}`}
      style={{
        "--type-highlight-bg": theme.background,
        "--type-highlight-border": theme.border,
        "--type-highlight-text": theme.text,
      }}
      title={isTokenVisible ? replacement.value : replacement.token}
      onClick={() => onToggle(replacement.token)}
    >
      <span className="chat-pii-toggle-text">{displayText}</span>
      <span className="chat-pii-toggle-hover">{isTokenVisible ? replacement.value : replacement.token}</span>
    </button>
  );
}

function renderTextWithPIIMarkers(text, keyPrefix, markers, toggledTokens, onToggle) {
  if (!markers.length) {
    return [text];
  }

  const parts = [];
  let cursor = 0;
  let match;

  piiMarkerPattern.lastIndex = 0;
  while ((match = piiMarkerPattern.exec(text)) !== null) {
    if (match.index > cursor) {
      parts.push(text.slice(cursor, match.index));
    }
    const replacement = markers[Number(match[1])];
    if (replacement) {
      parts.push(renderPIIToken(replacement, `${keyPrefix}-pii-${match.index}`, toggledTokens, onToggle));
    } else {
      parts.push(match[0]);
    }
    cursor = match.index + match[0].length;
  }

  if (cursor < text.length) {
    parts.push(text.slice(cursor));
  }

  piiMarkerPattern.lastIndex = 0;
  return parts;
}

function extractPlainText(node) {
  if (!node) {
    return "";
  }
  if (node.nodeType === Node.TEXT_NODE) {
    return (node.textContent || "").replaceAll("\u200b", "");
  }
  if (node.nodeName === "BR") {
    return "\n";
  }
  return Array.from(node.childNodes).map((child) => extractPlainText(child)).join("");
}

function plainTextLengthForRange(range) {
  const container = document.createElement("div");
  container.appendChild(range.cloneContents());
  return extractPlainText(container).length;
}

function getSelectionOffsets(root) {
  if (!root) {
    return { start: 0, end: 0 };
  }
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0) {
    return { start: 0, end: 0 };
  }
  const range = selection.getRangeAt(0);
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) {
    return { start: 0, end: 0 };
  }
  const startRange = range.cloneRange();
  startRange.selectNodeContents(root);
  startRange.setEnd(range.startContainer, range.startOffset);
  const endRange = range.cloneRange();
  endRange.selectNodeContents(root);
  endRange.setEnd(range.endContainer, range.endOffset);
  return {
    start: plainTextLengthForRange(startRange),
    end: plainTextLengthForRange(endRange),
  };
}

function setRangeBoundary(range, root, target, setter) {
  let remaining = Math.max(0, target);
  const walker = document.createTreeWalker(
    root,
    NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT,
    {
      acceptNode(node) {
        if (node.nodeType === Node.TEXT_NODE) {
          return NodeFilter.FILTER_ACCEPT;
        }
        return node.nodeName === "BR" ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_SKIP;
      },
    },
  );

  let current = walker.nextNode();
  while (current) {
    if (current.nodeType === Node.TEXT_NODE) {
      const textLength = current.textContent?.length || 0;
      if (remaining <= textLength) {
        range[setter](current, remaining);
        return;
      }
      remaining -= textLength;
    } else if (current.nodeName === "BR") {
      if (remaining === 0) {
        if (setter === "setStart") {
          range.setStartBefore(current);
        } else {
          range.setEndBefore(current);
        }
        return;
      }
      if (remaining === 1) {
        if (setter === "setStart") {
          range.setStartAfter(current);
        } else {
          range.setEndAfter(current);
        }
        return;
      }
      remaining -= 1;
    }
    current = walker.nextNode();
  }

  range[setter](root, root.childNodes.length);
}

function restoreSelectionOffsets(root, offsets) {
  if (!root || !offsets) {
    return;
  }
  const selection = window.getSelection();
  if (!selection) {
    return;
  }
  const range = document.createRange();
  setRangeBoundary(range, root, offsets.start, "setStart");
  setRangeBoundary(range, root, offsets.end, "setEnd");
  selection.removeAllRanges();
  selection.addRange(range);
}

function insertPlainTextAtSelection(text) {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0) {
    return;
  }
  const range = selection.getRangeAt(0);
  range.deleteContents();
  const lines = String(text).replace(/\r\n?/g, "\n").split("\n");
  const fragment = document.createDocumentFragment();

  lines.forEach((line, index) => {
    if (index > 0) {
      fragment.appendChild(document.createElement("br"));
    }
    if (line) {
      fragment.appendChild(document.createTextNode(line));
    }
  });

  if (lines.length > 0 && lines[lines.length - 1] === "") {
    fragment.appendChild(document.createElement("br"));
    fragment.appendChild(document.createTextNode("\u200b"));
  }

  const lastNode = fragment.lastChild;
  range.insertNode(fragment);
  const nextRange = document.createRange();
  if (lastNode) {
    nextRange.setStartAfter(lastNode);
  } else {
    nextRange.setStart(range.endContainer, range.endOffset);
  }
  nextRange.collapse(true);
  selection.removeAllRanges();
  selection.addRange(nextRange);
}

function RichTextPromptEditor({
  value,
  findings,
  placeholder,
  canSend,
  onChange,
  onSend,
  onInteraction,
}) {
  const editorRef = useRef(null);
  const selectionRef = useRef({ start: 0, end: 0 });
  const html = useMemo(() => renderHighlightedSource(value, findings), [value, findings]);

  useEffect(() => {
    const editor = editorRef.current;
    if (!editor) {
      return;
    }
    if (editor.innerHTML !== html) {
      editor.innerHTML = html;
    }
    if (document.activeElement === editor) {
      restoreSelectionOffsets(editor, selectionRef.current);
    }
  }, [html]);

  function syncFromEditor() {
    const editor = editorRef.current;
    if (!editor) {
      return;
    }
    selectionRef.current = getSelectionOffsets(editor);
    const nextValue = extractPlainText(editor).replace(/\u00a0/g, " ");
    onInteraction();
    onChange(nextValue);
  }

  function handleKeyDown(event) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      if (canSend) {
        onSend();
      }
      return;
    }
    if (event.key === "Enter" && event.shiftKey) {
      event.preventDefault();
      insertPlainTextAtSelection("\n");
      syncFromEditor();
    }
  }

  function handlePaste(event) {
    event.preventDefault();
    insertPlainTextAtSelection(event.clipboardData?.getData("text/plain") || "");
    syncFromEditor();
  }

  return (
    <div
      id="prompt"
      ref={editorRef}
      className="chat-composer-input chat-composer-editor"
      contentEditable
      role="textbox"
      aria-multiline="true"
      data-placeholder={placeholder}
      onBeforeInput={() => {
        const editor = editorRef.current;
        if (!editor) {
          return;
        }
        selectionRef.current = getSelectionOffsets(editor);
      }}
      onInput={syncFromEditor}
      onKeyDown={handleKeyDown}
      onPaste={handlePaste}
      suppressContentEditableWarning
    />
  );
}

function renderInlineMarkdown(text, keyPrefix, markers = [], toggledTokens = new Set(), onToggle = () => {}) {
  const pattern = /(\[[^\]]+\]\((https?:\/\/[^\s)]+)\)|`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*)/g;
  const parts = [];
  let cursor = 0;
  let match;

  while ((match = pattern.exec(text)) !== null) {
    if (match.index > cursor) {
      parts.push(...renderTextWithPIIMarkers(text.slice(cursor, match.index), `${keyPrefix}-text-${cursor}`, markers, toggledTokens, onToggle));
    }

    const token = match[0];
    const key = `${keyPrefix}-${match.index}`;

    if (token.startsWith("[") && match[2]) {
      const closingBracket = token.indexOf("]");
      const label = token.slice(1, closingBracket);
      parts.push(
        <a key={key} href={match[2]} target="_blank" rel="noreferrer">
          {renderTextWithPIIMarkers(label, `${key}-link`, markers, toggledTokens, onToggle)}
        </a>,
      );
    } else if (token.startsWith("`")) {
      parts.push(<code key={key}>{renderTextWithPIIMarkers(token.slice(1, -1), `${key}-code`, markers, toggledTokens, onToggle)}</code>);
    } else if (token.startsWith("**")) {
      parts.push(<strong key={key}>{renderTextWithPIIMarkers(token.slice(2, -2), `${key}-strong`, markers, toggledTokens, onToggle)}</strong>);
    } else if (token.startsWith("*")) {
      parts.push(<em key={key}>{renderTextWithPIIMarkers(token.slice(1, -1), `${key}-em`, markers, toggledTokens, onToggle)}</em>);
    }

    cursor = match.index + token.length;
  }

  if (cursor < text.length) {
    parts.push(...renderTextWithPIIMarkers(text.slice(cursor), `${keyPrefix}-tail-${cursor}`, markers, toggledTokens, onToggle));
  }

  return parts;
}

function renderMarkdown(content, replacements = [], toggledTokens = new Set(), onToggle = () => {}) {
  const injected = injectPIIMarkers(content, replacements);
  const normalized = injected.content.replace(/\r\n?/g, "\n");
  const lines = normalized.split("\n");
  const blocks = [];
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];
    const trimmed = line.trim();

    if (!trimmed) {
      index += 1;
      continue;
    }

    if (trimmed.startsWith("```")) {
      const language = trimmed.slice(3).trim();
      const codeLines = [];
      index += 1;
      while (index < lines.length && !lines[index].trim().startsWith("```")) {
        codeLines.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) {
        index += 1;
      }
      blocks.push(
        <pre key={`code-${blocks.length}`} className="chat-bubble-code-block">
          <code data-language={language || undefined}>{codeLines.join("\n")}</code>
        </pre>,
      );
      continue;
    }

    const headingMatch = line.match(/^(#{1,6})\s+(.+)$/);
    if (headingMatch) {
      const level = headingMatch[1].length;
      const HeadingTag = `h${level}`;
      blocks.push(
        <HeadingTag key={`heading-${blocks.length}`}>
          {renderInlineMarkdown(headingMatch[2], `heading-${blocks.length}`, injected.markers, toggledTokens, onToggle)}
        </HeadingTag>,
      );
      index += 1;
      continue;
    }

    if (/^>\s?/.test(trimmed)) {
      const quoteLines = [];
      while (index < lines.length && /^>\s?/.test(lines[index].trim())) {
        quoteLines.push(lines[index].trim().replace(/^>\s?/, ""));
        index += 1;
      }
      blocks.push(
        <blockquote key={`quote-${blocks.length}`}>
          {renderInlineMarkdown(quoteLines.join(" "), `quote-${blocks.length}`, injected.markers, toggledTokens, onToggle)}
        </blockquote>,
      );
      continue;
    }

    if (/^[-*]\s+/.test(trimmed)) {
      const items = [];
      while (index < lines.length && /^[-*]\s+/.test(lines[index].trim())) {
        items.push(lines[index].trim().replace(/^[-*]\s+/, ""));
        index += 1;
      }
      blocks.push(
        <ul key={`ul-${blocks.length}`}>
          {items.map((item, itemIndex) => (
            <li key={`ul-${blocks.length}-${itemIndex}`}>
              {renderInlineMarkdown(item, `ul-${blocks.length}-${itemIndex}`, injected.markers, toggledTokens, onToggle)}
            </li>
          ))}
        </ul>,
      );
      continue;
    }

    if (/^\d+\.\s+/.test(trimmed)) {
      const items = [];
      while (index < lines.length && /^\d+\.\s+/.test(lines[index].trim())) {
        items.push(lines[index].trim().replace(/^\d+\.\s+/, ""));
        index += 1;
      }
      blocks.push(
        <ol key={`ol-${blocks.length}`}>
          {items.map((item, itemIndex) => (
            <li key={`ol-${blocks.length}-${itemIndex}`}>
              {renderInlineMarkdown(item, `ol-${blocks.length}-${itemIndex}`, injected.markers, toggledTokens, onToggle)}
            </li>
          ))}
        </ol>,
      );
      continue;
    }

    const paragraphLines = [];
    while (index < lines.length) {
      const candidate = lines[index];
      const candidateTrimmed = candidate.trim();
      if (
        !candidateTrimmed
        || candidateTrimmed.startsWith("```")
        || /^#{1,6}\s+/.test(candidate)
        || /^>\s?/.test(candidateTrimmed)
        || /^[-*]\s+/.test(candidateTrimmed)
        || /^\d+\.\s+/.test(candidateTrimmed)
      ) {
        break;
      }
      paragraphLines.push(candidateTrimmed);
      index += 1;
    }

    blocks.push(
      <p key={`p-${blocks.length}`}>
        {renderInlineMarkdown(paragraphLines.join(" "), `p-${blocks.length}`, injected.markers, toggledTokens, onToggle)}
      </p>,
    );
  }

  return blocks;
}

function ProviderStatusDot({ available }) {
  return <span className={`settings-provider-dot ${available ? "settings-provider-dot-live" : ""}`} aria-hidden="true" />;
}

function ProviderCard({
  provider,
  selectedProviderID,
  selectedMethodID,
  config,
  onSelectProvider,
  onSelectMethod,
  onConfigChange,
  onSave,
  saving,
  text,
  claudeOAuth,
  claudeOAuthCode,
  onClaudeOAuthCodeChange,
  onClaudeOAuthStart,
  onClaudeOAuthSubmit,
  onClaudeOAuthCancel,
  onClaudeOAuthUnlink,
  claudeOAuthBusy,
}) {
  const expanded = provider.id === selectedProviderID;
  const currentMethod = expanded
    ? provider.methods?.find((method) => method.id === selectedMethodID) || getAvailableMethod(provider)
    : getAvailableMethod(provider);
  const isClaudeOAuth = provider.id === "claude" && currentMethod?.id === "oauth_token";
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
            {isClaudeOAuth ? null : <p>{text.providerHelp}</p>}
            {provider.id === "gemini" ? (
              <p>
                {text.geminiFreeAPIKeyPrefix}{" "}
                <a
                  className="workspace-inline-link"
                  href="https://aistudio.google.com/app/apikey"
                  target="_blank"
                  rel="noreferrer"
                >
                  {text.geminiFreeAPIKeyLink}
                </a>
                .
              </p>
            ) : null}
            {isClaudeOAuth ? (
              <p>
                {claudeOAuth.pending
                  ? text.claudeOAuthPending
                  : claudeOAuth.linked
                    ? text.claudeOAuthLinked
                    : text.claudeOAuthNotLinked}
              </p>
            ) : null}
          </div>

          {provider.methods?.length > 1 ? (
            <div className="workspace-field">
              <span className="workspace-field-label">{text.methodLabel}</span>
              <div className="settings-method-toggle" role="tablist" aria-label={text.methodLabel}>
                {provider.methods.map((method) => {
                  const selected = method.id === currentMethod?.id;
                  return (
                    <button
                      key={method.id}
                      className={`settings-method-toggle-button ${selected ? "settings-method-toggle-button-active" : ""}`}
                      type="button"
                      role="tab"
                      aria-selected={selected}
                      onClick={() => onSelectMethod(provider.id, method.id)}
                    >
                      {method.label}
                    </button>
                  );
                })}
              </div>
            </div>
          ) : null}

          {isClaudeOAuth && claudeOAuth.pending && claudeOAuth.authorizationURL ? (
            <div className="workspace-field">
              <p className="workspace-help-text">
                {text.claudeOAuthOpenLinkPrefix}{" "}
                <a
                  className="workspace-inline-link"
                  href={claudeOAuth.authorizationURL}
                  target="_blank"
                  rel="noreferrer"
                >
                  {text.claudeOAuthAuthorizationURL}
                </a>
                .
              </p>
            </div>
          ) : null}

          {isClaudeOAuth && claudeOAuth.pending && claudeOAuth.requiresCode ? (
            <label className="workspace-field" htmlFor={`${provider.id}-oauth-code`}>
              <span className="workspace-field-label">
                {text.claudeOAuthCodeLabel}
                <span className="workspace-field-required">{text.fieldRequired}</span>
              </span>
              <input
                id={`${provider.id}-oauth-code`}
                className="workspace-input"
                type="text"
                value={claudeOAuthCode}
                onChange={(event) => onClaudeOAuthCodeChange(event.target.value)}
                placeholder={text.claudeOAuthCodePlaceholder}
              />
            </label>
          ) : null}

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
                  placeholder={field.secret && currentMethod?.available ? "******" : field.placeholder || ""}
                />
              ) : (
                <input
                  id={`${provider.id}-${field.key}`}
                  className="workspace-input"
                  type={field.input_type === "password" ? "password" : "text"}
                  value={config[field.key] || ""}
                  onChange={(event) => onConfigChange(field.key, event.target.value)}
                  placeholder={field.secret && currentMethod?.available ? "******" : field.placeholder || ""}
                />
              )}
            </label>
          ))}

          {isClaudeOAuth ? (
            <div className="chat-composer-right">
              {claudeOAuth.pending ? (
                <>
                  {claudeOAuth.requiresCode ? (
                    <button
                      className="settings-provider-save-button"
                      type="button"
                      onClick={() => onClaudeOAuthSubmit()}
                      disabled={!claudeOAuthCode.trim() || claudeOAuthBusy}
                    >
                      {claudeOAuthBusy ? text.saving : text.claudeOAuthSubmit}
                    </button>
                  ) : null}
                  <button
                    className="chat-settings-button"
                    type="button"
                    onClick={() => onClaudeOAuthCancel()}
                    disabled={claudeOAuthBusy}
                  >
                    {text.claudeOAuthCancel}
                  </button>
                </>
              ) : claudeOAuth.linked ? (
                <button
                  className="chat-settings-button settings-disconnect-button"
                  type="button"
                  onClick={() => onClaudeOAuthUnlink()}
                  disabled={claudeOAuthBusy}
                >
                  {text.claudeOAuthDisconnect}
                </button>
              ) : (
                <button
                  className="settings-provider-save-button"
                  type="button"
                  onClick={() => onClaudeOAuthStart()}
                  disabled={claudeOAuthBusy}
                >
                  {claudeOAuthBusy ? text.saving : text.claudeOAuthStart}
                </button>
              )}
            </div>
          ) : (
            <button
              className="settings-provider-save-button"
              type="button"
              onClick={() => onSave(provider.id, currentMethod?.id)}
              disabled={!hasRequiredValues || saving}
            >
              {saving ? text.saving : text.saveKey}
            </button>
          )}
        </div>
      ) : null}
    </section>
  );
}

function AttachmentBadge({ attachment, text }) {
  if (!attachment) return null;
  const size = attachment.size ? `${Math.max(1, Math.round(attachment.size / 1024))} KB` : "";
  const issue = attachment.status === "passthrough" || attachment.status === "removed";
  return (
    <div className={`chat-attachment-badge ${issue ? "chat-attachment-badge-warning" : ""}`}>
      <span className="chat-attachment-icon" aria-hidden="true">▱</span>
      <span><strong>{attachment.filename}</strong><small>{[attachment.media_type, size].filter(Boolean).join(" · ")}</small></span>
      {issue ? <em>{attachment.status === "passthrough" ? text.attachmentNotStored : text.attachmentRemoved}</em> : null}
    </div>
  );
}

function ChatBubble({ role, content, label, pending = false, pendingLabel = "", piiReplacements = [], attachment, text }) {
  const replacements = useMemo(() => normalizePIIReplacements(piiReplacements, content), [content, piiReplacements]);
  const injectedPlainContent = useMemo(() => injectPIIMarkers(content, replacements), [content, replacements]);
  const replacementsKey = useMemo(
    () => replacements.map((replacement) => (
      `${replacement.token}:${replacement.start}:${replacement.end}:${replacement.value}`
    )).join("|"),
    [replacements],
  );
  const [toggledTokens, setToggledTokens] = useState(() => new Set());

  useEffect(() => {
    setToggledTokens(new Set());
  }, [content, replacementsKey]);

  function handleToggle(token) {
    setToggledTokens((current) => {
      const next = new Set(current);
      if (next.has(token)) {
        next.delete(token);
      } else {
        next.add(token);
      }
      return next;
    });
  }

  return (
    <article className={`chat-bubble-row chat-bubble-row-${role}`}>
      <div className={`chat-bubble chat-bubble-${role} ${pending ? "chat-bubble-pending" : ""}`}>
        <span className="chat-bubble-label">{label}</span>
        <AttachmentBadge attachment={attachment} text={text} />
        {pending ? (
          <div className="chat-bubble-loader" aria-label={pendingLabel} role="status">
            <span />
            <span />
            <span />
          </div>
        ) : (
          role === "assistant" ? (
            <div className="chat-bubble-markdown chat-bubble-pii-content">
              {renderMarkdown(content, replacements, toggledTokens, handleToggle)}
            </div>
          ) : (
            <p className={replacements.length > 0 ? "chat-bubble-pii-content" : ""}>
              {replacements.length > 0
                ? renderTextWithPIIMarkers(
                  injectedPlainContent.content,
                  `${role}-plain`,
                  injectedPlainContent.markers,
                  toggledTokens,
                  handleToggle,
                )
                : content}
            </p>
          )
        )}
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
  const [attachment, setAttachment] = useState(null);
  const [isDraggingFile, setIsDraggingFile] = useState(false);
  const [anonymizedPrompt, setAnonymizedPrompt] = useState("");
  const [previewSourceText, setPreviewSourceText] = useState("");
  const [previewFindings, setPreviewFindings] = useState([]);
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
  const [claudeOAuth, setClaudeOAuth] = useState(() => normalizeClaudeOAuthStatus());
  const [claudeOAuthCode, setClaudeOAuthCode] = useState("");
  const [claudeOAuthAction, setClaudeOAuthAction] = useState("");
  const anonymizeRequestRef = useRef(0);
  const threadRef = useRef(null);
  const fileInputRef = useRef(null);
  const dragDepthRef = useRef(0);
  const storedSelectionRef = useRef(readStoredWorkspaceSelection());
  const lastOpenedClaudeOAuthURLRef = useRef("");

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
      claudeOAuthStatus = claudeOAuth,
    } = options;
    setLoadingProviders(true);
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/providers"));
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || "Failed to load providers");
      }
      setRequestError("");
      const nextProviders = mergeProvidersWithClaudeOAuth(payload.providers || [], claudeOAuthStatus, text);
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

  function applyClaudeOAuthState(status) {
    const nextStatus = normalizeClaudeOAuthStatus(status);
    setClaudeOAuth(nextStatus);
    setProviders((current) => mergeProvidersWithClaudeOAuth(current, nextStatus, text));
    if (!nextStatus.pending || !nextStatus.requiresCode) {
      setClaudeOAuthCode("");
    }
    return nextStatus;
  }

  async function syncClaudeOAuthLinkedState(nextStatus, options = {}) {
    const { keepCurrentModel = false } = options;
    if (!nextStatus?.linked) {
      return;
    }
    setProviderID("claude");
    setMethodID("oauth_token");
    await loadProviders({
      preserveSelection: true,
      nextProviderID: "claude",
      nextMethodID: "oauth_token",
      nextModel: keepCurrentModel && selectedProvider?.id === "claude" ? model : "",
      keepConfig: true,
      claudeOAuthStatus: nextStatus,
    });
  }

  async function fetchClaudeOAuthStatus(options = {}) {
    const { silent = false } = options;
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/providers/claude/oauth/status"));
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      return applyClaudeOAuthState(payload);
    } catch (error) {
      if (!silent) {
        setRequestError(error.message);
      }
      return null;
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
  const activePreviewMatchesPrompt = previewSourceText === prompt;
  const displayPreviewFindings = previewSourceText && prompt.startsWith(previewSourceText)
    ? previewFindings
    : [];
  const anonymizedPreviewHTML = useMemo(
    () => renderHighlightedResult(prompt, displayPreviewFindings),
    [prompt, displayPreviewFindings],
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
    if (!settingsOpen) {
      return;
    }
    fetchClaudeOAuthStatus({ silent: true });
  }, [settingsOpen]);

  useEffect(() => {
    const authorizationURL = claudeOAuth.authorizationURL;
    if (!claudeOAuth.pending || !authorizationURL) {
      lastOpenedClaudeOAuthURLRef.current = "";
      return;
    }
    if (lastOpenedClaudeOAuthURLRef.current === authorizationURL) {
      return;
    }
    lastOpenedClaudeOAuthURLRef.current = authorizationURL;
    window.open(authorizationURL, "_blank", "noopener,noreferrer");
  }, [claudeOAuth.authorizationURL, claudeOAuth.pending]);

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
      setAttachment(null);
      setAnonymizedPrompt("");
      setPreviewSourceText("");
      setPreviewFindings([]);
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
        const initialClaudeOAuth = await fetchClaudeOAuthStatus({ silent: true });
        await loadProviders({ claudeOAuthStatus: initialClaudeOAuth || claudeOAuth });
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
      setPreviewSourceText("");
      setPreviewFindings([]);
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
        const nextSourceText = String(payload.original_text || prompt);
        setAnonymizedPrompt(String(payload.anonymized_text || ""));
        setPreviewSourceText(nextSourceText);
        setPreviewFindings(normalizePreviewFindings(payload.findings, nextSourceText));
        setRequestError("");
        setStatus(text.anonymizedStatus);
      } catch (error) {
        if (anonymizeRequestRef.current !== requestID) {
          return;
        }
        setPreviewSourceText("");
        setPreviewFindings([]);
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

  function updateMethod(nextProviderID, nextMethodID) {
    if (providerID !== nextProviderID) {
      setProviderID(nextProviderID);
    }
    setMethodID(nextMethodID);
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

  async function startClaudeOAuth() {
    setClaudeOAuthAction("start");
    setRequestError("");
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/providers/claude/oauth/start"), {
        method: "POST",
      });
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      const nextStatus = applyClaudeOAuthState(payload);
      setProviderID("claude");
      setMethodID("oauth_token");
      await loadProviders({
        preserveSelection: true,
        nextProviderID: "claude",
        nextMethodID: "oauth_token",
        nextModel: model,
        keepConfig: true,
        claudeOAuthStatus: nextStatus,
      });
      if (nextStatus.linked) {
        await syncClaudeOAuthLinkedState(nextStatus, { keepCurrentModel: true });
      }
    } catch (error) {
      setRequestError(error.message);
    } finally {
      setClaudeOAuthAction("");
    }
  }

  async function submitClaudeOAuth() {
    setClaudeOAuthAction("submit");
    setRequestError("");
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/providers/claude/oauth/submit"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ code: claudeOAuthCode.trim() }),
      });
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      const nextStatus = applyClaudeOAuthState(payload);
      await syncClaudeOAuthLinkedState(nextStatus, { keepCurrentModel: true });
    } catch (error) {
      setRequestError(error.message);
    } finally {
      setClaudeOAuthAction("");
    }
  }

  async function cancelClaudeOAuth() {
    setClaudeOAuthAction("cancel");
    setRequestError("");
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/providers/claude/oauth/cancel"), {
        method: "POST",
      });
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      const nextStatus = applyClaudeOAuthState(payload);
      await loadProviders({
        preserveSelection: true,
        nextProviderID: providerID,
        nextMethodID: methodID,
        nextModel: model,
        keepConfig: true,
        claudeOAuthStatus: nextStatus,
      });
    } catch (error) {
      setRequestError(error.message);
    } finally {
      setClaudeOAuthAction("");
    }
  }

  async function unlinkClaudeOAuth() {
    setClaudeOAuthAction("unlink");
    setRequestError("");
    try {
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/providers/claude/oauth"), {
        method: "DELETE",
      });
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      const nextStatus = applyClaudeOAuthState(payload);
      await loadProviders({
        preserveSelection: true,
        nextProviderID: providerID,
        nextMethodID: methodID === "oauth_token" ? "api_key" : methodID,
        nextModel: model,
        keepConfig: true,
        claudeOAuthStatus: nextStatus,
      });
    } catch (error) {
      setRequestError(error.message);
    } finally {
      setClaudeOAuthAction("");
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
    if (!anonymizedPrompt && !attachment) {
      return;
    }
    const hasStoredProvider = providers.some((provider) => (
      provider.available || provider.methods?.some((method) => method.available)
    ));
    const isSelectedClaudeOAuth = selectedProvider?.id === "claude" && selectedMethod?.id === "oauth_token";
    const hasSelectedCredentials = isSelectedClaudeOAuth
      ? claudeOAuth.linked
      : (selectedMethod?.fields || [])
        .filter((field) => field.required)
        .every((field) => (config[field.key] || "").trim());
    const selectedMethodReady = isSelectedClaudeOAuth
      ? claudeOAuth.linked
      : Boolean(selectedMethod?.available) || hasSelectedCredentials;
    if ((!hasStoredProvider && !hasSelectedCredentials) || !selectedMethodReady) {
      setSettingsOpen(true);
      return;
    }
    const promptSnapshot = prompt;
    const anonymizedPromptSnapshot = anonymizedPrompt;
    const previewSourceTextSnapshot = previewSourceText;
    const previewFindingsSnapshot = previewFindings;
    const attachmentSnapshot = attachment;
    const previousMessages = messages;
    const displayPromptSnapshot = previewSourceTextSnapshot || promptSnapshot;
    const optimisticUserMessage = {
      ...buildOptimisticMessage("user", displayPromptSnapshot),
      pii_replacements: previewFindingsSnapshot,
      attachment: attachmentSnapshot ? {
        filename: attachmentSnapshot.name,
        media_type: attachmentSnapshot.type || "application/octet-stream",
        size: attachmentSnapshot.size,
        status: "anonymizing",
      } : null,
    };
    const optimisticAssistantMessage = buildOptimisticMessage("assistant", "", { pending: true });

    setSending(true);
    setRequestError("");
    setMessages((current) => [...current, optimisticUserMessage, optimisticAssistantMessage]);
    setPrompt("");
    setAnonymizedPrompt("");
    setPreviewSourceText("");
    setPreviewFindings([]);
    setAttachment(null);
    setStatus(text.sendingStatus);
    try {
      const completionRequest = {
          conversation_id: activeConversationID,
          provider: providerID,
          method: methodID,
          model,
          anonymized_prompt: anonymizedPromptSnapshot,
          config,
      };
      let body = JSON.stringify(completionRequest);
      const headers = { "Content-Type": "application/json" };
      if (attachmentSnapshot) {
        const form = new FormData();
        form.append("request", body);
        form.append("file", attachmentSnapshot, attachmentSnapshot.name);
        body = form;
        delete headers["Content-Type"];
      }
      const response = await fetch(buildBackendURL(backendBaseURL, "/api/ai-workspace/complete"), { method: "POST", headers, body });
      const payload = await readJSON(response);
      if (!response.ok) {
        throw new Error(payload.error || text.providerError);
      }
      const nextConversationID = payload.conversation_id || activeConversationID;
      const restoredResponseText = String(payload.response_text || "");
      const responsePIIReplacements = normalizePIIReplacements(payload.response_pii_replacements);
      optimisticUserMessage.attachment = payload.attachment || optimisticUserMessage.attachment;
      setMessages((current) => {
        if (current.length < 2) {
          return current;
        }
        return [
          ...current.slice(0, -2),
          optimisticUserMessage,
          {
            ...buildOptimisticMessage("assistant", restoredResponseText, { id: optimisticAssistantMessage.id }),
            pii_replacements: responsePIIReplacements,
          },
        ];
      });
      setStatus(text.responseStatus);
      if (payload.warning) setRequestError(payload.warning);
      await loadProviders({
        preserveSelection: true,
        nextProviderID: providerID,
        nextMethodID: methodID,
        nextModel: model,
        keepConfig: true,
      });
      await syncConversations(nextConversationID);
    } catch (error) {
      setMessages(previousMessages);
      setPrompt(promptSnapshot);
      setAnonymizedPrompt(anonymizedPromptSnapshot);
      setPreviewSourceText(previewSourceTextSnapshot);
      setPreviewFindings(previewFindingsSnapshot);
      setAttachment(attachmentSnapshot);
      setStatus(text.anonymizedStatus);
      setRequestError(error.message);
    } finally {
      setSending(false);
    }
  }

  function selectAttachment(file) {
    if (!file) return;
    if (!isSupportedAttachment(file)) {
      setRequestError(text.unsupportedAttachment);
      return;
    }
    setAttachment(normalizeTextAttachment(file));
    setRequestError("");
  }

  function handleDragEnter(event) {
    if (!hasDraggedFiles(event.dataTransfer) || sending) return;
    event.preventDefault();
    dragDepthRef.current += 1;
    setIsDraggingFile(true);
  }

  function handleDragOver(event) {
    if (!hasDraggedFiles(event.dataTransfer) || sending) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
  }

  function handleDragLeave(event) {
    if (!hasDraggedFiles(event.dataTransfer)) return;
    event.preventDefault();
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) setIsDraggingFile(false);
  }

  function handleDrop(event) {
    if (!hasDraggedFiles(event.dataTransfer)) return;
    event.preventDefault();
    dragDepthRef.current = 0;
    setIsDraggingFile(false);
    if (sending) return;
    selectAttachment(event.dataTransfer.files?.[0]);
  }

  const configFields = selectedMethod?.fields || [];
  const isSelectedClaudeOAuth = selectedProvider?.id === "claude" && selectedMethod?.id === "oauth_token";
  const hasSelectedCredentials = isSelectedClaudeOAuth
    ? claudeOAuth.linked
    : configFields
      .filter((field) => field.required)
      .every((field) => (config[field.key] || "").trim());
  const hasConfiguredProvider = providers.some((provider) => (
    provider.available || provider.methods?.some((method) => method.available)
  )) || hasSelectedCredentials;
  const textReady = prompt.trim() ? Boolean(anonymizedPrompt && activePreviewMatchesPrompt) : true;
  const canSend = Boolean((prompt.trim() || attachment) && textReady && !sending && !anonymizing);

  return (
    <div className="chat-app-shell">
      <aside className="chat-sidebar">
        <div className="chat-sidebar-header">
          <a className="brand" href={dashboardURL} aria-label="klovys99 dashboard">
            <img src="/dashboard/assets/klovys99-logo.png" alt="klovys99" className="brand-logo" />
          </a>
        </div>

        <button className="chat-new-conversation" type="button" onClick={startNewConversation} disabled={sending}>
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
              disabled={sending}
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

      <main
        className={`chat-main ${isDraggingFile ? "chat-main-dragging" : ""}`}
        onDragEnter={handleDragEnter}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        {isDraggingFile ? (
          <div className="chat-drop-overlay" aria-hidden="true">
            <div className="chat-drop-overlay-content">
              <span>+</span>
              <strong>{text.dropAttachment}</strong>
              <small>{text.dropAttachmentFormats}</small>
            </div>
          </div>
        ) : null}
        <section ref={threadRef} className="chat-thread">
          {messages.length === 0 ? (
            <div className="chat-empty-state">
              <h1>{text.welcomeTitle}</h1>
            </div>
          ) : (
            <div className="chat-message-list">
              {messages.map((message, index) => (
                <ChatBubble
                  key={message.id || `${message.created_at || index}-${index}`}
                  role={message.role}
                  content={message.content}
                  piiReplacements={message.pii_replacements}
                  attachment={message.attachment}
                  text={text}
                  pending={message.pending}
                  pendingLabel={text.responsePending}
                  label={message.role === "user" ? text.sentLabel : `${selectedProvider?.label || "AI"} ${text.receivedLabel}`}
                />
              ))}
            </div>
          )}
        </section>

        <section className="chat-composer-wrap">
          <div className="chat-anonymized-preview">
            {prompt ? (
              <div
                className={`chat-anonymized-preview-body ${displayPreviewFindings.length === 0 ? "chat-anonymized-preview-body-empty" : ""}`}
                dangerouslySetInnerHTML={{ __html: anonymizedPreviewHTML }}
              />
            ) : (
              <pre className="chat-anonymized-preview-body chat-anonymized-preview-body-empty">
                {text.noAnonymizedPrompt}
              </pre>
            )}
          </div>

          <div className="chat-composer">
            {attachment ? (
              <div className="chat-selected-attachment">
                <AttachmentBadge attachment={{ filename: attachment.name, media_type: attachment.type, size: attachment.size, status: "selected" }} text={text} />
                <button type="button" onClick={() => setAttachment(null)} aria-label={text.removeAttachment}>×</button>
              </div>
            ) : null}
            <RichTextPromptEditor
              value={prompt}
              findings={displayPreviewFindings}
              placeholder={text.promptPlaceholder}
              canSend={canSend}
              onSend={handleSend}
              onInteraction={() => setRequestError("")}
              onChange={setPrompt}
            />
            <input
              ref={fileInputRef}
              className="chat-file-input"
              type="file"
              accept={acceptedAttachmentTypes}
              onChange={(event) => {
                selectAttachment(event.target.files?.[0]);
                event.target.value = "";
              }}
            />
            <button className="chat-send-button" type="button" onClick={handleSend} disabled={!canSend}>
              ↑
            </button>

            <div className="chat-composer-footer">
              <button className="chat-attach-button" type="button" onClick={() => fileInputRef.current?.click()} disabled={sending} aria-label={text.addAttachment}>+</button>
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
                      selectedProviderID={providerID}
                      selectedMethodID={methodID}
                      config={provider.id === providerID ? config : {}}
                      onSelectProvider={updateProvider}
                      onSelectMethod={updateMethod}
                      onConfigChange={updateConfig}
                      onSave={saveProviderCredentials}
                      saving={savingProviderID === provider.id}
                      text={text}
                      claudeOAuth={claudeOAuth}
                      claudeOAuthCode={claudeOAuthCode}
                      onClaudeOAuthCodeChange={setClaudeOAuthCode}
                      onClaudeOAuthStart={startClaudeOAuth}
                      onClaudeOAuthSubmit={submitClaudeOAuth}
                      onClaudeOAuthCancel={cancelClaudeOAuth}
                      onClaudeOAuthUnlink={unlinkClaudeOAuth}
                      claudeOAuthBusy={claudeOAuthAction !== ""}
                    />
                  ))}
                </div>
              </div>
            </div>
          </section>
        </div>
      ) : null}
    </div>
  );
}
