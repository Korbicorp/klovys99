"use strict";

const refreshIntervalMs = 5000;
const svgNamespace = "http://www.w3.org/2000/svg";
const colors = ["#076cd8", "#262626", "#16a34a", "#dc2626", "#9333ea", "#f59e0b", "#0f766e", "#64748b"];
const numberFormat = new Intl.NumberFormat("en-US");
const percentFormat = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });
const timeFormat = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
});

const typeDescriptions = {
  EMAIL: "Email addresses detected in prompts or context.",
  IP: "IP addresses, network endpoints, or infrastructure identifiers.",
  PHONE: "Phone numbers that can identify a person or organization contact.",
  NIR: "French social security identifiers.",
  FIRST_NAME: "First names detected as personal information.",
  LAST_NAME: "Last names detected as personal information.",
  ADDRESS: "Postal addresses or address-like location details.",
  IBAN: "Bank account identifiers.",
  CREDIT_CARD: "Payment card numbers.",
  MAC_ADDRESS: "Device network hardware identifiers.",
  CRYPTO: "Cryptographic keys, wallets, tokens, or similar sensitive values.",
  SECRET: "API keys, passwords, tokens, credentials, or secret-like values.",
  GENERIC_ID: "Generic internal identifiers, account IDs, UUIDs, or labeled IDs.",
  NUMERIC_ID: "Numeric identifiers that may refer to users, tickets, orders, or records.",
  REFERENCE_ID: "Business references, case IDs, tickets, invoices, or tracking IDs.",
  NAME: "Names detected when first and last name boundaries are ambiguous.",
  PERSON_NAME: "Full person names.",
  LOCATION: "Locations, cities, countries, or place names.",
  ORGANIZATION: "Company, institution, or organization names.",
  CONTEXT_IDENTIFIER: "Context-specific identifiers extracted from nearby labels.",
  OTHER_PII: "Other personal information detected by external recognizers.",
  DATE: "Dates that may reveal personal, contractual, or operational context.",
  BLOOD_TYPE: "Medical blood type information.",
  DOCUMENT_ID: "Document, passport, identity, or administrative identifiers.",
  VEHICLE_PLATE: "Vehicle registration identifiers.",
  MEDICAL_PROVIDER: "Healthcare provider names or medical institution references.",
  SCHOOL: "School or education institution names.",
  EMPLOYER: "Employer or workplace identifiers.",
  PET_IDENTIFIER: "Pet names or pet-related identifiers that can reveal personal context.",
};

const defaultProtectionOptions = Object.keys(typeDescriptions).map((type) => ({
  type,
  enabled: true,
}));

const elements = {
  status: document.querySelector("#connectionStatus"),
  refreshButton: document.querySelector("#refreshButton"),
  resetButton: document.querySelector("#resetButton"),
	lastUpdated: document.querySelector("#lastUpdated"),
	protectionPanel: document.querySelector(".protection-panel"),
	protectionRate: document.querySelector("#protectionRate"),
  protectedRatio: document.querySelector("#protectedRatio"),
  protectionBarFill: document.querySelector("#protectionBarFill"),
  protectedBarLabel: document.querySelector("#protectedBarLabel"),
  totalBarLabel: document.querySelector("#totalBarLabel"),
  topExposureType: document.querySelector("#topExposureType"),
  topExposureCount: document.querySelector("#topExposureCount"),
  topExposureDescription: document.querySelector("#topExposureDescription"),
  llmErrors: document.querySelector("#llmErrors"),
  proxyErrors: document.querySelector("#proxyErrors"),
  requestBodyErrors: document.querySelector("#requestBodyErrors"),
  pieChart: document.querySelector("#pieChart"),
  pieTotal: document.querySelector("#pieTotal"),
  pieUnit: document.querySelector("#pieUnit"),
  pieLegend: document.querySelector("#pieLegend"),
  pieEmpty: document.querySelector("#pieEmpty"),
  typeRows: document.querySelector("#typeRows"),
  typeEmpty: document.querySelector("#typeEmpty"),
  timelineRows: document.querySelector("#timelineRows"),
  timelineEmpty: document.querySelector("#timelineEmpty"),
};

let loading = false;

elements.refreshButton.addEventListener("click", () => {
  void loadStats();
});

elements.resetButton.addEventListener("click", () => {
  void resetStats();
});

void loadStats();
window.setInterval(() => {
  void loadStats();
}, refreshIntervalMs);

async function loadStats() {
  if (loading) {
    return;
  }
  loading = true;
  setStatus("loading", "Loading");
  try {
    const response = await fetch("/api/stats", { cache: "no-store" });
    if (!response.ok) {
      throw new Error(`Stats API returned ${response.status}`);
    }
    const summary = normalizeSummary(await response.json());
    renderSummary(summary);
    setStatus("live", "Live");
    elements.lastUpdated.textContent = new Date().toLocaleTimeString("en-US", {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch (error) {
    console.error(error);
    setStatus("error", "Unavailable");
  } finally {
    loading = false;
  }
}

async function resetStats() {
  const confirmed = window.confirm("Reset all local stats? This removes active and rotated stats files.");
  if (!confirmed) {
    return;
  }

  elements.resetButton.disabled = true;
  try {
    const response = await fetch("/api/stats/reset", { method: "POST" });
    if (!response.ok) {
      throw new Error(`Stats reset returned ${response.status}`);
    }
    await loadStats();
  } catch (error) {
    console.error(error);
    setStatus("error", "Reset failed");
  } finally {
    elements.resetButton.disabled = false;
  }
}

function setStatus(status, label) {
  elements.status.dataset.status = status;
  elements.status.textContent = label;
}

function normalizeSummary(summary) {
  return {
    total_requests: safeNumber(summary.total_requests),
    anonymized_requests: safeNumber(summary.anonymized_requests),
    llm_errors: safeNumber(summary.llm_errors),
    proxy_errors: safeNumber(summary.proxy_errors),
    request_body_errors: safeNumber(summary.request_body_errors),
    total_replacements: safeNumber(summary.total_replacements),
    counts_by_type: Array.isArray(summary.counts_by_type) ? summary.counts_by_type : [],
    timeline: Array.isArray(summary.timeline) ? summary.timeline : [],
    protection_options: normalizeProtectionOptions(summary),
  };
}

function renderSummary(summary) {
  renderProtectionCoverage(summary);
  renderTopExposure(summary);
  renderPie(summary);
  renderTypeTable(summary);
  renderTimeline(summary.timeline);
  renderHealth(summary);
}

function renderProtectionCoverage(summary) {
  const options = summary.protection_options;
  const total = options.length;
  const enabled = options.filter((option) => option.enabled).length;
  const rate = total > 0 ? (enabled / total) * 100 : 0;
  const boundedRate = Math.min(Math.max(rate, 0), 100);
  const protectionColor = protectionColorForRate(boundedRate);

  elements.protectionRate.textContent = `${percentFormat.format(rate)}%`;
  elements.protectedRatio.textContent = `${formatNumber(enabled)} / ${formatNumber(total)} protections enabled`;
  elements.protectionBarFill.style.width = `${boundedRate}%`;
  elements.protectionPanel.style.setProperty("--protection-color", protectionColor);
  elements.protectedBarLabel.textContent = `${formatNumber(enabled)} enabled`;
  elements.totalBarLabel.textContent = `${formatNumber(total)} available`;
}

function protectionColorForRate(rate) {
  const yellow = [250, 204, 21];
  const orange = [249, 115, 22];
  const green = [22, 163, 74];
  if (rate <= 50) {
    return interpolateColor(yellow, orange, rate / 50);
  }
  return interpolateColor(orange, green, (rate - 50) / 50);
}

function interpolateColor(start, end, amount) {
  const boundedAmount = Math.min(Math.max(amount, 0), 1);
  const channel = (index) => Math.round(start[index] + (end[index] - start[index]) * boundedAmount);
  return `rgb(${channel(0)}, ${channel(1)}, ${channel(2)})`;
}

function normalizeProtectionOptions(summary) {
  const rawOptions = summary.protection_options || summary.protectionOptions;
  if (Array.isArray(rawOptions)) {
    return rawOptions
      .map((option) => {
        const type = String(option.type || option.name || option.id || "").trim();
        if (type === "") {
          return null;
        }
        return {
          type,
          enabled: Boolean(option.enabled ?? option.active ?? option.is_enabled),
        };
      })
      .filter(Boolean);
  }

  const enabledTypes = summary.enabled_protection_types || summary.enabledProtectionTypes || summary.enabled_types || summary.enabledTypes;
  if (Array.isArray(enabledTypes)) {
    const enabledSet = new Set(enabledTypes.map((type) => String(type).trim()).filter(Boolean));
    return defaultProtectionOptions.map((option) => ({
      ...option,
      enabled: enabledSet.has(option.type),
    }));
  }

  const disabledTypes = summary.disabled_protection_types || summary.disabledProtectionTypes || summary.disabled_types || summary.disabledTypes;
  if (Array.isArray(disabledTypes)) {
    const disabledSet = new Set(disabledTypes.map((type) => String(type).trim()).filter(Boolean));
    return defaultProtectionOptions.map((option) => ({
      ...option,
      enabled: !disabledSet.has(option.type),
    }));
  }

  return defaultProtectionOptions;
}

function renderTopExposure(summary) {
  const topType = normalizedTypeCounts(summary)[0];
  if (!topType) {
    elements.topExposureType.textContent = "None yet";
    elements.topExposureCount.textContent = "0 replacements";
    elements.topExposureDescription.textContent = "No sensitive data type has been detected yet.";
    return;
  }

  elements.topExposureType.textContent = topType.type;
  elements.topExposureCount.textContent = `${formatNumber(topType.count)} replacements`;
  elements.topExposureDescription.textContent = `${descriptionForType(topType.type)} This is the most frequent data class klovys99 anonymized locally.`;
}

function renderHealth(summary) {
  renderHealthValue(elements.llmErrors, summary.llm_errors);
  renderHealthValue(elements.proxyErrors, summary.proxy_errors);
  renderHealthValue(elements.requestBodyErrors, summary.request_body_errors);
}

function renderHealthValue(element, value) {
  element.textContent = formatNumber(value);
  element.dataset.hasErrors = value > 0 ? "true" : "false";
}

function renderPie(summary) {
  const typeSlices = normalizedTypeCounts(summary).map((item) => ({
    label: item.type,
    value: item.count,
    description: descriptionForType(item.type),
  }));

  const slices = typeSlices.length > 0 ? typeSlices : requestFallbackSlices(summary);
  const total = slices.reduce((sum, item) => sum + item.value, 0);

  elements.pieEmpty.hidden = total > 0;
  elements.pieTotal.textContent = formatNumber(total);
  elements.pieUnit.textContent = typeSlices.length > 0 ? "items" : "requests";
  elements.pieLegend.innerHTML = "";
  renderPieSvg(slices, total);

  slices.forEach((slice, index) => {
    const item = document.createElement("li");
    item.className = "legend-item";
    item.title = `${slice.label}: ${slice.description}`;
    item.innerHTML = `
      <span class="legend-color" style="background:${colors[index % colors.length]}"></span>
      <span class="legend-label">${escapeHtml(slice.label)}</span>
      <span class="legend-value">${formatNumber(slice.value)}</span>
    `;
    elements.pieLegend.appendChild(item);
  });
}

function renderPieSvg(slices, total) {
  elements.pieChart.replaceChildren();
  if (total <= 0) {
    const circle = createSvgElement("circle", {
      class: "pie-empty-ring",
      cx: "120",
      cy: "120",
      r: "112",
    });
    elements.pieChart.appendChild(circle);
    return;
  }

  let current = 0;
  slices.forEach((slice, index) => {
    const startAngle = -90 + current * 360;
    const sliceShare = slice.value / total;
    const endAngle = -90 + (current + sliceShare) * 360;
    current += sliceShare;

    const path = createSvgElement("path", {
      class: "pie-slice",
      d: describeDonutSegment(120, 120, 112, 58, startAngle, endAngle),
      fill: colors[index % colors.length],
      tabindex: "0",
    });
    const title = createSvgElement("title");
    title.textContent = `${slice.label}: ${formatNumber(slice.value)} (${percentFormat.format(sliceShare * 100)}%). ${slice.description}`;
    path.appendChild(title);
    elements.pieChart.appendChild(path);
  });
}

function requestFallbackSlices(summary) {
  if (summary.total_requests <= 0) {
    return [];
  }
  const unchanged = Math.max(summary.total_requests - summary.anonymized_requests, 0);
  return [
    {
      label: "Anonymized requests",
      value: summary.anonymized_requests,
      description: "Requests where klovys99 replaced at least one sensitive value.",
    },
    {
      label: "Unchanged requests",
      value: unchanged,
      description: "Requests processed without a detected sensitive replacement.",
    },
  ].filter((item) => item.value > 0);
}

function renderTypeTable(summary) {
  const rows = normalizedTypeCounts(summary);
  const total = rows.reduce((sum, item) => sum + item.count, 0);

  elements.typeRows.innerHTML = "";
  elements.typeEmpty.hidden = rows.length > 0;

  rows.forEach((row) => {
    const tr = document.createElement("tr");
    const share = total > 0 ? `${percentFormat.format((row.count / total) * 100)}%` : "0%";
    const description = descriptionForType(row.type);
    tr.title = `${row.type}: ${description}`;
    tr.innerHTML = `
      <td>
        <span class="type-name">${escapeHtml(row.type)}</span>
        <span class="type-description">${escapeHtml(description)}</span>
      </td>
      <td>${formatNumber(row.count)}</td>
      <td>${share}</td>
    `;
    elements.typeRows.appendChild(tr);
  });
}

function renderTimeline(timeline) {
  const buckets = timeline
    .map((bucket) => ({
      bucket: bucket.bucket,
      requests: safeNumber(bucket.requests),
      anonymized: safeNumber(bucket.anonymized_requests),
      replacements: safeNumber(bucket.total_replacements),
      errors: safeNumber(bucket.llm_errors) + safeNumber(bucket.proxy_errors) + safeNumber(bucket.request_body_errors),
    }))
    .filter((bucket) => bucket.requests > 0 || bucket.replacements > 0 || bucket.errors > 0)
    .slice(-12);

  const maxRequests = Math.max(...buckets.map((bucket) => bucket.requests), 1);
  elements.timelineRows.innerHTML = "";
  elements.timelineEmpty.hidden = buckets.length > 0;

  buckets.forEach((bucket) => {
    const row = document.createElement("div");
    row.className = "timeline-row";
    const width = Math.max((bucket.requests / maxRequests) * 100, bucket.requests > 0 ? 8 : 0);
    row.innerHTML = `
      <div class="timeline-time">${escapeHtml(formatBucket(bucket.bucket))}</div>
      <div class="timeline-bar-track">
        <div class="timeline-bar" style="width:${width}%"></div>
        <div class="timeline-details">
          <span>${formatNumber(bucket.anonymized)} anonymized</span>
          <span class="timeline-errors">${formatNumber(bucket.errors)} errors</span>
        </div>
      </div>
      <div class="timeline-count">${formatNumber(bucket.requests)} requests</div>
    `;
    elements.timelineRows.appendChild(row);
  });
}

function normalizedTypeCounts(summary) {
  return summary.counts_by_type
    .map((item) => ({ type: String(item.type || "Unknown"), count: safeNumber(item.count) }))
    .filter((item) => item.count > 0);
}

function descriptionForType(type) {
  return typeDescriptions[type] || "Sensitive data detected by a local or external rule.";
}

function describeDonutSegment(cx, cy, outerRadius, innerRadius, startAngle, endAngle) {
  const fullCircleSafeEnd = endAngle - startAngle >= 360 ? startAngle + 359.999 : endAngle;
  const startOuter = polarToCartesian(cx, cy, outerRadius, startAngle);
  const endOuter = polarToCartesian(cx, cy, outerRadius, fullCircleSafeEnd);
  const startInner = polarToCartesian(cx, cy, innerRadius, fullCircleSafeEnd);
  const endInner = polarToCartesian(cx, cy, innerRadius, startAngle);
  const largeArcFlag = fullCircleSafeEnd - startAngle > 180 ? "1" : "0";

  return [
    `M ${startOuter.x} ${startOuter.y}`,
    `A ${outerRadius} ${outerRadius} 0 ${largeArcFlag} 1 ${endOuter.x} ${endOuter.y}`,
    `L ${startInner.x} ${startInner.y}`,
    `A ${innerRadius} ${innerRadius} 0 ${largeArcFlag} 0 ${endInner.x} ${endInner.y}`,
    "Z",
  ].join(" ");
}

function polarToCartesian(cx, cy, radius, angleInDegrees) {
  const angleInRadians = (angleInDegrees * Math.PI) / 180;
  return {
    x: cx + radius * Math.cos(angleInRadians),
    y: cy + radius * Math.sin(angleInRadians),
  };
}

function createSvgElement(name, attributes = {}) {
  const element = document.createElementNS(svgNamespace, name);
  Object.entries(attributes).forEach(([key, value]) => {
    element.setAttribute(key, value);
  });
  return element;
}

function formatBucket(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "Unknown";
  }
  return timeFormat.format(date);
}

function safeNumber(value) {
  return Number.isFinite(Number(value)) ? Number(value) : 0;
}

function formatNumber(value) {
  return numberFormat.format(safeNumber(value));
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}
